package watch

import (
	"slices"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// OR-543: a failed ticket is retried without a person once the work branch
// has moved since it failed -- at most maxFailedRetries times.
type retryRig struct {
	s        *spy
	home     string
	head     string
	requeued []string
}

func newRetryRig(t *testing.T) *retryRig {
	r := &retryRig{home: t.TempDir(), head: "aaa"}
	r.s = &spy{all: []tracker.Issue{{Key: "LTA-2", Labels: []string{tracker.LabelFailed}}}}
	return r
}

func (r *retryRig) tick(t *testing.T) (string, tickOutcome) {
	t.Helper()
	d := r.s.deps()
	d.Requeue = func(key, label string) error { r.requeued = append(r.requeued, key); return nil }
	d.BaseHead = func(home, project string) string { return r.head }
	var b strings.Builder
	out, err := oneTick(Options{Home: r.home}, d, &b, slots{cap: 1, free: 1}, newPool(1))
	if err != nil {
		t.Fatal(err)
	}
	return b.String(), out
}

func TestAFailedTicketWaitsForTheWorkBranchToMove(t *testing.T) {
	r := newRetryRig(t)
	out, _ := r.tick(t) // first sight: base recorded, nothing retried
	r.tick(t)           // same base: still nothing
	if len(r.requeued) != 0 {
		t.Fatalf("retried against the base it failed on: %v", r.requeued)
	}
	if !strings.Contains(out, "retried automatically once the work branch moves") {
		t.Errorf("a retryable failed ticket was not reported as waiting:\n%s", out)
	}
	if strings.Contains(out, "out of automatic retries") {
		t.Errorf("a ticket with retries left was sent to a person:\n%s", out)
	}
}

func TestAFailedTicketIsRequeuedOnceTheWorkBranchMoves(t *testing.T) {
	r := newRetryRig(t)
	r.tick(t)
	r.head = "bbb"
	out, res := r.tick(t)
	if !slices.Equal(r.requeued, []string{"LTA-2"}) {
		t.Fatalf("requeued = %v, want [LTA-2]", r.requeued)
	}
	if !res.Moved {
		t.Error("a requeue did not count as progress; the no-progress breaker would trip on it")
	}
	if !strings.Contains(out, "retry 1 of 2") {
		t.Errorf("the requeue was not said:\n%s", out)
	}
}

func TestRetriesStopAtTheCapAndThenAskAPerson(t *testing.T) {
	r := newRetryRig(t)
	r.tick(t)
	for _, h := range []string{"b", "c", "d", "e"} {
		r.head = h
		r.tick(t)
	}
	if len(r.requeued) != maxFailedRetries {
		t.Fatalf("requeued %d times, want the cap of %d", len(r.requeued), maxFailedRetries)
	}
	out, res := r.tick(t)
	if !strings.Contains(out, "LTA-2: orion-failed and out of automatic retries") {
		t.Errorf("an exhausted ticket was not handed to a person:\n%s", out)
	}
	if !slices.Equal(res.Failed, []string{"LTA-2"}) {
		t.Errorf("the stop message would not name it: Failed = %v", res.Failed)
	}
}

func TestNoRetryWhenTheWorkBranchCannotBeRead(t *testing.T) {
	r := newRetryRig(t)
	r.head = ""
	r.tick(t)
	r.tick(t)
	// And becoming readable is not the branch moving: the first real head is
	// the base, not a reason to retry.
	r.head = "aaa"
	r.tick(t)
	if len(r.requeued) != 0 {
		t.Errorf("retried with no way to know the base had moved: %v", r.requeued)
	}
}
