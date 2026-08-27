package tracker

import "testing"

// A finished sub-task is context, not work. Passing it to an agent invites
// it to redo something a person completed by hand -- which it cannot tell
// from the text, because a Task's description says what to do, not whether
// it was done.
func TestDoneChildrenAreNotWork(t *testing.T) {
	kids := []Issue{
		{Key: "OR-51", Status: "To Do"},
		{Key: "OR-52", Status: "Done"},
		{Key: "OR-53", Status: "In Progress"},
		{Key: "OR-54", Status: "Closed"},
		{Key: "OR-55", Status: "Cancelled"},
	}
	got := Workable(kids)
	if len(got) != 2 {
		t.Fatalf("workable = %d, want 2 (To Do + In Progress); got %+v", len(got), got)
	}
	for _, g := range got {
		if g.Key == "OR-52" || g.Key == "OR-54" || g.Key == "OR-55" {
			t.Errorf("%s is finished and was handed to the agent anyway", g.Key)
		}
	}
}

// Status vocabulary varies by workflow, and treating an unknown status as
// Done would silently drop real work. Unknown means workable.
func TestAnUnfamiliarStatusIsTreatedAsWork(t *testing.T) {
	got := Workable([]Issue{{Key: "OR-56", Status: "Awaiting Review"}})
	if len(got) != 1 {
		t.Error("an unrecognised status was assumed finished; that silently drops work")
	}
}
