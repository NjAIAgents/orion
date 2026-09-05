package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/state"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// cleanupAllowance is how many cleanup commands a tripped session may still
// run: enough to look at the tree, revert it and commit what compiles, and
// not enough to be useful for anything else.
//
// Bounded on purpose. The allowance exists so a trip is not also an order to
// abandon a modified file (OR-194); a session that can run `git status`
// forever has been given its budget back under another name.
const cleanupAllowance = 6

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
	//
	// Except for the two RECOVERY actions, which must stay open or the trip
	// is a deadlock rather than a breaker. This was found the expensive way:
	// an unverified-edits trip blocked the very `go build` that would have
	// been the verify, and blocked writing plans/BLOCKED.md -- the file the
	// message below instructs the agent to write. Both exits sealed, so every
	// tripped session could only die, with its work uncommitted (OR-119).
	//
	//   1. The stop-note. Writing BLOCKED.md is the breaker's own protocol.
	//   2. For an unverified-edits trip ONLY: a verification command. A
	//      passing verify is the designed reset for that counter, so it is
	//      allowed through, and breakerPost clears the trip when it passes.
	//      Other trip kinds (session-time, tool budget, loops) have no
	//      self-service recovery and stay fully sealed.
	//   3. The CLEANUP ALLOWANCE: a bounded list of git commands that leave
	//      the worktree in a reportable state. "Stop looping" and "stop
	//      acting" are not the same instruction, and treating them as one
	//      left OR-192 with a modified test file, no way to revert it, and
	//      nothing coming back for it. See isCleanupCommand for why this is
	//      a list of forms rather than a permission to use Bash.
	if sess.Tripped != "" {
		if isBlockedNoteWrite(in, cfg) {
			return Allow("")
		}
		if sess.Tripped == "breaker/unverified-edits" &&
			in.ToolName == "Bash" && looksLikeVerification(in.Command()) {
			return Allow("")
		}
		if in.ToolName == "Bash" && isCleanupCommand(in.Command()) {
			if sess.CleanupCalls >= cleanupAllowance {
				return Block("%s tripped this session (%s) and the cleanup allowance\n"+
					"  of %d commands is spent. Nothing further will be allowed.\n"+
					"  Say what is left undone and stop.\n"+
					"  To resume after review: orion reset --session %s",
					sess.Tripped, sess.TrippedDetail, cleanupAllowance, sess.ID)
			}
			// Counted BEFORE the call runs. A post-hoc count would let a
			// session that dies mid-cleanup come back with its allowance
			// intact, which is the reset this must never be.
			after, _ := store.Update(in.SessionID, func(s *state.Session) { s.CleanupCalls++ })
			return Allow(fmt.Sprintf(
				"cleanup allowance: %d of %d used. This tidies the worktree; it does not "+
					"reopen the task, and spending it does not clear the trip.",
				after.CleanupCalls, cleanupAllowance))
		}
		// The recovery line is TRIP-SPECIFIC. It used to be printed on every
		// kind, worded as a conditional ("if the trip is unverified-edits...").
		// Two agents in a row read that as "Bash is still open", tried Bash on
		// a LOOP trip, were refused, and reported the breaker as
		// self-contradictory (OR-143, OR-156). The sentence was technically
		// true and reliably misleading, which for a message whose whole job is
		// to be obeyed is the same as being wrong.
		recovery := "  There is no self-service recovery from this trip. Stop here.\n"
		if sess.Tripped == "breaker/unverified-edits" {
			recovery = "  Running the tests or the build IS still allowed, and is the way out:\n" +
				"  a PASSING verify clears this trip and you may continue.\n"
		}
		// What became of the uncommitted work, stated rather than implied.
		// "Committed for you" printed after a commit that failed is the
		// technically-shaped, reliably-misleading message this codebase keeps
		// having to unlearn (OR-143), so the line reports the real outcome.
		if sess.TripSnapshot != "" {
			recovery += "  Your uncommitted work: " + sess.TripSnapshot + ".\n"
		}
		// The cleanup allowance is named on EVERY trip kind because it exists
		// on every trip kind -- unlike the verify recovery above, stating it
		// unconditionally is accurate. It is spelled as the exact commands
		// rather than "cleanup is allowed": the second wording is the one an
		// agent reads as "Bash is open", which is how OR-143 and OR-156 were
		// both misled by a sentence that was technically true.
		return Block("%s already tripped this session (%s).\n"+
			"  Stop and hand back to the human. Do not retry, do not work around it.\n"+
			"  %s/BLOCKED.md has already been written for you; add what you were\n"+
			"  attempting, what is done and what remains.\n"+
			"%s"+
			"  You may still leave the worktree tidy, %d of %d cleanup commands used:\n"+
			"    git status, git diff, git checkout -- <path>, git restore <path>,\n"+
			"    git add <path>, git commit (adding a commit; --amend is refused)\n"+
			"  Revert what should not survive, COMMIT whatever compiles, then stop.\n"+
			"  Uncommitted work described in a plan file cannot be resumed, and an\n"+
			"  uncommitted change also blocks the next rebase of this branch.\n"+
			"  Nothing else is allowed, and using these does not clear the trip.\n"+
			"  The policy for every trip kind is docs/BREAKERS.md; it is the answer,\n"+
			"  so do not stop to ask which recoveries exist.\n"+
			"  To resume after review: orion reset --session %s",
			sess.Tripped, sess.TrippedDetail, cfg.Paths.Plans, recovery,
			sess.CleanupCalls, cleanupAllowance, sess.ID)
	}

	// Wall clock is checked before the call rather than after, so a long
	// session stops at the next action instead of after one more.
	if limit := time.Duration(cfg.Limits.MaxSessionMinutes) * time.Minute; sess.Elapsed() > limit {
		trip(store, cfg, in.SessionID, "breaker/session-time",
			fmt.Sprintf("%s elapsed, limit %s", sess.Elapsed().Round(time.Second), limit))
		return Block("breaker: session has run %s, exceeding the %s limit.\n"+
			"  Long sessions drift. Summarize progress, commit what works, and start fresh.",
			sess.Elapsed().Round(time.Second), limit)
	}

	return Allow("")
}

