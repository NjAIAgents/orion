package collect

import (
	"fmt"
	"io"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The CI fix loop.
//
//	CI fails -> fetch the failing job's log -> agent fixes on the SAME branch
//	         -> push -> CI runs again -> repeat, bounded
//
// The agent that wrote the branch is the right one to fix it: the failure is
// specific, the code is its own, and the alternative is a person copying a CI
// log into a chat window by hand -- which is the manual loop this whole
// project exists to remove.
//
// Everything below is about knowing when to STOP. An unbounded version is a
// machine that spends money all night oscillating between two broken states,
// and it does not crash, so nobody finds out until the bill.
func tryFix(res Result, key string, pr PR, cfg config.Config, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) (bool, Result) {

	fp := Fingerprint(pr.Detail)
	state := loadFixes(ws.Dir).States[key]
	max := cfg.CI.MaxFixAttempts
	if max <= 0 {
		max = 3
	}

	// Brake one: the same failure twice means the last attempt achieved
	// nothing. Spending the remaining attempts would prove only that it can
	// fail identically three times.
	if state.Repeating(fp) {
		giveUp(key, ws, log, w, fmt.Sprintf(
			"the last fix produced an identical failure, so it is not making progress (attempt %d)",
			state.Count()))
		return false, res
	}

	// Brake two: the ceiling, for a loop that changes something every round
	// and still never converges.
	if state.Count() >= max {
		giveUp(key, ws, log, w, fmt.Sprintf(
			"%d fix attempts were spent without a green build", state.Count()))
		return false, res
	}

	if opts.DryRun {
		ui.Ok(w, "would", "%s: attempt %d of %d to fix CI", key, state.Count()+1, max)
		return true, res
	}

	// Counted BEFORE the run. A crash mid-fix must not refund the attempt --
	// a ceiling that resets whenever the process dies is no ceiling at all,
	// and process death is exactly what a runaway loop tends to produce.
	state, err := recordAttempt(ws.Dir, key, branch, fp, pr.Detail)
	if err != nil {
		ui.Warn(w, "could not record the fix attempt: %v", err)
		return false, res
	}

	attempt := state.Count()
	ui.Ok(w, "working", "%s: CI failed; attempt %d of %d to fix it", key, attempt, max)
	log.Emit(events.Event{Kind: events.KindCI, Actor: events.ActorOrion,
		Msg: fmt.Sprintf("fix attempt %d of %d: %s", attempt, max, firstLine(pr.Detail))})

	tell(w, log, notify.Event{
		Channel: channelOf(ws), Level: notify.Warning, Workspace: ws.ID,
		Title: fmt.Sprintf("%s: fixing a CI failure (attempt %d of %d)", key, attempt, max),
		Body: fmt.Sprintf("*The build went red and Orion is trying to fix it.*\n\n"+
			"*What failed*\n%s\n\n• pull request  %s\n\n"+
			"_Nothing is required of you. If it cannot fix it in %d attempts, "+
			"it stops and says so._", quote(pr.Detail), link(pr.URL, "open it"), max),
	})

	pushed, err := deps.Fix(ws, key, branch, pr.Detail)
	if err != nil {
		giveUp(key, ws, log, w, "the fix run failed: "+err.Error())
		res.Err = err
		return false, res
	}
	if !pushed {
		// Exit 0 with nothing pushed means the agent could not see what to
		// change. Another identical attempt would produce the same nothing.
		giveUp(key, ws, log, w,
			"the agent produced no change, so it does not know how to fix this")
		return false, res
	}

	log.Emit(events.Event{Kind: events.KindPush, Actor: events.ActorImplementer,
		Msg: fmt.Sprintf("pushed a fix for CI (attempt %d)", attempt)})
	ui.Ok(w, "ok", "%s: pushed a fix; CI will run again", key)
	res.Changed = true
	res.Verdict = VerdictPending // it is building again, not failing
	return true, res
}

// giveUp records why the loop stopped, without marking the ticket.
//
// Deliberately does not relabel: the caller falls through to the normal
// failing path, which owns the tracker changes. Two places writing the same
// labels is how a ticket ends up in two states at once.
//
// And deliberately does NOT clear the attempt history. Clearing here looks
// like tidying up after a finished episode, but `orion collect` is a command
// people re-run -- and on a timer, it re-runs itself. A history wiped at the
// moment of giving up means the very next pass starts from zero attempts and
// tries again, so the ceiling bounds one invocation rather than the problem.
// That is an unbounded spend wearing a bound's clothing.
//
// The history is cleared in exactly one place: a successful merge.
func giveUp(key string, ws *workspace.Workspace, log *events.Log, w io.Writer, why string) {
	ui.Warn(w, "%s: giving up on fixing CI -- %s", key, why)
	log.Emit(events.Event{Kind: events.KindFailed, Actor: events.ActorOrion,
		Msg: "stopped fixing: " + why})
}
