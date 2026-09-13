package collect

import (
	"testing"
)

// OR-458: Trips had no reader at all before this, unlike FixRounds. A
// workspace with no breaker-trips.json must read as unknown, not zero -- the
// same contract FixRounds already keeps, and for the same reason: a
// cleaned-up workspace says nothing about how many times a ticket tripped
// the breaker, and reading silence as "never" is how an already-evicted
// ticket gets re-admitted with no memory of it.
func TestTripsIsUnknownWithoutARecord(t *testing.T) {
	wsDir := t.TempDir()
	if n, known := Trips(wsDir, "OR-1"); known || n != 0 {
		t.Errorf("Trips on a workspace with no record = (%d, %v), want (0, false)", n, known)
	}
}

func TestRecordTripAccumulatesPerTicket(t *testing.T) {
	wsDir := t.TempDir()
	if err := RecordTrip(wsDir, "OR-1", "unverified-edits", "no verify after 25 edits"); err != nil {
		t.Fatal(err)
	}
	if err := RecordTrip(wsDir, "OR-1", "no-progress", "40 identical tool calls"); err != nil {
		t.Fatal(err)
	}
	if err := RecordTrip(wsDir, "OR-2", "unverified-edits", "no verify after 25 edits"); err != nil {
		t.Fatal(err)
	}

	n, known := Trips(wsDir, "OR-1")
	if !known || n != 2 {
		t.Errorf("Trips(OR-1) = (%d, %v), want (2, true)", n, known)
	}
	n, known = Trips(wsDir, "OR-2")
	if !known || n != 1 {
		t.Errorf("Trips(OR-2) = (%d, %v), want (1, true)", n, known)
	}
	// The file exists once any ticket has a record; a ticket never mentioned
	// in it is a reading of zero, not an unknown -- same contract as
	// FixRounds for a ticket absent from an existing ci-fixes.json.
	if n, known := Trips(wsDir, "OR-3"); !known || n != 0 {
		t.Errorf("Trips(OR-3) = (%d, %v), want (0, true)", n, known)
	}
}

func TestClearTripsForgetsATicketsHistory(t *testing.T) {
	wsDir := t.TempDir()
	if err := RecordTrip(wsDir, "OR-1", "unverified-edits", "detail"); err != nil {
		t.Fatal(err)
	}
	if err := clearTrips(wsDir, "OR-1"); err != nil {
		t.Fatal(err)
	}
	if n, known := Trips(wsDir, "OR-1"); !known || n != 0 {
		t.Errorf("Trips(OR-1) after clearTrips = (%d, %v), want (0, true)", n, known)
	}
}