func breakerPost(in Input, cfg config.Config, store *state.Store) Decision {
	sig := in.Signature()
	actor := actorKey(in)
	failed := in.Failed()
	cmd := normalizeCmd(in.Command())
	file := in.FilePath()
	isEdit := isEditTool(in.ToolName)
	isVerify := looksLikeVerification(in.Command())

	sess, err := store.Update(in.SessionID, func(s *state.Session) {
		s.ToolCalls++
		rememberAwaited(s, in)
		// A PASSING verification command is exempt from the identical-repeat
		// loop counter. Re-running the tests is the normal edit-test cycle,
		// and it is exactly what the unverified-edits breaker demands after
		// every edit -- counting it as a loop makes the two breakers fight
		// each other (OR-124). A FAILING verify still counts: retrying a red
		// test with nothing changed is a real loop signal.
		//
		// A POLL is exempt on the same precedent. Waiting for a long command
		// to finish and looping look identical from here -- both are the same
		// read of the same path -- so counting the wait meant the only legal
		// way to run a nine-minute suite was to not wait for it (OR-207).
		if isPoll(s, in) {
			s.ConsecPolls++
		} else {
			s.ConsecPolls = 0
		}
		if (!isVerify || failed) && !isPoll(s, in) {
			if s.Repeats[actor] == nil {
				s.Repeats[actor] = map[string]int{}
			}
			s.Repeats[actor][sig]++
		}
		rememberPoll(s, actor, sig, in)

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
			// And it clears an unverified-edits trip: the verify that the
			// trip was demanding has now happened and passed. breakerPre let
			// this command through for exactly this moment. Other trip kinds
			// are not cleared by anything but a human's `orion reset`.
			if s.Tripped == "breaker/unverified-edits" {
				s.Tripped = ""
				s.TrippedDetail = ""
			}
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

	// A cleanup command on an already-tripped session is not re-judged.
	//
	// It cannot be: the counters that tripped are already over their limits,
	// so verdict() would block every call in the allowance breakerPre just
	// granted -- most obviously on a tool-budget trip, where ToolCalls only
	// climbs. An allowance the next hook refuses is not an allowance.
	if sess.Tripped != "" && in.ToolName == "Bash" && isCleanupCommand(in.Command()) {
		return Decision{Code: ExitAllow, Notes: notes}
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
		trip(store, cfg, in.SessionID, d.trippedKind, d.trippedDetail)
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
	actor := actorKey(in)
	cmd := normalizeCmd(in.Command())

	switch {
	case sess.Repeats[actor][sig] >= cfg.Limits.MaxRepeatIdentical:
		return tripDecision{
			Block("breaker: LOOP. The identical %s call has now run %d times with no change in input.\n"+
				"  Repeating it will not produce a different result.\n"+
				"  Do one of: change the approach, read the error properly, or stop and report.\n"+
				"  Do not retry this call.", in.ToolName, sess.Repeats[actor][sig]),
			"breaker/loop",
			fmt.Sprintf("%s repeated %d times", in.ToolName, sess.Repeats[actor][sig]),
		}

	case cfg.Limits.MaxConsecutivePolls > 0 &&
		sess.ConsecPolls >= cfg.Limits.MaxConsecutivePolls:
		return tripDecision{
			Block("breaker: WAITING WITH NO PROGRESS. %d polls in a row and nothing else.\n"+
				"  This run is HEADLESS: a backgrounded command is never announced back to\n"+
				"  you, and ScheduleWakeup does nothing. Nothing will arrive.\n"+
				"  Run long commands in the FOREGROUND and wait for them there.\n"+
				"  If you already have the result you need, say your verdict and stop.",
				sess.ConsecPolls),
			"breaker/no-progress",
			fmt.Sprintf("%d consecutive polls", sess.ConsecPolls),
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

func trip(store *state.Store, cfg config.Config, sessionID, kind, detail string) {
	if kind == "" {
		return
	}
	first := false
	_, _ = store.Update(sessionID, func(s *state.Session) {
		if s.Tripped == "" {
			s.Tripped = kind
			s.TrippedDetail = detail
			first = true
		}
	})
	if first {
		writeBlockedNote(cfg, sessionID, kind, detail)
		if snap := commitTripWork(cfg, kind, detail); snap != "" {
			_, _ = store.Update(sessionID, func(s *state.Session) { s.TripSnapshot = snap })
		}
	}
}

// commitTripWork commits whatever the run had uncommitted, at the moment of
// the trip, and returns a one-line account of what happened -- empty when
// this trip kind does not take a snapshot.
//
// This is the allowance's FIRST act rather than something the agent has to
// think to spend its remaining budget on. OR-189 and OR-191 both finished
// their implementation, both had it green, and both ended orion-failed with
// every line uncommitted; both BLOCKED.md files said so in as many words,
// which means the information needed to act was there and nothing acted on
// it. A branch with commits can be resumed or reviewed. A worktree cannot.
//
// NOT for breaker/unverified-edits, which is the one trip with a designed way
// out: a passing verify clears it and the run continues. A snapshot commit in
// the middle of a run that then succeeds is a "wip:" commit in somebody's
// pull request for no reason.
//
// The snapshot is explicitly unverified, and the message says so. The session
// was stopped for not making progress, so this is preservation for review,
// not a claim the work is correct.
func commitTripWork(cfg config.Config, kind, detail string) string {
	if kind == "breaker/unverified-edits" || cfg.Root == "" {
		return ""
	}
	msg := fmt.Sprintf("wip: snapshot the work uncommitted when %s tripped\n\n"+
		"The breaker tripped: %s. Committed by the breaker itself so the run's\n"+
		"work survives the run; NOTHING here has been verified -- the session was\n"+
		"stopped for not making progress. Review before merging, and see\n"+
		"%s/BLOCKED.md in this worktree for what it was attempting.\n",
		kind, detail, cfg.Paths.Plans)

	// BLOCKED.md is excluded: it is the account of the trip, written for
	// whoever opens the worktree next, and it is not part of the change.
	n, err := workspace.CommitAll(cfg.Root, msg,
		filepath.Join(cfg.Paths.Plans, "BLOCKED.md"), cfg.Paths.State)
	switch {
	case err != nil:
		return fmt.Sprintf("could NOT commit the uncommitted work: %v", err)
	case n == 0:
		return ""
	}
	return fmt.Sprintf("committed %d uncommitted file(s) as an unverified snapshot", n)
}

// writeBlockedNote records the trip in BLOCKED.md as PART OF tripping.
//
// Ordering is the whole point (OR-194). On the OR-192 run the note existed
// only because the agent happened to write it before the breaker closed; one
// tool call later the next reader would have found a modified test file and
// no explanation at all. A note the breaker writes itself cannot be lost to
// the timing of one call.
//
// Appended, never overwritten. A BLOCKED.md already on the branch is
// somebody's account of a different blockage, and destroying it to report
// this one trades one lost explanation for another.
//
// Best-effort by design: a failure to write must not change the verdict. The
// block message still tells the agent to write the note, so the worst case is
// the behaviour that existed before this.
func writeBlockedNote(cfg config.Config, sessionID, kind, detail string) {
	if cfg.Root == "" || cfg.Paths.Plans == "" {
		return // no resolved project root: nowhere to put it that a reader would look
	}
	dir := cfg.Paths.Plans
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cfg.Root, dir)
	}
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "BLOCKED.md"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n## %s tripped\n\n"+
		"- when: %s\n"+
		"- session: `%s`\n"+
		"- detail: %s\n\n"+
		"Written by the breaker at the moment it tripped, so this record exists even\n"+
		"if the session ends on the very next call.\n\n"+
		"The agent should add what it was attempting, what is done and what remains.\n"+
		"It may still run `git status`, `git diff`, `git checkout -- <path>`,\n"+
		"`git restore`, `git add` and `git commit` to leave the worktree reportable.\n\n"+
		"Resume after review with `orion reset --session %s`.\n",
		kind, time.Now().UTC().Format(time.RFC3339), sessionID, detail, sessionID)
}

// actorKey identifies WHICH agent made a call: the main thread or one
// specific subagent. A subagent spawned inside a run shares its parent's
// SessionID -- the harness gives it its own transcript file instead, so
// TranscriptPath is what actually distinguishes them. Without this, two
// agents each innocently reading the same file twice sum to a false loop
// trip that neither one caused (OR-170). SessionID is the fallback for a
// payload that omits transcript_path, matching the old, session-wide
// behavior rather than inventing a new failure mode.
func actorKey(in Input) string {
	if in.TranscriptPath != "" {
		return in.TranscriptPath
	}
	return in.SessionID
}

// isPoll reports whether this call is an agent WAITING rather than an agent
// repeating itself.
//
// The two are the same action seen from here -- read the same path again --
// which is exactly why OR-189 and OR-191 both died: each backgrounded
// ./scripts/test.sh (nine minutes at the time), re-read its output file while
// it ran, and tripped the identical-repeat breaker with the finished work
// still uncommitted. Both agents diagnosed themselves correctly and both were
// still lost, because polling was the only way to wait that existed.
//
// Three signals, each of which distinguishes a wait from a loop:
//
//  1. Asking a background task for its output. That tool has no other use.
//  2. Reading a file this session was told a background command would write.
//     Empty is the normal state of such a file for most of the wait, so
//     "unchanged" proves nothing about progress here.
//  3. A read that returned something DIFFERENT from the previous identical
//     read. Same input, new output, is not a no-progress repeat by any
//     definition -- the file is being written to while it is read.
//
// Everything else still counts. Re-reading a static file that nothing is
// writing is the loop this breaker was built for, and OR-189's own note
// concedes the point; the fix is to make waiting possible, not to weaken the
// trip.
func isPoll(s *state.Session, in Input) bool {
	switch in.ToolName {
	case "BashOutput", "TaskOutput":
		return true
	case "Read", "NotebookRead":
	default:
		return false
	}
	if p := in.FilePath(); p != "" && (s.Awaiting[p] || s.Awaiting[filepath.Base(p)]) {
		return true
	}
	prev := s.LastPoll[actorKey(in)][in.Signature()]
	return prev != "" && prev != fingerprint(in.ToolResponse)
}

// awaitedCap bounds both poll maps. They are memory of convenience, not a
// ledger: a session that has launched sixty-four background commands has
// bigger problems than an exemption it did not get.
const awaitedCap = 64

// rememberAwaited records the files a backgrounded command will write to, so
// a later read of one of them is recognised as a wait.
//
// Two sources, because the path can come from either side. The agent names it
// when it redirects (`./scripts/test.sh > /tmp/out.log &`), and the harness
// names it in the response when it chooses the output file itself -- which is
// the case both lost runs actually hit.
//
// The base name is stored alongside the path: a command that redirects to a
// relative path and a read that resolves it to an absolute one are the same
// file, and refusing to see that would exempt nothing.
func rememberAwaited(s *state.Session, in Input) {
	if in.ToolName != "Bash" || !in.Background() {
		return
	}
	for _, p := range awaitedPaths(in) {
		if s.Awaiting == nil {
			s.Awaiting = map[string]bool{}
		}
		if len(s.Awaiting) >= awaitedCap {
			return
		}
		s.Awaiting[p] = true
		s.Awaiting[filepath.Base(p)] = true
	}
}

// rememberPoll stores what this read returned, so the next identical one can
// tell "the file grew" from "nothing has changed".
func rememberPoll(s *state.Session, actor, sig string, in Input) {
	if in.ToolName != "Read" && in.ToolName != "NotebookRead" {
		return
	}
	if s.LastPoll == nil {
		s.LastPoll = map[string]map[string]string{}
	}
	if s.LastPoll[actor] == nil {
		s.LastPoll[actor] = map[string]string{}
	}
	if _, seen := s.LastPoll[actor][sig]; !seen && len(s.LastPoll[actor]) >= awaitedCap {
		return
	}
	s.LastPoll[actor][sig] = fingerprint(in.ToolResponse)
}

// awaitedPaths pulls the output file out of a backgrounded Bash call: the
// redirect targets in the command, plus any absolute path the harness reports
// back. Heuristic on purpose -- a wrong hit only exempts a poll of that one
// path from the repeat counter, while a miss costs a finished ticket.
func awaitedPaths(in Input) []string {
	var out []string
	fields := strings.Fields(in.Command())
	for i, f := range fields {
		switch {
		case f == ">" || f == ">>" || f == "1>" || f == "2>" || f == "&>" || f == "tee":
			if i+1 < len(fields) {
				out = appendPath(out, fields[i+1])
			}
		case strings.HasPrefix(f, ">") || strings.HasPrefix(f, ">>"):
			out = appendPath(out, strings.TrimLeft(f, ">"))
		}
	}
	for _, f := range strings.Fields(string(in.ToolResponse)) {
		if p := unquotePath(f); strings.HasPrefix(p, "/") && len(p) > 1 {
			out = append(out, p)
		}
	}
	return out
}

func appendPath(out []string, raw string) []string {
	if p := unquotePath(raw); p != "" {
		return append(out, p)
	}
	return out
}

// unquotePath strips the quoting and JSON punctuation a path arrives wrapped
// in, so `"/tmp/out.log\n",` reads as /tmp/out.log.
func unquotePath(f string) string {
	f = strings.TrimLeft(f, "\"'`")
	// A path lifted out of a JSON response arrives welded to what followed
	// it -- `/tmp/ab12.output","is_error":false}` -- so the end of the path
	// is the first quote, comma or escape, not the end of the token.
	if i := strings.IndexAny(f, "\"'`,;\\ \t"); i >= 0 {
		f = f[:i]
	}
	// Trailing punctuation only: a leading dot is `./out.log`, which is a
	// path, not punctuation.
	return strings.TrimRight(f, "()[]{}:")
}

func fingerprint(b []byte) string { return fmt.Sprintf("%016x", fnv1a(string(b))) }

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
// isBlockedNoteWrite recognizes the stop-note the breaker's own protocol
// demands. The Block message says "write BLOCKED.md"; refusing that write is
// the deadlock this exists to prevent. Scoped to exactly that file under the
// configured plans directory -- an agent cannot use it to keep editing code.
func isBlockedNoteWrite(in Input, cfg config.Config) bool {
	if !isEditTool(in.ToolName) {
		return false
	}
	p := in.FilePath()
	return p != "" && strings.HasSuffix(p, "BLOCKED.md") &&
		strings.Contains(p, cfg.Paths.Plans)
}

// isCleanupCommand recognises the only commands a tripped session may still
// run: the ones that leave the worktree in a reportable state.
//
// This is deliberately NOT "Bash, for cleanup". Widening the breaker that
// way is the same as not tripping, because nothing distinguishes a cleanup
// edit from another attempt at the task except intent, and intent is not
// observable (OR-194). What IS observable is the command itself: `git
// checkout -- x` can only revert, `git commit` cannot change a file's
// contents, `git status` cannot change anything. So the allowance is a list
// of forms, and everything outside it stays refused.
//
// The metacharacter check is load-bearing, not defensive tidiness. Without
// it `git status; <anything>` passes a prefix test and the whole allowance
// becomes a general reprieve with an extra step.
//
// So is the rewrite check. A prefix match answers "which command is this",
// not "what can it do", and `git commit` and `git commit --amend` are the
// same command doing opposite things: one ADDS a commit, the other replaces
// the tip. The allowance promises that what the run already committed
// survives -- it is the only durable record a tripped run leaves -- and
// --amend is exactly the flag that breaks that promise.
func isCleanupCommand(cmd string) bool {
	c := normalizeCmd(cmd)
	if c == "" || strings.ContainsAny(c, ";&|`<>\n") || strings.Contains(c, "$(") {
		return false
	}
	if rewritesHistory(c) {
		return false
	}
	for _, form := range []string{
		"git status", "git diff",
		"git checkout -- ", "git restore ",
		"git add ", "git commit",
	} {
		if strings.HasPrefix(c, form) {
			return true
		}
	}
	return false
}

// rewritesHistory reports whether a command would replace a commit rather
// than add one.
//
// Compared field by field rather than by substring, so `git commit -m
// "reverted the --amend attempt"` is not refused for quoting the flag in its
// message. A bare, unquoted --amend anywhere in the command is an amend
// whatever else is on the line, which is what this has to catch.
func rewritesHistory(cmd string) bool {
	for _, f := range strings.Fields(cmd) {
		if f == "--amend" {
			return true
		}
	}
	return false
}

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
