package queue

import (
	"testing"
	"time"
)

// The ledger is cumulative across runs, not a snapshot of the latest one: a
// ticket recorded on an earlier `orion collect` pass has to still be there
// after a later pass records a different ticket, or the file could never
// answer "how many tickets ever carried a scope".
func TestTheLedgerKeepsTicketsFromEarlierRuns(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// A prior run records OR-1 and saves.
	prior := LoadScopes(dir)
	prior.Record(Prediction{Key: "OR-1", Declared: []string{"internal/a"},
		Actual: []string{"internal/a/a.go"}}, now)
	if err := SaveScopes(dir, prior); err != nil {
		t.Fatal(err)
	}

	// This run loads fresh, records a second ticket, and saves again.
	this := LoadScopes(dir)
	this.Record(Prediction{Key: "OR-2", Declared: []string{"internal/b"},
		Actual: []string{"internal/b/b.go"}}, now)
	if err := SaveScopes(dir, this); err != nil {
		t.Fatal(err)
	}

	back := LoadScopes(dir)
	if len(back.Predictions) != 2 {
		t.Fatalf("read back %d predictions, want 2: this run must not have dropped the prior one",
			len(back.Predictions))
	}
	keys := map[string]bool{}
	for _, p := range back.Predictions {
		keys[p.Key] = true
	}
	if !keys["OR-1"] || !keys["OR-2"] {
		t.Errorf("predictions = %+v, want both OR-1 (prior run) and OR-2 (this run)", back.Predictions)
	}
}
