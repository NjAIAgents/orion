package collect

import (
	"slices"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// OR-345. A ticket that fails must leave BOTH queue states behind, not one.
//
// The bug this pins: failing() removed only orion-ci-wait, which is the state
// on the per-branch path. A batch culprit is in orion-ready -- the
// integration queue's inbox -- so the removal missed and the ticket ended up
// carrying orion-failed AND orion-ready at once. The pass query matches on
// ready, so the next tick assembled the branch that had just been convicted,
// into a batch that could only fail again.
//
// It looked correct from outside for as long as it was broken: `orion queue`
// checks orion-failed first, so the ticket displayed as failed while the
// batch kept eating its branch. Proven on OR-304, whose labels after
// conviction were ["bug" "ci" "orion-failed" "orion-ready"].
func TestFailingClearsEveryPreFailureState(t *testing.T) {
	want := tracker.PreFailure()
	if !slices.Contains(want, tracker.LabelCIWait) {
		t.Errorf("PreFailure() omits %q, the per-branch path's state: %v",
			tracker.LabelCIWait, want)
	}
	if !slices.Contains(want, tracker.LabelReady) {
		t.Errorf("PreFailure() omits %q, which is the state a batch culprit is "+
			"in -- leaving it set is what re-assembled a convicted branch (OR-345): %v",
			tracker.LabelReady, want)
	}
}

// Every label Orion can be holding when it fails must be in PreFailure().
//
// Managed() is the full set Orion owns. Subtract the ones a failure cannot be
// arriving from -- the queue label (not yet claimed), working (the agent is
// still running, failure comes later), and failed itself -- and what remains
// must all be cleared. Written as a derivation rather than a second literal
// list so that adding a state to Managed() and forgetting it here fails
// loudly instead of silently, which is exactly how OR-345 shipped.
func TestPreFailureCoversEveryStateAFailureCanArriveFrom(t *testing.T) {
	const queue = tracker.QueueLabelDefault
	exempt := []string{queue, tracker.LabelWorking, tracker.LabelFailed}

	cleared := tracker.PreFailure()
	for _, l := range tracker.Managed(queue) {
		if slices.Contains(exempt, l) {
			continue
		}
		if !slices.Contains(cleared, l) {
			t.Errorf("%q is a state Orion can hold when a ticket fails, but "+
				"PreFailure() does not clear it -- a ticket would carry it "+
				"alongside orion-failed and be re-queued by it (OR-345)", l)
		}
	}
}
