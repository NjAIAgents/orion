package work

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/state"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// What a stopped run leaves behind, and why Orion clears it rather than the
// agent.
//
// The agent now has a bounded cleanup allowance after a trip (see
// internal/hook/breaker.go), but an allowance only helps while the agent is
// still alive to spend it. A run killed on the turn ceiling, or one whose
// last act was the call that tripped, has no such moment. This does not
// depend on one: it runs when the RUN ends, whatever ended it.
//
// It matters because a dirty worktree is not inert. collect's rebaseOnto
// refuses a tree with uncommitted tracked changes, and correctly says so --
// so residue silently disables the automation that would have kept the branch
// current, and the operator finds out on some later collect tick, as three
// commands to run by hand. OR-192 recovered only because a later stage
// happened to exist and happened to read plans/BLOCKED.md; had the run ended
// on the trip, nothing was coming back for it.
//
// The uncommitted tree is what this reads to decide, and the ONLY thing.
// Conditioning it on a tripped breaker was the same predicted failure a
// second time (OR-233): the flag is erased by mechanisms that are each
// individually correct -- an unverified-edits trip self-clears when a verify
// passes, `orion reset` clears it by operator action, and every session
// writes its own state file -- so on OR-217 the flag had gone, the residue
// stayed, the rebase refused it every poll for a quarter of an hour with two
// healthy branches starved behind it, and recovery was an operator running
// git by hand inside a hashed path under ORION_HOME. A trip, where one is
// still on record, decides the commit MESSAGE and what is reported. It does
// not decide whether to act.
//
// COMMITTING it is the ONLY thing tried. The breaker takes a snapshot the
// moment it trips (internal/hook/breaker.go), so after a trip there is
// usually nothing left; this is the backstop for when that could not happen.
// Reverting used to be unconditional, on the reasoning that the residue of a
// trip is unverified -- true, and it cost OR-189 and OR-191 258 and 439 lines
// of finished, green work between them. Unverified work on a branch can be
// read, resumed or dropped by a person. Reverted work cannot be anything.
//
// Keeping the revert as the FALLBACK for a failed commit assumed the commit
// would normally succeed. OR-241 established that CommitAll had never worked
// in this repository, so the fallback fired on every run, and on OR-116 it
// destroyed cmd/orion/releaseship_cli_test.go: the add had failed before
// staging, so no blob was written and the file was unrecoverable.
//
// So when the commit fails, the work is KEPT (OR-242). A dirty worktree is a
// visible, recoverable problem -- it blocks the next rebase, loudly, on the
// next collect tick, with every file still where the agent left it. A revert
// is an invisible, irrecoverable one. The two are not comparable, and no
// amount of tidiness is worth an agent's output.
//
// What the run committed is untouched either way.
func settleTripResidue(jobPath, branch, key, summary, issueURL string, runFailed bool,
	cfg config.Config, ws *workspace.Workspace, comment func(string) error,
	log *events.Log, w io.Writer) {

	if jobPath == "" || ws == nil {
		return
	}
	// A dry run removes its own worktree before returning. Nothing ran, so
	// there is nothing to tidy and nothing to say.
	if _, err := os.Stat(jobPath); err != nil {
		return
	}

	// The trip is read for what it lets this SAY, not for permission to act,
	// so a run with no trip on record carries empty strings through and is
	// settled exactly the same way.
	var kind, detail, snapshot, session string
	if sess, tripped := state.New(stateDirOf(jobPath, cfg)).AnyTripped(); tripped {
		kind, detail, snapshot, session = sess.Tripped, sess.TrippedDetail, sess.TripSnapshot, sess.ID
	}

	dirty, err := workspace.DirtyTracked(jobPath)
	if err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"%s and the worktree could not be read: %v", tripPhrase(kind, detail), err)
		return
	}
	if dirty == "" && snapshot == "" {
		// It left nothing behind. Nothing to report here, and deliberately not
		// even the trip: a run that spent its cleanup allowance, committed and
		// opened its pull request has ENDED, and there is no parked session for
		// an operator to release. Saying "the breaker tripped, run orion reset"
		// on a healthy ending would be the first line they learn to ignore, and
		// the OR-232 lines below are the ones that have to be read.
		return
	}

	// A trip that the breaker already snapshotted still gets said out loud.
	// Both lost runs "looked like ordinary failures until someone opened the
	// worktree", which is the part this exists to stop.
	outcome, verb, unresolved := snapshot, ui.VerbWarn, false
	// What the report names. The tracked set is what decided to act, because
	// it is what blocks the rebase; it is NOT the whole of what is at stake
	// once the commit fails.
	kept, files := dirty, 0
	if dirty != "" {
		files = len(strings.Split(dirty, "\n"))
		n, commitErr := workspace.CommitAll(jobPath, msgSnapshot(kind, detail, cfg),
			filepath.Join(cfg.Paths.Plans, "BLOCKED.md"), cfg.Paths.State)
		switch {
		case commitErr == nil:
			outcome = fmt.Sprintf("committed %d uncommitted file(s) as an unverified snapshot on %s",
				n, branch)
		default:
			// Could not preserve it, so leave it exactly as it is. The
			// blocked rebase is the loud, recoverable half of this; the
			// alternative was destroying the only copy.
			//
			// Report the files the way the failed COMMIT saw them, untracked
			// ones included: OR-116's destroyed file was a new test, and a
			// list of what git already tracks leaves out exactly the ones
			// that exist nowhere else.
			if _, all := workspace.Dirty(jobPath); all != "" {
				kept = all
				files = len(strings.Split(all, "\n"))
			}
			outcome = fmt.Sprintf("could NOT commit %d uncommitted file(s) (%v); KEPT them, uncommitted, in the worktree", files, commitErr)
			verb, unresolved = ui.VerbFail, true
		}
	}

	ui.Say(w, key, events.ActorOrion, verb,
		"%s and this run ends with uncommitted work; %s%s",
		tripPhrase(kind, detail), outcome, resumeWith(session))
	switch {
	case unresolved:
		// Every one of them by name, and the command that clears them: a
		// count is not something an operator can act on.
		for _, line := range strings.Split(kept, "\n") {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
		}
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			"Nothing was reverted; the work is still on disk. Settle it with:"))
		fmt.Fprintf(w, "            orion settle %s\n", key)
	case dirty != "":
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, firstLine(dirty)))
	}
	log.Emitf(events.KindNote, events.ActorOrion,
		"%s, holding uncommitted work; %s", tripPhrase(kind, detail), outcome)

	// On the TICKET, not only in the run output and the operator's channel.
	// A ticket that ends orion-failed holding finished work says so where the
	// person who picks it up next is already looking; OR-189 and OR-191 said
	// it nowhere, so both read as ordinary failures for days.
	if comment != nil {
		_ = comment(commentResidue(kind, detail, branch, outcome, files, runFailed))
	}

	// And where somebody who was not watching will see it. A blocked rebase
	// discovered on the next collect tick is a slow way to learn that a run
	// ended badly enough to need a person.
	title, body := msgResidue(key, summary, kind, detail,
		branch, kept, issueURL, outcome, unresolved)
	tell(w, log, ws, notify.Event{
		Level: notify.Warning, Workspace: ws.ID, Actor: events.ActorOrion,
		Title: title, Body: body,
	})
}

