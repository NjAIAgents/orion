package queue

import (
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/tracker"
)

func scoped(key string, paths ...string) tracker.Issue {
	i := tracker.Issue{Key: key}
	if len(paths) > 0 {
		i.Description = "Files: " + strings.Join(paths, ", ")
	}
	return i
}

func verdictOf(ds []Decision, key string) Decision {
	for _, d := range ds {
		if d.Key == key {
			return d
		}
	}
	return Decision{}
}

// The whole point of OR-260: the collision is refused at admission, where it
// costs nothing, instead of at assembly, where the agent has run and the
// tokens are spent.
func TestTwoTicketsWithOverlappingScopeAreNotAdmittedTogether(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{
			scoped("OR-259", "scripts/release.sh"),
			scoped("OR-43", "scripts/release.sh"),
			scoped("OR-92", "internal/update"),
		},
		Free: 3,
	})

	if got := verdictOf(ds, "OR-259").Verdict; got != Admit {
		t.Errorf("OR-259 = %s, want admit: it is the head and collides with nothing", got)
	}
	if got := verdictOf(ds, "OR-92").Verdict; got != Admit {
		t.Errorf("OR-92 = %s, want admit: a different area entirely", got)
	}

	d := verdictOf(ds, "OR-43")
	if d.Verdict != Hold {
		t.Fatalf("OR-43 = %s, want hold: it declares the same file as OR-259", d.Verdict)
	}
	if d.Rule != "scope" {
		t.Errorf("OR-43 held under rule %q, want scope", d.Rule)
	}
	// "the report names the overlap" is an acceptance criterion, not a nicety:
	// a hold nobody can explain is the thing this replaces.
	if !strings.Contains(d.Reason, "scripts/release.sh") ||
		!strings.Contains(d.Reason, "OR-259") {
		t.Errorf("the reason names neither the overlap nor the holder: %q", d.Reason)
	}
}

// A batch forms across passes. A rule that compared only this pass's
// admissions would miss the commonest collision there is: one ticket started
// last tick, another starting now.
func TestACandidateCollidingWithWorkInFlightIsNotAdmitted(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{scoped("OR-136", "internal/actors")},
		Working:    []tracker.Issue{scoped("OR-135", "internal/actors/roster.go")},
		Free:       1,
	})

	d := verdictOf(ds, "OR-136")
	if d.Verdict != Hold || d.Rule != "scope" {
		t.Fatalf("OR-136 = %s/%s, want hold/scope: OR-135 is in flight on the same package",
			d.Verdict, d.Rule)
	}
	if !strings.Contains(d.Reason, "OR-135") {
		t.Errorf("the reason must name the ticket in flight: %q", d.Reason)
	}
}

// An absent scope holds nothing back. Most tickets in a real tracker carry no
// declaration at all, and a gate that treated silence as collision would stop
// the queue on every one of them.
func TestAnAbsentScopeIsAdmittedAlongsideAnything(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{
			scoped("OR-259", "scripts/release.sh"),
			scoped("OR-1"), // declares nothing
			scoped("OR-2"),
		},
		Free: 3,
	})
	for _, key := range []string{"OR-259", "OR-1", "OR-2"} {
		if d := verdictOf(ds, key); d.Verdict != Admit {
			t.Errorf("%s = %s (%s: %s), want admit", key, d.Verdict, d.Rule, d.Reason)
		}
	}
}

// A held ticket is not being worked, so it must not reserve its ground. One
// waiting ticket blocking another for no work in progress would be a queue
// that deadlocks itself.
func TestAHeldTicketDoesNotOccupyItsScope(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{
			scoped("OR-1", "internal/a"),
			scoped("OR-2", "internal/a"), // held: collides with OR-1
			scoped("OR-3", "internal/a"), // must collide with OR-1, not with OR-2
		},
		Free: 3,
	})
	for _, key := range []string{"OR-2", "OR-3"} {
		d := verdictOf(ds, key)
		if d.Verdict != Hold {
			t.Fatalf("%s = %s, want hold", key, d.Verdict)
		}
		if !strings.Contains(d.Reason, "OR-1") {
			t.Errorf("%s is held against a ticket that is not running: %q", key, d.Reason)
		}
	}
}

