package web

// OR-273: two projects, each with its own queue label, must each see their
// own label in the queued column -- not the other's, and not whichever was
// asked for first. board_test.go's TestTheQueuedColumnCarriesTheProjectsOwnLabel
// only ever asks about one label per test run, so it cannot catch a bug where
// asking about a second project's label after a first one leaks the first
// label forward (a cached slice, a shared backing array).

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

func TestDifferentProjectsEachDisplayTheirOwnQueueLabel(t *testing.T) {
	const labelA = "PROJECT-A-LABEL"
	const labelB = "PROJECT-B-LABEL"

	queuedLabel := func(cols []tracker.QueueState) string {
		for _, c := range cols {
			if c.Name == "queued" {
				return c.Label
			}
		}
		return ""
	}

	if got := queuedLabel(Columns(labelA)); got != labelA {
		t.Errorf("project A's queued column carries %q, want %q", got, labelA)
	}
	if got := queuedLabel(Columns(labelB)); got != labelB {
		t.Errorf("project B's queued column carries %q, want %q", got, labelB)
	}
	// Re-querying A after B must still show A's own label -- proves no state
	// leaked forward between calls.
	if got := queuedLabel(Columns(labelA)); got != labelA {
		t.Errorf("project A's queued column changed to %q after querying B, want %q", got, labelA)
	}
}
