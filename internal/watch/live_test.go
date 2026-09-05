package watch

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/ui"
)

// Showing the checks costs nothing beyond the read the verdict already came
// from: every reconcile tick calls Collect exactly once, and the checks line
// is built from that same call's []collect.Result (liveChecks takes no
// fetcher of its own). If displaying them ever cost a second `gh pr view`,
// collects would climb past the number of reconcile ticks; it does not
// (OR-310).
func TestChecksCostNoAdditionalAPICallBeyondTheTickThatFetchedThem(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	s := &spy{
		maxSleeps: 3, queued: issues("FCIA-7"), hold: make(chan struct{}),
		pendingTicks: 3,
		pendingChecks: []collect.Check{
			{Name: "go (ubuntu)", State: collect.CheckRunning},
		},
	}
	releaseAfter(s, 40*time.Millisecond)
	out := runWatch(t, s, Options{MaxConcurrent: 1})

	_ = out
	if s.collects != s.sleeps {
		t.Errorf("collect ran %d times against %d reconcile ticks: showing the checks must not add an extra call",
			s.collects, s.sleeps)
	}
}

// releaseAfter frees the spy's held jobs once the watcher has had time to
// dispatch and tick. The hold blocks the JOB goroutine, never the loop, so
// the watcher keeps ticking with a run genuinely in flight -- which is the
// only situation the live display exists for.
func releaseAfter(s *spy, d time.Duration) {
	go func() {
		time.Sleep(d)
		close(s.hold)
	}()
}
