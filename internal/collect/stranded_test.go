package collect

import "testing"

// OR-458: Stranded had no reader at all before this. Same unknown-vs-zero
// contract as FixRounds and Trips.
func TestStrandedIsUnknownWithoutARecord(t *testing.T) {
	wsDir := t.TempDir()
	if n, known := Stranded(wsDir, "OR-1"); known || n != 0 {
		t.Errorf("Stranded on a workspace with no record = (%d, %v), want (0, false)", n, known)
	}
}

// A COUNT OF CONSECUTIVE passes, distinct from Trips's lifetime total: two
// strandings with a clean settle between them must read as one, not two.
func TestRecordStrandedTracksAConsecutiveStreak(t *testing.T) {
	wsDir := t.TempDir()
	if err := RecordStranded(wsDir, "OR-1", "could not commit 2 files"); err != nil {
		t.Fatal(err)
	}
	if err := RecordStranded(wsDir, "OR-1", "could not commit 2 files"); err != nil {
		t.Fatal(err)
	}
	if n, known := Stranded(wsDir, "OR-1"); !known || n != 2 {
		t.Errorf("Stranded(OR-1) after two strandings = (%d, %v), want (2, true)", n, known)
	}

	if err := ClearStranded(wsDir, "OR-1"); err != nil {
		t.Fatal(err)
	}
	if n, known := Stranded(wsDir, "OR-1"); !known || n != 0 {
		t.Errorf("Stranded(OR-1) after a clean settle = (%d, %v), want (0, true)", n, known)
	}

	// A fresh stranding after the reset starts the streak over, not from
	// where it left off.
	if err := RecordStranded(wsDir, "OR-1", "could not commit 1 file"); err != nil {
		t.Fatal(err)
	}
	if n, known := Stranded(wsDir, "OR-1"); !known || n != 1 {
		t.Errorf("Stranded(OR-1) after one post-reset stranding = (%d, %v), want (1, true)", n, known)
	}
}

func TestClearStrandedOnATicketNeverStrandedIsANoop(t *testing.T) {
	wsDir := t.TempDir()
	if err := ClearStranded(wsDir, "OR-9"); err != nil {
		t.Fatal(err)
	}
	if n, known := Stranded(wsDir, "OR-9"); known || n != 0 {
		t.Errorf("Stranded(OR-9) = (%d, %v), want (0, false); ClearStranded must not create a record", n, known)
	}
}
