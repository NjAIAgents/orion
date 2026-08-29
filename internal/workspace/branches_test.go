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

// A command run from inside a worktree knows its branch and nothing else, so
// it has to get back to the ticket the other way (OR-183). The suffixed
// branch is the case that matters: no convention recovers FCIA-6 from
// orion/fcia-6-2, which is why the record is read rather than re-derived.
func TestKeyOfBranchFindsTheTicketFromASuffixedBranch(t *testing.T) {
	ws := &Workspace{ID: "t", Dir: t.TempDir()}
	if err := RecordBranch(ws, "FCIA-6", "orion/fcia-6-2"); err != nil {
		t.Fatal(err)
	}
	key, ok := KeyOfBranch(ws, "orion/fcia-6-2")
	if !ok || key != "FCIA-6" {
		t.Fatalf("KeyOfBranch = (%q, %v), want (\"FCIA-6\", true)", key, ok)
	}
	// An unrecorded branch must say so rather than return some other
	// ticket's key: attributing spend to the wrong ticket is worse than
	// attributing it to none.
	if key, ok := KeyOfBranch(ws, "orion/fcia-7"); ok || key != "" {
		t.Fatalf("KeyOfBranch on an unrecorded branch = (%q, %v), want (\"\", false)", key, ok)
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
