package budget

import (
	"sync"
	"testing"
	"time"
)

// The test that fails on the unlocked version.
//
// Two watchers in two terminals, one per project repository, record runs at
// the same moment. Load-modify-save without a lock loses updates: both read
// the same ledger, both append their own run, and the second rename discards
// the first one's spend. Under-counted spend means the weekly checkpoint
// fires later than it should, and nothing reports that a run went missing
// (OR-138).
func TestConcurrentUpdatesDoNotLoseRuns(t *testing.T) {
	home := t.TempDir()
	const writers = 12

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			err := Update(home, func(l *Ledger) {
				l.Record(Run{At: time.Now().UTC(), CostUSD: 1, InputTokens: 100})
			})
			if err != nil {
				t.Errorf("writer %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	l, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Runs) != writers {
		t.Fatalf("recorded %d runs, want %d: concurrent updates were lost", len(l.Runs), writers)
	}

	st := l.Status(Limits{WeeklyUSD: 1000})
	if st.SpentUSD != float64(writers) {
		t.Errorf("spend = %v, want %d", st.SpentUSD, writers)
	}
}

// Update must see what a concurrent writer already committed, not a snapshot
// taken before it. This is the read half of the same race.
func TestUpdateSeesEarlierWrites(t *testing.T) {
	home := t.TempDir()
	if err := Update(home, func(l *Ledger) {
		l.Record(Run{At: time.Now().UTC(), CostUSD: 5})
	}); err != nil {
		t.Fatal(err)
	}
	var seen int
	if err := Update(home, func(l *Ledger) {
		seen = len(l.Runs)
		l.Record(Run{At: time.Now().UTC(), CostUSD: 5})
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("second Update saw %d runs, want 1", seen)
	}
	l, _ := Load(home)
	if len(l.Runs) != 2 {
		t.Fatalf("ledger has %d runs, want 2", len(l.Runs))
	}
}

// Acknowledging a checkpoint must not discard runs a watcher recorded while
// the operator was reading the status.
func TestAckDoesNotDiscardConcurrentRuns(t *testing.T) {
	home := t.TempDir()
	if err := Update(home, func(l *Ledger) {
		l.Record(Run{At: time.Now().UTC(), CostUSD: 90})
	}); err != nil {
		t.Fatal(err)
	}
	// A watcher records another run here, after any status read.
	if err := Update(home, func(l *Ledger) {
		l.Record(Run{At: time.Now().UTC(), CostUSD: 5})
	}); err != nil {
		t.Fatal(err)
	}
	if err := Update(home, func(l *Ledger) { l.AckAll(50) }); err != nil {
		t.Fatal(err)
	}

	l, _ := Load(home)
	if len(l.Runs) != 2 {
		t.Fatalf("ack dropped a run: %d runs, want 2", len(l.Runs))
	}
	if len(l.Acked) == 0 {
		t.Error("the acknowledgement was not persisted")
	}
}
