package hook

import (
	"fmt"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/state"
)

// Breaker is the circuit breaker: loop detection, failure budgets,
// tool-call and wall-clock ceilings.
//
// It is wired to BOTH PreToolUse and PostToolUse, and the split matters:
//
//   - PostToolUse observes what happened and increments counters. Blocking
//     here cannot un-run the tool; it only tells the model to stop.
//   - PreToolUse reads the tripped flag and refuses the NEXT call. This is
//     what actually halts a runaway session.
//
// Wiring only one of the two produces a breaker that reports but does not
// stop, which is the failure mode this design exists to prevent.
func Breaker(in Input, cfg config.Config, store *state.Store) Decision {
	switch in.HookEventName {
	case "PreToolUse":
		return breakerPre(in, cfg, store)
	case "PostToolUse":
		return breakerPost(in, cfg, store)
	default:
		return Allow("")
	}
}

func breakerPre(in Input, cfg config.Config, store *state.Store) Decision {
	sess := store.Read(in.SessionID)

	// A breaker that has already tripped stays tripped for the rest of the
	// session. Re-deriving the verdict here would let a counter reset or a
	// changed signature quietly re-arm a session no human has looked at.
	if sess.Tripped != "" {
		return Block("%s already tripped this session (%s).\n"+
			"  Stop and hand back to the human. Do not retry, do not work around it.\n"+
			"  Write what you learned to %s/BLOCKED.md, then summarize and stop.\n"+
			"  To resume after review: orion reset --session %s",
			sess.Tripped, sess.TrippedDetail, cfg.Paths.Plans, sess.ID)
	}

	// Wall clock is checked before the call rather than after, so a long
	// session stops at the next action instead of after one more.
	if limit := time.Duration(cfg.Limits.MaxSessionMinutes) * time.Minute; sess.Elapsed() > limit {
		trip(store, in.SessionID, "breaker/session-time",
			fmt.Sprintf("%s elapsed, limit %s", sess.Elapsed().Round(time.Second), limit))
		return Block("breaker: session has run %s, exceeding the %s limit.\n"+
			"  Long sessions drift. Summarize progress, commit what works, and start fresh.",
			sess.Elapsed().Round(time.Second), limit)
	}

	return Allow("")
}

func breakerPost(in Input, cfg config.Config, store *state.Store) Decision {
	sig := in.Signature()
	failed := in.Failed()
	cmd := normalizeCmd(in.Command())
	file := in.FilePath()
	isEdit := isEditTool(in.ToolName)
	isVerify := looksLikeVerification(in.Command())

	sess, err := store.Update(in.SessionID, func(s *state.Session) {
		s.ToolCalls++
		s.Repeats[sig]++

		if failed {
			s.ConsecFailures++
			if cmd != "" {
				s.CmdFailures[cmd]++
			}
		} else {
			s.ConsecFailures = 0
			if cmd != "" {
				delete(s.CmdFailures, cmd)
			}
		}

		if isEdit {
			s.EditsSinceCheck++
			if file != "" {
				s.FilesTouched[file]++
			}
		}
		// A PASSING verification run resets the edit budget. Running the
		// tests is the point; running anything at all is not.
		if isVerify && !failed {
			s.EditsSinceCheck = 0
		}
	})

	var notes []string
	switch {
	case err == state.ErrLockTimeout:
		notes = append(notes, "breaker state lock timed out; counters may undercount under parallel load")
	case err != nil:
		return Warn("breaker could not persist state (%v). Guardrails are DEGRADED for this session.", err)
	}
	if cfg.Degraded {
		notes = append(notes, cfg.DegradedReason)
	}

	d := verdict(in, cfg, sess)
	if !d.Blocked() {
		// Warn on crossing 80% of the tool budget so the session can wind
		// down on its own terms instead of being cut off mid-thought.
		if warn := cfg.Limits.MaxToolCalls * 4 / 5; sess.ToolCalls == warn {
			notes = append(notes, fmt.Sprintf("breaker at 80%% of tool budget (%d/%d). Start converging.",
				sess.ToolCalls, cfg.Limits.MaxToolCalls))
		}
	} else {
		trip(store, in.SessionID, d.trippedKind, d.trippedDetail)
	}
	d.Notes = append(notes, d.Notes...)
	return d.Decision
}

// tripDecision pairs a verdict with the breaker identity to record.
type tripDecision struct {
	Decision
	trippedKind   string
	trippedDetail string
}