// Scope is checked BEFORE capacity. "No free slot" is true and useless when
// the ticket would still be waiting next pass for a reason nobody named.
func TestACollisionIsReportedAheadOfNoFreeSlot(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{
			scoped("OR-1", "internal/a"),
			scoped("OR-2", "internal/a"),
		},
		Free: 1,
	})
	if d := verdictOf(ds, "OR-2"); d.Rule != "scope" {
		t.Fatalf("OR-2 held under %q (%s), want scope: the slot was never its problem",
			d.Rule, d.Reason)
	}
}

// A blocked or superseded ticket is refused for that reason, not for a
// collision it also happens to have. The scope rule runs only on tickets that
// were otherwise eligible.
func TestScopeNeverOverridesAnEarlierRefusal(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{
			scoped("OR-1", "internal/a"),
			func() tracker.Issue {
				i := scoped("OR-2", "internal/a")
				i.BlockedBy = []string{"OR-99"}
				return i
			}(),
		},
		Free:     2,
		Resolved: func(string) (bool, bool) { return false, true },
	})
	if d := verdictOf(ds, "OR-2"); d.Rule != "blocked" {
		t.Errorf("OR-2 held under %q, want blocked: the blocker is the actionable fact", d.Rule)
	}
}

// The ledger records the prediction beside the outcome so planning's estimates
// can be judged. Nothing reads it to hold work back.
func TestThePredictionIsRecordedAgainstWhatActuallyLanded(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	s := LoadScopes(dir)
	s.Record(Prediction{
		Key:      "OR-260",
		Declared: []string{"internal/queue", "internal/fanout"},
		Actual:   []string{"internal/queue/plan.go", "internal/watch/watch.go"},
	}, now)
	if err := SaveScopes(dir, s); err != nil {
		t.Fatal(err)
	}

	back := LoadScopes(dir)
	if len(back.Predictions) != 1 {
		t.Fatalf("read back %d predictions, want 1", len(back.Predictions))
	}
	p := back.Predictions[0]
	if p.At != now {
		t.Errorf("At = %v, want %v", p.At, now)
	}
	// internal/queue was declared and touched at file grain: not a miss.
	if got := p.Missed(); len(got) != 1 || got[0] != "internal/fanout" {
		t.Errorf("Missed = %v, want [internal/fanout]", got)
	}
	if got := p.Extra(); len(got) != 1 || got[0] != "internal/watch/watch.go" {
		t.Errorf("Extra = %v, want [internal/watch/watch.go]", got)
	}
}

// A ticket re-run after a failed landing has one answer, not two. The older
// row describes a branch that no longer exists.
func TestRecordingATicketTwiceReplacesTheOlderRow(t *testing.T) {
	now := time.Now().UTC()
	var s Scopes
	s.Record(Prediction{Key: "OR-1", Actual: []string{"a.go"}}, now)
	s.Record(Prediction{Key: "OR-1", Actual: []string{"b.go"}}, now)

	if len(s.Predictions) != 1 {
		t.Fatalf("kept %d rows for one ticket, want 1", len(s.Predictions))
	}
	if got := s.Predictions[0].Actual; len(got) != 1 || got[0] != "b.go" {
		t.Errorf("kept %v, want the latest [b.go]", got)
	}
}

