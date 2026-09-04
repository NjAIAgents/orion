package collect

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// slowIsoTester answers "pending" the first few times an ISOLATION ref is
// read, then hands over to the real fake. The batch's own first test reports
// at once, as it does when a rollup already holds a verdict.
type slowIsoTester struct {
	*fakeTester
	pendingReads int
	seen         map[string]int
}

func (s *slowIsoTester) Test(ref string) (bool, error) {
	if strings.Contains(ref, "-iso") {
		if s.seen == nil {
			s.seen = map[string]int{}
		}
		if s.seen[ref] < s.pendingReads {
			s.seen[ref]++
			return false, ErrCheckPending
		}
	}
	return s.fakeTester.Test(ref)
}

// Observed 2026-09-03: a red batch's first split had no check result yet,
// isolation returned that as an error, the batch was declared incomplete and
// its ref deleted, and the next pass assembled it again -- restarting CI every
// pass, forever. Isolation must wait for silence to end (OR-321).
func TestIsolationWaitsForAPendingCheckRatherThanAbandoningTheBatch(t *testing.T) {
	g := newFakeGit()
	tr := &slowIsoTester{fakeTester: &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}},
		pendingReads: 3}

	now := time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)
	slept := 0
	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3", "OR-4"), nil,
		WithIsolationWait(30*time.Minute),
		WithClock(func() time.Time { return now }),
		WithSleeper(func(d time.Duration) { slept++; now = now.Add(d) }))
	if err != nil {
		t.Fatalf("isolation gave up on a check that had merely not reported yet: %v", err)
	}
	if got := b.Members(Culprit); len(got) != 1 || got[0] != "OR-3" {
		t.Errorf("culprit = %v, want [OR-3]: the search must reach a verdict", got)
	}
	if slept == 0 {
		t.Error("nothing waited; the pending reads were not polled")
	}
	if b.Pending {
		t.Error("a batch whose isolation finished must not be reported as pending")
	}
}

// The wait is bounded: silence past the deadline is reported as such, with
// the time waited, rather than looping forever.
func TestIsolationGivesUpAfterTheWaitAndSaysHowLongItWaited(t *testing.T) {
	g := newFakeGit()
	tr := &slowIsoTester{fakeTester: &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}},
		pendingReads: 1_000}

	now := time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)
	_, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3", "OR-4"), nil,
		WithIsolationWait(10*time.Minute),
		WithClock(func() time.Time { return now }),
		WithSleeper(func(d time.Duration) { now = now.Add(d) }))
	if err == nil {
		t.Fatal("a check that never reports must eventually be given up on")
	}
	if !errors.Is(err, ErrCheckPending) || !strings.Contains(err.Error(), "after waiting") {
		t.Errorf("the error must say it waited and for how long: %v", err)
	}
}

// Without a wait configured the old behaviour stands, so callers and tests
// that never asked for one see no change.
func TestNoIsolationWaitMeansAPendingCheckIsStillAnError(t *testing.T) {
	g := newFakeGit()
	tr := &slowIsoTester{fakeTester: &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}},
		pendingReads: 1}
	_, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3", "OR-4"), nil)
	if !errors.Is(err, ErrCheckPending) {
		t.Errorf("with no wait configured a pending isolation check is an error, got %v", err)
	}
}
