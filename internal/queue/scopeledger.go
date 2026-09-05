package queue

// The scope ledger: what planning PREDICTED a ticket would touch, beside what
// the work actually touched (OR-260).
//
// WHY RECORD IT AT ALL. A declared scope is a guess made before the work is
// done, and the queue is about to make admission decisions on it. A prediction
// that nothing ever checks is indistinguishable from a good one, so the honest
// thing is to write both numbers down from the first day and let them be
// judged later. If planning's estimates turn out to be worthless, that is
// worth knowing BEFORE the queue depends on them -- and this is the only way
// anyone finds out.
//
// A MISS IS DATA, NOT A FAILURE. An agent that finds the real fix in a fourth
// file is doing its job. Nothing reads this to punish a ticket, and nothing
// here feeds back into whether work is admitted; it is a record, and the
// judgement it supports is about PLANNING, not about the run.
//
// A FILE, next to the evictions ledger and for the same reason ADR 0004 gives:
// the whole of the manager's durable memory should be greppable, diffable and
// readable after a crash without a client.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/orion-sdlc/orion/internal/fanout"
)

// Prediction is one ticket's declared scope set beside what it changed.
type Prediction struct {
	Key string `json:"key"`
	// Declared is what the ticket's description said, as read. Empty means
	// the ticket declared nothing -- which is a fact worth recording too,
	// because "how many tickets even carry a scope" is the first question
	// anyone judging this will ask.
	Declared []string `json:"declared"`
	// Actual are the files the branch changed against its base.
	Actual []string  `json:"actual"`
	At     time.Time `json:"at"`
}

// Scopes is the durable history, newest last.
type Scopes struct {
	Predictions []Prediction `json:"predictions"`
}

func scopesPath(dir string) string { return filepath.Join(dir, "queue-scopes.json") }

// LoadScopes reads the ledger, treating absence and corruption alike as an
// empty one -- the same judgement LoadLedger makes, and cheaper here: nothing
// decides anything from this file, so a lost one costs history and nothing
// else.
func LoadScopes(dir string) Scopes {
	b, err := os.ReadFile(scopesPath(dir))
	if err != nil {
		return Scopes{}
	}
	var s Scopes
	if json.Unmarshal(b, &s) != nil {
		return Scopes{}
	}
	return s
}

func SaveScopes(dir string, s Scopes) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(scopesPath(dir), append(b, '\n'), 0o600)
}

// Record appends one prediction, replacing any earlier entry for the same
// ticket.
//
// REPLACING rather than appending a second row: a ticket re-run after a failed
// landing would otherwise be counted twice, and the older row describes a
// branch that no longer exists. The question this file answers is "was the
// prediction right about the work that landed", which has one answer per
// ticket.
func (s *Scopes) Record(p Prediction, now time.Time) {
	if p.At.IsZero() {
		p.At = now
	}
	for i := range s.Predictions {
		if s.Predictions[i].Key == p.Key {
			s.Predictions[i] = p
			return
		}
	}
	s.Predictions = append(s.Predictions, p)
}

// Missed lists the declared paths a ticket did not in fact touch, and Extra
// the paths it touched that were never declared -- the two halves of "was the
// prediction any good".
//
// Directory grain is honoured on both sides: a declared internal/queue is not
// a miss when the change landed in internal/queue/plan.go, and that file is
// not extra either.
func (p Prediction) Missed() []string { return notCovered(p.Declared, p.Actual) }

// Extra is the other half: what was touched and never declared.
func (p Prediction) Extra() []string { return notCovered(p.Actual, p.Declared) }

// notCovered returns the entries of a that no entry of b shares ground with.
//
// Through fanout.Overlap rather than a comparison written here, because
// "is this the same ground" already has one implementation and a second one
// would be free to disagree with the gate that admits the work.
func notCovered(a, b []string) []string {
	var out []string
	for _, x := range a {
		if len(fanout.Overlap(fanout.Scope{Paths: []string{x}}, fanout.Scope{Paths: b})) == 0 {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
