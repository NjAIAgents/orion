package collect

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// TestABatchClosesTheTicketsItLanded is OR-314.
//
// The batch merged the code and told only the screen. A landed ticket stayed
// In Progress carrying orion-ready, and that label is not cosmetic: the next
// collect pass re-read it as ready, assembled it into a new batch, and
// ejected it for having nothing left to contribute. Observed on three
// consecutive sessions and closed by hand each time.
func TestABatchClosesTheTicketsItLanded(t *testing.T) {
	j := newTracker()
	var buf bytes.Buffer

	closeLanded("OR-1", "orion/batch", "abc1234", config.Config{}, Deps{Jira: j}, &buf)

	if got := j.transitions["OR-1"]; got != "Done" {
		t.Errorf("transitioned to %q, want Done", got)
	}
	// EVERY managed label, not just the one that brought it here. A ticket
	// that failed earlier and then landed would otherwise keep orion-failed
	// forever, and the queue would report "failed" beside a status of Done.
	removed := strings.Join(j.removed["OR-1"], " ")
	for _, want := range tracker.Managed("ORION") {
		if !strings.Contains(removed, want) {
			t.Errorf("label %q was not cleared; removed %v", want, j.removed["OR-1"])
		}
	}
	// The comment names the ref AND the SHA: the ref name is reused by every
	// batch, so alone it says nothing about which one this was.
	c := strings.Join(j.comments["OR-1"], " ")
	if !strings.Contains(c, "orion/batch") || !strings.Contains(c, "abc1234") {
		t.Errorf("comment does not identify the merge: %q", c)
	}
}

// TestAFailedLabelClearDoesNotLoseTheMerge. The code is on the work branch
// either way, so a tracker that will not co-operate must not turn a
// successful merge into a reported failure -- and must not stop the steps
// after it, since a ticket that is Done with a stale label is less confusing
// than one left In Progress with the same stale label.
func TestAFailedLabelClearDoesNotLoseTheMerge(t *testing.T) {
	j := newTracker()
	j.labelErr = errTracker
	var buf bytes.Buffer

	closeLanded("OR-1", "orion/batch", "abc1234", config.Config{}, Deps{Jira: j}, &buf)

	if !strings.Contains(buf.String(), "labels could not be cleared") {
		t.Errorf("the failure was not reported:\n%s", buf.String())
	}
	// It carried on: the status still moved and the merge is still recorded.
	if got := j.transitions["OR-1"]; got != "Done" {
		t.Errorf("stopped at the first failure; transitioned to %q", got)
	}
	if len(j.comments["OR-1"]) == 0 {
		t.Error("the merge was not recorded on the ticket")
	}
}

// TestNoTrackerIsNotACrash. This runs on the batch path for every landed
// member; a nil client must not take the merge down with it.
func TestNoTrackerIsNotACrash(t *testing.T) {
	var buf bytes.Buffer
	closeLanded("OR-1", "orion/batch", "abc", config.Config{}, Deps{}, &buf)
}
