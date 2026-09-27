package watch

import (
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// OR-423: on log-triage-agent every queued ticket waited behind six
// orion-failed ones, which are never retried. The watcher printed generic
// "blocked by" lines for an hour and stopped with "achieved nothing" -- never
// naming the tickets a person had to requeue.
func TestAnIdleQueueNamesTheFailedTicketsItWaitsBehind(t *testing.T) {
	s := &spy{
		held: []HeldTicket{{Key: "LTA-31", Reason: "blocked by LTA-30"}},
		all: []tracker.Issue{
			{Key: "LTA-2", Labels: []string{tracker.LabelFailed}},
			{Key: "LTA-31", Labels: []string{"ORION"}},
			{Key: "LTA-112", Labels: []string{tracker.LabelFailed}},
		},
	}
	out := runWatch(t, s, Options{Once: true, MaxConcurrent: 1})
	if !strings.Contains(out, "LTA-2, LTA-112: orion-failed and out of automatic retries") {
		t.Errorf("the failed tickets the queue waits on were not named:\n%s", out)
	}
}

// Not said while something is running: then the queue is busy, not stuck.
func TestFailedTicketsAreNotBlamedWhileWorkIsRunning(t *testing.T) {
	s := &spy{all: []tracker.Issue{{Key: "LTA-2", Labels: []string{tracker.LabelFailed}}}}
	p := newPool(2)
	p.live["LTA-9"] = true
	var b strings.Builder
	if _, err := oneTick(Options{Home: t.TempDir()}, s.deps(), &b, slots{cap: 2, here: 1, free: 1}, p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "orion-failed") {
		t.Errorf("blamed failed tickets while a job was running:\n%s", b.String())
	}
}

func TestTheStopMessageNamesTheFailedBlockers(t *testing.T) {
	n := newNoProgress(time.Hour)
	t0 := time.Now()
	n.idled(t0)
	got := n.reason(t0.Add(time.Hour), nil, []string{"LTA-2", "LTA-112"})
	if !strings.Contains(got, "waiting on LTA-2 LTA-112, which are orion-failed and out of automatic retries") {
		t.Errorf("stop message = %q", got)
	}
}
