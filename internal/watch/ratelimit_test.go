package watch

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/work"
)

// A rate limit is one fact about the account, not n facts about n runs.
//
// OR-162's wait logic was written for a single stream: the run reported a
// limit and the watcher slept inside that run's own path. With n concurrent
// sessions all n hit the ceiling within seconds, and n independent sleeps
// would each re-derive the same pause -- and park the loop that still has to
// reconcile the other n-1 tickets.
func TestARateLimitStopsDispatchForTheWholeWatcher(t *testing.T) {
	stopping.Store(false)
	s := &spy{queued: issues("OR-1", "OR-2", "OR-3", "OR-4"), maxSleeps: 6}

	d := s.deps()
	inner := d.Work
	var mu sync.Mutex
	first := true
	d.Work = func(o work.Options) []work.Result {
		res := inner(o)
		mu.Lock()
		limit := first
		first = false
		mu.Unlock()
		if limit {
			res[0].Limit = supervisor.RateLimit{
				Status:   supervisor.LimitRejected,
				Type:     "five_hour",
				ResetsAt: time.Now().Add(20 * time.Minute),
			}
		}
		return res
	}

	ui.ConsoleReset() // OR-262
	var buf bytes.Buffer
	if err := Run(Options{
		Out: &buf, Home: t.TempDir(), Interval: time.Millisecond, MaxConcurrent: 1,
	}, d); err != nil {
		t.Fatal(err)
	}

	// One job ran, reported the limit, and nothing else was started for the
	// remaining ticks -- the pause belongs to the watcher, not to that run.
	if got := s.workedKeys(); len(got) != 1 {
		t.Fatalf("started %v after a rate limit; the pause must hold for every slot", got)
	}
	out := buf.String()
	if !strings.Contains(out, "five_hour") {
		t.Errorf("the limit must be named: %s", out)
	}
	if n := strings.Count(out, "starting nothing new until"); n != 1 {
		t.Errorf("the pause was announced %d times; it is decided once, centrally:\n%s", n, out)
	}
}

// A reported reset days away is a claim, and OR-162 showed a claim can be
// wrong. Waking early costs one refused tick; waking late costs everything the
// queue would have done.
func TestAnAbsurdlyDistantResetIsCappedRatherThanTrusted(t *testing.T) {
	now := time.Now()
	until, paused := limitPause(io.Discard, work.Result{
		Key: "OR-1",
		Limit: supervisor.RateLimit{
			Status:   supervisor.LimitRejected,
			Type:     "seven_day",
			ResetsAt: now.Add(72 * time.Hour),
		},
	}, now)

	if !paused {
		t.Fatal("an exhausted limit must pause dispatch")
	}
	if d := until.Sub(now); d > maxLimitSleep {
		t.Fatalf("paused for %s; the cap is %s", d, maxLimitSleep)
	}
}

// A run that reported no limit must not pause anything.
func TestACleanRunDoesNotPauseTheWatcher(t *testing.T) {
	if _, paused := limitPause(io.Discard, work.Result{Key: "OR-1"}, time.Now()); paused {
		t.Fatal("a run with no limit verdict stopped the queue")
	}
}
