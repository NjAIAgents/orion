package workspace

import "testing"

// The whole point: a ticket with no recorded branch yet must say so, not
// return a zero value indistinguishable from "recorded, but empty" (OR-173).
func TestBranchOfUnrecordedReturnsFalse(t *testing.T) {
	ws := &Workspace{ID: "t", Dir: t.TempDir()}
	if branch, ok := BranchOf(ws, "FCIA-6"); ok || branch != "" {
		t.Fatalf("BranchOf on an empty record = (%q, %v), want (\"\", false)", branch, ok)
	}
}

func TestRecordBranchRoundTrips(t *testing.T) {
	ws := &Workspace{ID: "t", Dir: t.TempDir()}
	if err := RecordBranch(ws, "FCIA-6", "orion/fcia-6"); err != nil {
		t.Fatal(err)
	}
	branch, ok := BranchOf(ws, "FCIA-6")
	if !ok || branch != "orion/fcia-6" {
		t.Fatalf("BranchOf = (%q, %v), want (\"orion/fcia-6\", true)", branch, ok)
	}
}

// A retry gets a NEW job and a new, suffixed branch. The record must follow
// the ticket's current attempt, not whichever attempt recorded first.
func TestRecordBranchOverwritesAnEarlierAttempt(t *testing.T) {
	ws := &Workspace{ID: "t", Dir: t.TempDir()}
	if err := RecordBranch(ws, "FCIA-6", "orion/fcia-6"); err != nil {
		t.Fatal(err)
	}
	if err := RecordBranch(ws, "FCIA-6", "orion/fcia-6-2"); err != nil {
		t.Fatal(err)
	}
	branch, ok := BranchOf(ws, "FCIA-6")
	if !ok || branch != "orion/fcia-6-2" {
		t.Fatalf("BranchOf = (%q, %v), want the retry's orion/fcia-6-2", branch, ok)
	}
}
