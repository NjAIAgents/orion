package watch

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/ui"
)

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

// OR-240: off a terminal the watcher emits one plain line per run per tick,
// and no cursor control at all.
//
// A redirected log has to stay a log. This is the whole degradation contract,
// and it is checked against the SAME object that pins a region on a terminal:
// there is no second code path to keep honest.
func TestOffTerminalTheWatcherPrintsAPlainHeartbeatPerRun(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	s := &spy{maxSleeps: 3, queued: issues("FCIA-7"), hold: make(chan struct{})}
	releaseAfter(s, 40*time.Millisecond)
	out := runWatch(t, s, Options{MaxConcurrent: 1})

	if strings.Contains(out, "\x1b[") {
		t.Errorf("a non-terminal destination must get no cursor control:\n%q", out)
	}
	var beats []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "calls") {
			beats = append(beats, line)
		}
	}
	if len(beats) == 0 {
		t.Fatalf("expected a plain heartbeat line while a run was in flight:\n%s", out)
	}
	for _, b := range beats {
		if !strings.Contains(b, "FCIA-7") {
			t.Errorf("a heartbeat must name its run: %q", b)
		}
		// One LINE per run per tick: the whole point is that a log stays
		// greppable, so the row must not carry a bar or a sparkline.
		if strings.ContainsAny(b, "━╸─▁▂▃▄▅▆▇█") {
			t.Errorf("the plain form must carry no region glyphs: %q", b)
		}
	}
}

// With nothing in flight and nothing awaiting CI, a tick prints nothing at
// all. A watcher left running overnight must not bury the one line that
// matters under eight hundred saying nothing happened.
func TestATickWithNothingRunningPrintsNothing(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	out := runWatch(t, &spy{maxSleeps: 3}, Options{MaxConcurrent: 2})

	if strings.TrimSpace(out) != "" {
		t.Errorf("a tick with nothing in flight and nothing queued must print nothing:\n%q", out)
	}
}

// A reaped job leaves the live registry, or its row spins forever for work
// that finished. Checked through the display itself rather than through the
// registry's internals: a leak is only a bug because it is visible.
func TestAReapedJobLeavesTheDisplay(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	s := &spy{maxSleeps: 2, queued: issues("FCIA-7"), hold: make(chan struct{})}
	releaseAfter(s, 20*time.Millisecond)
	runWatch(t, s, Options{MaxConcurrent: 1, MaxJobs: 1})

	var buf bytes.Buffer
	live := ui.NewLive(&buf)
	live.Tick()
	live.Close()
	if buf.Len() != 0 {
		t.Errorf("a finished job is still on the display: %q", buf.String())
	}
}