// verdict is the pure decision function: given a snapshot of counters,
// what should happen. Kept free of I/O so it is directly testable.
//
// Order matters. Report the most specific diagnosis first: "you have
// repeated the same call four times" is actionable, "you have made 400
// tool calls" is not.
func verdict(in Input, cfg config.Config, sess *state.Session) tripDecision {
	sig := in.Signature()
	cmd := normalizeCmd(in.Command())

	switch {
	case sess.Repeats[sig] >= cfg.Limits.MaxRepeatIdentical:
		return tripDecision{
			Block("breaker: LOOP. The identical %s call has now run %d times with no change in input.\n"+
				"  Repeating it will not produce a different result.\n"+
				"  Do one of: change the approach, read the error properly, or stop and report.\n"+
				"  Do not retry this call.", in.ToolName, sess.Repeats[sig]),
			"breaker/loop",
			fmt.Sprintf("%s repeated %d times", in.ToolName, sess.Repeats[sig]),
		}

	case cmd != "" && sess.CmdFailures[cmd] >= cfg.Limits.MaxSameCommandFailures:
		return tripDecision{
			Block("breaker: REPEATED FAILURE. %q has failed %d times.\n"+
				"  The command is not going to start working. Diagnose the cause or escalate.\n"+
				"  If this is an environment problem, say so plainly and stop.",
				truncate(cmd, 80), sess.CmdFailures[cmd]),
			"breaker/command-failures",
			fmt.Sprintf("%q failed %d times", truncate(cmd, 60), sess.CmdFailures[cmd]),
		}

	case sess.ConsecFailures >= cfg.Limits.MaxConsecutiveFailures:
		return tripDecision{
			Block("breaker: %d tool calls have failed in a row.\n"+
				"  Something upstream is wrong. Stop guessing, state what you know, and hand back.",
				sess.ConsecFailures),
			"breaker/consecutive-failures",
			fmt.Sprintf("%d failures in a row", sess.ConsecFailures),
		}

	case sess.ToolCalls >= cfg.Limits.MaxToolCalls:
		return tripDecision{
			Block("breaker: BUDGET EXHAUSTED at %d tool calls (limit %d).\n"+
				"  Commit anything that works, write the remainder to %s/BLOCKED.md, and stop.\n"+
				"  Raise limits.max_tool_calls in orion.json only if this task genuinely needs it.",
				sess.ToolCalls, cfg.Limits.MaxToolCalls, cfg.Paths.Plans),
			"breaker/tool-budget",
			fmt.Sprintf("%d tool calls", sess.ToolCalls),
		}

	case sess.EditsSinceCheck >= cfg.Limits.MaxEditsWithoutVerify:
		return tripDecision{
			Block("breaker: %d edits made without a passing verification run.\n"+
				"  Run the test or build command now. Editing further compounds an unchecked change.",
				sess.EditsSinceCheck),
			"breaker/unverified-edits",
			fmt.Sprintf("%d edits with no passing verification", sess.EditsSinceCheck),
		}

	case len(sess.FilesTouched) >= cfg.Limits.MaxFilesTouched:
		return tripDecision{
			Block("breaker: BLAST RADIUS. %d distinct files edited in one session (limit %d).\n"+
				"  A change this wide should be split. Stop and propose a decomposition.",
				len(sess.FilesTouched), cfg.Limits.MaxFilesTouched),
			"breaker/blast-radius",
			fmt.Sprintf("%d distinct files edited", len(sess.FilesTouched)),
		}
	}

	return tripDecision{Decision: Allow("")}
}

func trip(store *state.Store, sessionID, kind, detail string) {
	if kind == "" {
		return
	}
	_, _ = store.Update(sessionID, func(s *state.Session) {
		if s.Tripped == "" {
			s.Tripped = kind
			s.TrippedDetail = detail
		}
	})
}

func isEditTool(name string) bool {
	switch name {
	case "Edit", "Write", "NotebookEdit", "MultiEdit":
		return true
	}
	return false
}

// looksLikeVerification recognizes the commands that constitute checking
// your work. Deliberately narrow: treating any command as verification
// would let `ls` reset the edit budget.
func looksLikeVerification(cmd string) bool {
	c := strings.ToLower(cmd)
	if c == "" {
		return false
	}
	for _, probe := range []string{
		"make test", "make build", "make lint", "make check", "make itest",
		"npm test", "npm run test", "npm run build", "npm run lint",
		"pnpm test", "pnpm build", "yarn test", "yarn build",
		"go test", "go build", "go vet",
		"pytest", "python -m pytest", "tox",
		"cargo test", "cargo build", "cargo clippy",
		"mvn test", "gradle test", "./gradlew test",
		"dotnet test", "bundle exec rspec", "rspec", "phpunit",
	} {
		if strings.Contains(c, probe) {
			return true
		}
	}
	return false
}

// normalizeCmd collapses whitespace so the same command typed with
// different spacing shares a failure bucket.
func normalizeCmd(cmd string) string { return strings.Join(strings.Fields(cmd), " ") }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
