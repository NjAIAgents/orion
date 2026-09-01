package queue

// The evictions ledger: what was taken out of the queue, why, and how often
// (OR-243).
//
// WHY THIS IS A RECORD AND NOT A COUNT DERIVED AT READ TIME. The rule it
// exists for is "a ticket evicted TWICE escalates to a person instead of being
// retried", and that rule is only as trustworthy as the count behind it. The
// three signals an eviction is decided from are not equally durable: fix
// rounds live in the workspace, breaker trips are keyed by worktree rather
// than by ticket, and a failed settle leaves nothing behind but a dirty
// worktree. Reconstructing "how many times has this been evicted" from those
// on every pass would give a different answer as worktrees are cleaned up --
// and the direction it drifts is toward forgetting, so a ticket that has
// failed twice quietly becomes a ticket that has failed once and gets retried
// forever.
//
// A FILE, for the reason ADR 0004 gives and internal/collect's batch state
// repeats: the whole of the manager's durable memory should be greppable,
// diffable and readable after a crash without a client.
//
// AN EVICTION IS NEVER A SILENT UNLABEL. Every entry here pairs with a named
// state on the ticket carrying the same reason. This file is the history; the
// tracker is the current fact. If they disagree, the tracker wins -- a person
// who re-queued a ticket by hand has made a decision, and the manager's job
// is not to overrule it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// EscalateAfter is how many evictions a ticket gets before the manager stops
// deciding and asks a person.
//
// Two, matching the cap CONVENTIONS-orchestration puts on fix rounds, and for
// the same reason: one failure is an event, two is a pattern, and a third
// attempt at something that has already failed the same way twice is spending
// money to learn nothing.
const EscalateAfter = 2

// Eviction is one removal from the queue.
type Eviction struct {
	Key    string    `json:"key"`
	Reason string    `json:"reason"`
	Rule   string    `json:"rule"` // which rule fired, for counting by cause
	Run    string    `json:"run,omitempty"`
	At     time.Time `json:"at"`
}

// Ledger is the durable history, newest last.
type Ledger struct {
	Evictions []Eviction `json:"evictions"`
	// Passes counts consecutive passes a ticket was considered and not
	// admitted, so nothing rots at the bottom of the queue unnoticed. Reset
	// the moment it is admitted: this measures NEGLECT, not age.
	Passes map[string]int `json:"passes,omitempty"`
}

func ledgerPath(dir string) string { return filepath.Join(dir, "queue-ledger.json") }

// LoadLedger reads the ledger, treating absence and corruption alike as an
// empty one.
//
// The same judgement requests.go and batchstate.go make about their own
// files, and it costs less here than it does there: a lost ledger means a
// ticket gets one more attempt than it should, where refusing to run over an
// unreadable file means the queue stops moving entirely.
func LoadLedger(dir string) Ledger {
	b, err := os.ReadFile(ledgerPath(dir))
	if err != nil {
		return Ledger{}
	}
	var l Ledger
	if json.Unmarshal(b, &l) != nil {
		return Ledger{}
	}
	return l
}

func SaveLedger(dir string, l Ledger) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(ledgerPath(dir), append(b, '\n'), 0o600)
}

// Count is how many times a ticket has been evicted.
func (l Ledger) Count(key string) int {
	n := 0
	for _, e := range l.Evictions {
		if e.Key == key {
			n++
		}
	}
	return n
}

// Last returns the most recent eviction of a ticket.
func (l Ledger) Last(key string) (Eviction, bool) {
	for i := len(l.Evictions) - 1; i >= 0; i-- {
		if l.Evictions[i].Key == key {
			return l.Evictions[i], true
		}
	}
	return Eviction{}, false
}

// Record appends an eviction. Callers pass the clock so a test does not have
// to sleep to produce an ordering.
func (l *Ledger) Record(e Eviction, now time.Time) {
	if e.At.IsZero() {
		e.At = now
	}
	l.Evictions = append(l.Evictions, e)
}

// Missed marks a ticket as considered-and-not-admitted on this pass, and
// returns how many consecutive passes that is now.
func (l *Ledger) Missed(key string) int {
	if l.Passes == nil {
		l.Passes = map[string]int{}
	}
	l.Passes[key]++
	return l.Passes[key]
}

// Admitted clears a ticket's neglect count.
//
// On ADMISSION rather than on completion, because the count measures whether
// the queue is reaching a ticket at all. A ticket admitted and then failed has
// been reached; that it failed is a different problem with a different report.
func (l *Ledger) Admitted(key string) {
	delete(l.Passes, key)
}

// Starved lists tickets not admitted for at least n consecutive passes, in
// key order.
//
// Sorted rather than in map order, because this is printed: an unstable list
// defeats the console's own collapsing of repeated lines, and a report that
// reorders itself every pass reads as churn rather than as a standing warning.
func (l Ledger) Starved(n int) []string {
	var out []string
	for k, c := range l.Passes {
		if c >= n {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Forget drops a ticket's history entirely, for a ticket a person has
// deliberately re-queued.
//
// The manager never calls this on itself. It exists so that re-queueing by
// hand is a decision the manager respects rather than one it undoes on the
// next pass by escalating again on a count the person meant to clear.
func (l *Ledger) Forget(key string) {
	kept := l.Evictions[:0]
	for _, e := range l.Evictions {
		if e.Key != key {
			kept = append(kept, e)
		}
	}
	l.Evictions = kept
	delete(l.Passes, key)
}
