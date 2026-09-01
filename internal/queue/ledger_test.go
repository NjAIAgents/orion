package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTheLedgerSurvivesARoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	var l Ledger
	l.Record(Eviction{Key: "OR-1", Rule: "rounds", Reason: "spent", Run: "r1"}, now)
	l.Missed("OR-2")
	l.Missed("OR-2")
	if err := SaveLedger(dir, l); err != nil {
		t.Fatal(err)
	}

	back := LoadLedger(dir)
	if back.Count("OR-1") != 1 {
		t.Errorf("OR-1 has %d evictions after a round trip, want 1", back.Count("OR-1"))
	}
	e, ok := back.Last("OR-1")
	if !ok || e.Rule != "rounds" || e.Run != "r1" || !e.At.Equal(now) {
		t.Errorf("the eviction came back as %+v", e)
	}
	if back.Passes["OR-2"] != 2 {
		t.Errorf("neglect count came back as %d, want 2", back.Passes["OR-2"])
	}
}

// A lost ledger means one extra attempt. Refusing to run over an unreadable
// file means the queue stops moving, which is worse.
func TestAnUnreadableLedgerIsEmptyRatherThanFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "queue-ledger.json"),
		[]byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l := LoadLedger(dir); len(l.Evictions) != 0 {
		t.Errorf("a corrupt ledger produced %d evictions", len(l.Evictions))
	}
	if l := LoadLedger(t.TempDir()); len(l.Evictions) != 0 {
		t.Error("a missing ledger produced evictions")
	}
}

// The count is what the escalation rule rests on, so it must not drift with
// how the file happens to be ordered or how many other tickets are in it.
func TestTheCountIsPerTicketAndOrderIndependent(t *testing.T) {
	var l Ledger
	now := time.Now()
	l.Record(Eviction{Key: "OR-2", Reason: "a"}, now)
	l.Record(Eviction{Key: "OR-1", Reason: "b"}, now)
	l.Record(Eviction{Key: "OR-2", Reason: "c"}, now)
	l.Record(Eviction{Key: "OR-2", Reason: "d"}, now)

	if l.Count("OR-2") != 3 || l.Count("OR-1") != 1 || l.Count("OR-9") != 0 {
		t.Errorf("counts are %d/%d/%d, want 3/1/0",
			l.Count("OR-2"), l.Count("OR-1"), l.Count("OR-9"))
	}
	if e, _ := l.Last("OR-2"); e.Reason != "d" {
		t.Errorf("Last returned %q, want the most recent", e.Reason)
	}
}

// Printed output must be stable, or the console's collapsing of repeated
// lines is defeated and a standing warning reads as churn.
func TestStarvedIsSortedSoTheReportDoesNotChurn(t *testing.T) {
	var l Ledger
	for _, k := range []string{"OR-9", "OR-1", "OR-5"} {
		l.Missed(k)
		l.Missed(k)
	}
	got := l.Starved(2)
	want := []string{"OR-1", "OR-5", "OR-9"}
	if len(got) != len(want) {
		t.Fatalf("starved = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("starved = %v, want %v", got, want)
		}
	}
}

// The timestamp is supplied so a test does not have to sleep to produce an
// ordering, but a caller that sets one keeps it.
func TestAnExplicitTimestampIsNotOverwritten(t *testing.T) {
	var l Ledger
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	l.Record(Eviction{Key: "OR-1", At: at}, time.Now())
	if e, _ := l.Last("OR-1"); !e.At.Equal(at) {
		t.Errorf("At = %v, want the caller's %v", e.At, at)
	}
}
