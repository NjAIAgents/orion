package watch

import (
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
)

// The failure OR-428 was written for: cycles that achieve nothing, for hours,
// with every existing guard blind to it. 91 CI runs over 13.5 hours, $0 of
// budget spent, and OR-261's warning firing thirty times into an empty room.
func TestAnHourOfNothingTrips(t *testing.T) {
	n := newNoProgress(time.Hour)
	t0 := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	if n.idled(t0) {
		t.Fatal("the first fruitless cycle must only start the clock: a watcher " +
			"waiting for its first ticket has achieved nothing and is not broken")
	}
	if n.idled(t0.Add(59 * time.Minute)) {
		t.Error("tripped inside the window")
	}
	if !n.idled(t0.Add(time.Hour)) {
		t.Error("an hour of cycles achieving nothing must stop the watcher")
	}
}

// The false trip that would make an operator disable this: CI is slow, the
// batch is Pending, and the watcher is behaving perfectly. Pending is
// progress happening elsewhere, so oneTick never reports Moved for it -- but
// the moment anything DOES move, the clock must go back to zero.
func TestProgressResetsTheClock(t *testing.T) {
	n := newNoProgress(time.Hour)
	t0 := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	n.idled(t0)
	n.idled(t0.Add(50 * time.Minute))
	n.progressed()

	if n.idled(t0.Add(55 * time.Minute)) {
		t.Fatal("progress must restart the window, not merely pause it")
	}
	if n.idled(t0.Add(114 * time.Minute)) {
		t.Error("the window is measured from the last progress, not from the " +
			"first idle cycle ever seen")
	}
	if !n.idled(t0.Add(115 * time.Minute)) {
		t.Error("an hour after the last progress must still trip")
	}
}

// A breaker nobody can switch off is a breaker that gets worked around. Zero
// means shipped default at the config layer; by the time it reaches here, a
// zero window is an explicit "off".
func TestADisabledBreakerNeverTrips(t *testing.T) {
	n := newNoProgress(0)
	t0 := time.Now()
	for i := 0; i < 500; i++ {
		if n.idled(t0.Add(time.Duration(i) * time.Hour)) {
			t.Fatal("a zero window disables the breaker")
		}
	}
}

// The message is the whole value of the trip. "Nothing landed" sends someone
// to the logs at 06:00; naming the tickets sends them to the tickets.
func TestTheReasonNamesWhatDidNotHappen(t *testing.T) {
	n := newNoProgress(time.Hour)
	t0 := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	n.idled(t0)
	n.idled(t0.Add(time.Hour))

	got := n.reason(t0.Add(time.Hour), []string{"OR-59", "OR-273", "OR-274"})

	for _, want := range []string{"OR-59", "OR-273", "OR-274", "1h0m0s"} {
		if !strings.Contains(got, want) {
			t.Errorf("the reason omits %q, so the operator cannot act on it without "+
				"opening the log:\n  %s", want, got)
		}
	}
	// An operator reading this at breakfast must know the tree was not
	// touched, or the first thing they do is check for damage.
	if !strings.Contains(got, "as they were") {
		t.Errorf("the reason should say nothing was changed:\n  %s", got)
	}
}

// Eight keys is a readable Slack line; forty is not. The cap exists so the
// message stays worth reading on a wide queue.
func TestAWideStuckSetIsSummarised(t *testing.T) {
	var keys []string
	for i := 0; i < 20; i++ {
		keys = append(keys, "OR-1"+string(rune('0'+i%10)))
	}
	got := joinKeys(keys)
	if !strings.Contains(got, "and 12 more") {
		t.Errorf("a wide set should be summarised, got %q", got)
	}
}

// The shipped default is the answer to "how long may this get nowhere before
// you wake me", and OR-428 settled it at an hour. Asserted because the value
// is the entire user-facing contract of this feature.
func TestTheShippedWindowIsAnHour(t *testing.T) {
	if got := (config.Limits{}).NoProgress(); got != time.Hour {
		t.Errorf("the shipped no-progress window should be 1h, got %s", got)
	}
}
