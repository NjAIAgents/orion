package watch

import "testing"

// An empty queue has nothing to pick from and nothing to report: no reordering
// happened, so there is no basis to name.
func TestPickWithNoQueuedTicketsReturnsEmpty(t *testing.T) {
	got, basis := pick(nil, 3)
	if len(got) != 0 {
		t.Fatalf("pick(nil, 3) = %v, want empty", got)
	}
	if basis != "" {
		t.Errorf("basis = %q, want none: nothing was spread", basis)
	}
}
