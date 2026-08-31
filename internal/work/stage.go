package work

// The tracker-side rendering of a boundary internal/ui already computes.
//
// OR-189 gave every stage crossing one call that both prints the line and
// records the event, so the two can never disagree about who now holds the
// run. The board could not see any of it: a claimed ticket said
// orion-working and nothing more, so implementation, QA and a fix round were
// indistinguishable from Jira (OR-225).
//
// So the same call moves the stage label too. Here rather than inside
// ui.Stage because internal/ui renders and must not reach a tracker, and
// here rather than at each call site because a label set at four boundaries
// out of five is a label a reader learns not to trust.
//
// Deliberately NOT applied to internal/collect's boundaries. Those cross
// while the ticket wears orion-ci-wait, which already says everything there
// is to say about that state -- a pull request is open and no agent is
// running -- and a stage label on a ticket holding no claim would be a
// stage for work nobody is doing.

import (
	"io"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// handoff prints one stage boundary and moves the ticket's stage label with
// it.
//
// BEST EFFORT, and a separate request from any lock write. The stage label
// is decoration: a tracker that refuses it leaves a less informative Jira
// view, which must never be allowed to cost a run or -- far worse -- to
// fail a claim it was bundled into. The failure is said once, not returned.
//
// Nothing is written when the incoming side has no stage label. That is the
// handoff to ci at "pull request opened": the claim was released in the same
// breath, and releasing a claim already clears every stage label.
func handoff(w io.Writer, log *events.Log, deps Deps, opts Options, h ui.Handoff) {
	ui.Stage(w, log, h)

	add := actors.StageLabel(h.Next)
	if opts.DryRun || deps.Jira == nil || add == "" {
		return
	}
	// One party holding both sides -- Orion pushing a branch and then opening
	// the pull request for it -- is not a change of stage, so it is not a
	// write.
	remove := actors.StageLabel(h.By)
	if add == remove {
		return
	}
	if err := setStage(deps, h.Key, add, remove); err != nil {
		ui.Say(w, h.Key, events.ActorOrion, ui.VerbWarn,
			"the board will not say which actor holds it: %v", err)
	}
}

// setStage swaps one stage label for another in a single request, so the
// ticket never briefly carries two.
func setStage(deps Deps, key, add, remove string) error {
	var rm []string
	if remove != "" {
		rm = []string{remove}
	}
	return deps.Jira.SetLabels(key, []string{add}, rm)
}