// resumeWith puts the recovery command on the operator's surface, or adds
// nothing when there is no session to name.
//
// A CLAUSE on the line that already exists rather than a line of its own: the
// fault this repairs is not that the operator had too little to read, it is
// that what they read never named the remedy (OR-232).
func resumeWith(session string) string {
	if cmd := state.ResetCommand(session); cmd != "" {
		return "; resume it with: " + cmd
	}
	return ""
}

// tripPhrase names what tripped, or says plainly that nothing did.
//
// Every caller settles the residue either way. This decides only how the
// settling READS, which is the whole remaining job of the breaker flag here.
func tripPhrase(kind, detail string) string {
	if kind == "" {
		return "no breaker trip was on record"
	}
	return fmt.Sprintf("the breaker tripped (%s: %s)", kind, detail)
}

// msgSnapshot is the commit message for work Orion preserves on the run's
// behalf. Deliberately the same shape as the breaker's own snapshot message:
// whoever reads `git log` should not have to know which of the two saved it.
//
// A run with no trip on record gets its own wording rather than a trip's with
// the fields blank. It is the same commit for the same reason -- an
// uncommitted tree makes the next rebase refuse -- but pointing that reader at
// a BLOCKED.md the breaker never wrote would send them looking for a note that
// is not there.
func msgSnapshot(kind, detail string, cfg config.Config) string {
	if kind == "" {
		return "wip: snapshot the work uncommitted when the run ended\n\n" +
			"The run ended holding this in its worktree, with no breaker trip on\n" +
			"record. Committed so the work survives the run and so the next rebase of\n" +
			"this branch is not refused; NOTHING here has been verified. Review before\n" +
			"merging.\n"
	}
	return fmt.Sprintf("wip: snapshot the work uncommitted when %s tripped\n\n"+
		"The breaker tripped: %s. Committed as the run ended so the work survives\n"+
		"it; NOTHING here has been verified -- the session was stopped for not\n"+
		"making progress. Review before merging, and see %s/BLOCKED.md in this\n"+
		"worktree for what it was attempting.\n", kind, detail, cfg.Paths.Plans)
}

// commentResidue is what the ticket gets. In those words: how the run ended,
// how many uncommitted files it was holding, and what became of them.
func commentResidue(kind, detail, branch, outcome string, files int, runFailed bool) string {
	var b strings.Builder
	ending := "this run ended"
	if runFailed {
		ending = "this run ended `orion-failed`"
	}
	if kind != "" {
		ending += " with the breaker tripped"
	}
	fmt.Fprintf(&b, "%s, holding %d uncommitted file(s) in its worktree.\n\n", ending, files)
	if kind != "" {
		fmt.Fprintf(&b, "- what tripped: %s — %s\n", kind, detail)
	} else {
		b.WriteString("- what tripped: nothing on record; the run simply ended holding work\n")
	}
	fmt.Fprintf(&b, "- what became of the work: %s\n", outcome)
	fmt.Fprintf(&b, "- branch: %s\n", branch)
	b.WriteString("\nThe run may have finished its implementation before it stopped; " +
		"read the branch rather than assuming an ordinary failure.")
	if kind != "" {
		b.WriteString(" `plans/BLOCKED.md` on that branch says what it was attempting.")
	}
	b.WriteString("\n")
	return b.String()
}

// stateDirOf resolves where a job's hooks wrote their session state. The
// hooks run with the worktree as their root, so it is that worktree's state
// directory rather than the shared clone's.
func stateDirOf(jobPath string, cfg config.Config) string {
	if filepath.IsAbs(cfg.Paths.State) {
		return cfg.Paths.State
	}
	return filepath.Join(jobPath, cfg.Paths.State)
}