// Two different pieces of ground are already spoken for by two different
// working tickets. Each candidate is judged against that set on its own
// merits: one colliding candidate must not drag down a sibling that shares
// nothing with it, and the ticket it is held against must be the one it
// actually collides with, not whichever came first in the working list.
func TestEachCandidateIsHeldOnlyAgainstTheWorkItActuallyCollidesWith(t *testing.T) {
	ds := Plan(Facts{
		Working: []tracker.Issue{
			scoped("OR-A", "internal/a"),
			scoped("OR-B", "internal/b"),
		},
		Candidates: []tracker.Issue{
			scoped("OR-1", "internal/a"), // collides with OR-A only
			scoped("OR-2", "internal/b"), // collides with OR-B only
			scoped("OR-3", "internal/c"), // collides with neither
		},
		Free: 3,
	})

	d1 := verdictOf(ds, "OR-1")
	if d1.Verdict != Hold || !strings.Contains(d1.Reason, "OR-A") {
		t.Fatalf("OR-1 = %s (%q), want held against OR-A", d1.Verdict, d1.Reason)
	}
	if strings.Contains(d1.Reason, "OR-B") {
		t.Errorf("OR-1 was held against OR-B, which shares nothing with it: %q", d1.Reason)
	}

	d2 := verdictOf(ds, "OR-2")
	if d2.Verdict != Hold || !strings.Contains(d2.Reason, "OR-B") {
		t.Fatalf("OR-2 = %s (%q), want held against OR-B", d2.Verdict, d2.Reason)
	}
	if strings.Contains(d2.Reason, "OR-A") {
		t.Errorf("OR-2 was held against OR-A, which shares nothing with it: %q", d2.Reason)
	}

	// The remainder: a candidate that collides with neither working ticket
	// must be admitted, not swept into a hold because its siblings were held.
	if d3 := verdictOf(ds, "OR-3"); d3.Verdict != Admit {
		t.Errorf("OR-3 = %s, want admit: it collides with neither OR-A nor OR-B", d3.Verdict)
	}
}

// Missed is the half of the ledger that asks "what did planning predict that
// never landed". A path declared but not touched, at file grain, is a miss.
func TestPredictionMissedReturnsDeclaredPathsNotCoveredByActualFiles(t *testing.T) {
	p := Prediction{
		Declared: []string{"internal/queue", "internal/fanout"},
		Actual:   []string{"internal/queue/plan.go"},
	}
	if got := p.Missed(); len(got) != 1 || got[0] != "internal/fanout" {
		t.Errorf("Missed = %v, want [internal/fanout]: internal/queue was touched, internal/fanout was not",
			got)
	}
}

// Extra is the other half: what the branch touched that nothing declared.
func TestPredictionExtraReturnsActualFilesNotCoveredByDeclaredPaths(t *testing.T) {
	p := Prediction{
		Declared: []string{"internal/queue"},
		Actual:   []string{"internal/queue/plan.go", "internal/watch/watch.go"},
	}
	if got := p.Extra(); len(got) != 1 || got[0] != "internal/watch/watch.go" {
		t.Errorf("Extra = %v, want [internal/watch/watch.go]: internal/queue/plan.go was declared",
			got)
	}
}

// Both halves of the ledger honour directory grain through fanout.Overlap: a
// declared internal/queue is not a miss when the change landed in
// internal/queue/plan.go, and that file is not extra either.
func TestPredictionHonoursDirectoryGrain(t *testing.T) {
	p := Prediction{
		Declared: []string{"internal/queue"},
		Actual:   []string{"internal/queue/plan.go"},
	}
	if got := p.Missed(); len(got) != 0 {
		t.Errorf("Missed = %v, want nothing: internal/queue covers internal/queue/plan.go", got)
	}
	if got := p.Extra(); len(got) != 0 {
		t.Errorf("Extra = %v, want nothing: internal/queue/plan.go is covered by internal/queue", got)
	}
}

// A row is written even when the branch's diff could not be read -- dropping
// it would make the ledger a sample of the runs whose diff happened to fetch.
// But an unreadable diff is not evidence of an empty change, so it answers
// neither half of "was the prediction any good".
func TestAnUnreadableDiffIsRecordedWithoutBeingJudged(t *testing.T) {
	var s Scopes
	s.Record(Prediction{
		Key:        "OR-1",
		Declared:   []string{"internal/a", "internal/b"},
		Unreadable: "could not fetch the remote",
	}, time.Now().UTC())

	if len(s.Predictions) != 1 {
		t.Fatalf("recorded %d rows, want 1: the prediction was made either way", len(s.Predictions))
	}
	p := s.Predictions[0]
	if got := p.Missed(); len(got) != 0 {
		t.Errorf("Missed = %v; nobody looked at the diff, so nothing was missed", got)
	}
	if got := p.Extra(); len(got) != 0 {
		t.Errorf("Extra = %v; there is no diff to have touched anything", got)
	}
}
