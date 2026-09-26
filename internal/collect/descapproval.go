// Author: Claude Sonnet 5
// Created: 2026-09-10
// Last updated: 2026-09-10
// Description: A Jira-native approval gate for description rewrites (OR-431).

package collect

import (
	"fmt"
	"io"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// DescTrackerAPI is the tracker surface this gate needs. Named rather than
// reusing a wider interface so a fake only has to implement what a
// description approval actually touches.
type DescTrackerAPI interface {
	GetIssue(key string) (*tracker.Issue, error)
	SetLabels(key string, add, remove []string) error
	Comment(key, text string) error
	SetDescription(key, text string) (was string, err error)
}

// RequestDescriptionChange posts a before/after comment and marks the ticket
// pending, for a human to approve or reject from Jira itself.
//
// NEVER APPLIES THE CHANGE. This only asks. SetDescription is not called
// here on purpose -- the write happens in ResolveDescriptionChange, and only
// after a human has labelled the ticket approved. Composing the request and
// applying it in the same function is exactly the shape that turns into "it
// asked and then did it anyway" the first time somebody refactors the
// caller.
//
// actor is who drafted the text, so the comment reads as the agent's
// proposal rather than an anonymous system message -- the same attribution
// every other agent-authored comment already carries.
func RequestDescriptionChange(deps DescTrackerAPI, key, actor, proposed string, w io.Writer) error {
	issue, err := deps.GetIssue(key)
	if err != nil {
		return fmt.Errorf("reading %s before proposing a description change: %w", key, err)
	}
	body := descApprovalComment(issue.Description, proposed)
	if err := deps.Comment(key, actors.Comment(actor, body)); err != nil {
		return fmt.Errorf("posting the before/after on %s: %w", key, err)
	}
	if err := deps.SetLabels(key, []string{tracker.LabelDescPending}, nil); err != nil {
		return fmt.Errorf("marking %s pending: %w", key, err)
	}
	ui.Say(w, key, events.ActorOrion, ui.VerbOK,
		"posted a description change for review; add %s or %s to decide it",
		tracker.LabelDescApproved, tracker.LabelDescRejected)
	return nil
}

// descApprovalComment is the before/after a reviewer compares. Both in full,
// not a diff -- a diff algorithm's idea of what changed is one more thing
// that could disagree with what actually gets written, and the reviewer is
// deciding whether to trust NEW text, not how it was derived from the old.
func descApprovalComment(before, after string) string {
	return "Proposed description change -- review and label " +
		tracker.LabelDescApproved + " or " + tracker.LabelDescRejected + ".\n\n" +
		"BEFORE:\n" + before + "\n\n" +
		"AFTER:\n" + after
}

// PendingDescriptionChange is a description-rewrite request awaiting a
// decision, read back from the ticket so a resolve needs nothing the
// original request did not already leave on the tracker.
type PendingDescriptionChange struct {
	Key      string
	Proposed string
}

// ResolveDescriptionChange applies or discards a pending request, based on
// which label a human added.
//
//   - orion-desc-approved: the write happens NOW, for the first time in this
//     whole flow. SetDescription's own before-read is the last line of
//     defence against a description that changed again between the request
//     and the approval.
//   - orion-desc-rejected: nothing is written. The pending label is cleared
//     either way, so a ticket can be proposed again rather than staying
//     wedged on one answer forever.
//   - neither: still waiting. Read again next tick, the same as every other
//     gate Orion polls rather than pushes.
func ResolveDescriptionChange(deps DescTrackerAPI, p PendingDescriptionChange, w io.Writer) error {
	issue, err := deps.GetIssue(p.Key)
	if err != nil {
		return fmt.Errorf("reading %s to resolve its description change: %w", p.Key, err)
	}
	approved := labelSet(issue.Labels, tracker.LabelDescApproved)
	rejected := labelSet(issue.Labels, tracker.LabelDescRejected)

	switch {
	case approved:
		was, err := deps.SetDescription(p.Key, p.Proposed)
		if err != nil {
			return fmt.Errorf("applying the approved description on %s: %w", p.Key, err)
		}
		if err := deps.SetLabels(p.Key, nil,
			[]string{tracker.LabelDescPending, tracker.LabelDescApproved}); err != nil {
			ui.Say(w, p.Key, events.ActorOrion, ui.VerbWarn,
				"description applied but the pending label could not be cleared: %v", err)
		}
		ui.Say(w, p.Key, events.ActorOrion, ui.VerbOK,
			"description approved and applied; replaced %d character(s)", len(was))
		return nil

	case rejected:
		if err := deps.SetLabels(p.Key, nil,
			[]string{tracker.LabelDescPending, tracker.LabelDescRejected}); err != nil {
			ui.Say(w, p.Key, events.ActorOrion, ui.VerbWarn,
				"rejection noted but the pending label could not be cleared: %v", err)
		}
		ui.Say(w, p.Key, events.ActorOrion, ui.VerbOK,
			"description change rejected; nothing was written")
		return nil

	default:
		// Neither label yet. Not an error, not a warning -- a person has not
		// looked at it, which is the ordinary state of anything waiting on a
		// human.
		return nil
	}
}

// labelSet reports whether want is among labels. Named apart from
// batchrun_test.go's hasLabel: that one lives in a _test.go file, so
// non-test code in this package cannot call it.
func labelSet(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
