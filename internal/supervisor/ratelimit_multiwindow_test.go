package supervisor

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func limitEvent(window, status string, resetsAt time.Time) RateLimit {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"` + status +
		`","rateLimitType":"` + window + `","resetsAt":` +
		strconv.FormatInt(resetsAt.Unix(), 10) + `}}`
	r, ok := parseRateLimit([]byte(line))
	if !ok {
		panic("fixture did not parse: " + line)
	}
	return r
}

// The bug that slept until Monday: the CLI emits one event per window, and
// assignment kept only the last one (OR-162).
func TestEveryWindowIsRetainedNotOverwritten(t *testing.T) {
	now := time.Now()
	var acc RateLimit
	acc = acc.Merge(limitEvent("five_hour", "allowed", now.Add(2*time.Hour)))
	acc = acc.Merge(limitEvent("seven_day", "allowed", now.Add(70*time.Hour)))

	if len(acc.Windows) != 2 {
		t.Fatalf("kept %d windows, want 2: %+v", len(acc.Windows), acc.Windows)
	}
	if _, ok := acc.Windows["five_hour"]; !ok {
		t.Error("five_hour was overwritten by the later event")
	}
}

// The exact shape of the reported incident: blocked on the short window while
// the long one is fine. Waiting on the wrong clock turns a two-hour pause into
// a lost weekend.
func TestWaitUsesTheSoonestBlockingWindow(t *testing.T) {
	now := time.Now()
	var acc RateLimit
	acc = acc.Merge(limitEvent("seven_day", "allowed", now.Add(70*time.Hour)))
	acc = acc.Merge(limitEvent("five_hour", "rejected", now.Add(2*time.Hour)))

	if acc.OK() {
		t.Fatal("a rejected window must stop work")
	}
	d := acc.Wait(now)
	if d > 3*time.Hour {
		t.Fatalf("waited %s; must count down to the FIVE-HOUR reset, not the weekly one", d)
	}
	if got := acc.Describe(now); !strings.Contains(got, "five_hour") {
		t.Errorf("the message must name the blocking window, got: %q", got)
	}
}

// A graded status near the ceiling is a warning, not an outage. This is what
// fired at 80% of the weekly limit and stopped the queue.
func TestAGradedStatusKeepsWorking(t *testing.T) {
	now := time.Now()
	acc := RateLimit{}.Merge(limitEvent("seven_day", "allowed_warning", now.Add(70*time.Hour)))

	if !acc.OK() {
		t.Fatal("a graded 'allowed_*' status must not stop work")
	}
	if got := acc.Describe(now); strings.Contains(got, "exhausted") {
		t.Errorf("a graded status must not be reported as exhaustion, got: %q", got)
	}
}

// A status nobody has decoded still stops, but it must say so honestly rather
// than asserting exhaustion, and it must quote the value so the next unknown
// one is diagnosable.
func TestAnUnrecognisedStatusStopsButSaysItIsUnrecognised(t *testing.T) {
	now := time.Now()
	acc := RateLimit{}.Merge(limitEvent("seven_day", "some_new_state", now.Add(time.Hour)))

	if acc.OK() {
		t.Fatal("an undecoded status must still fail closed")
	}
	got := acc.Describe(now)
	if strings.Contains(got, "exhausted") {
		t.Errorf("must not claim exhaustion for an unknown status, got: %q", got)
	}
	if !strings.Contains(got, "some_new_state") {
		t.Errorf("must quote the raw status so it can be diagnosed, got: %q", got)
	}
}

// Every window allowed reads as allowed, and names them.
func TestAllWindowsAllowedIsAllowed(t *testing.T) {
	now := time.Now()
	var acc RateLimit
	acc = acc.Merge(limitEvent("five_hour", "allowed", now.Add(time.Hour)))
	acc = acc.Merge(limitEvent("seven_day", "allowed", now.Add(70*time.Hour)))

	if !acc.OK() {
		t.Fatal("all windows allowed must be OK")
	}
	if acc.Wait(now) != 0 {
		t.Error("nothing is blocking, so there is nothing to wait for")
	}
}

// A single parsed event with no accumulation must still behave, since that is
// what every existing caller holds.
func TestASingleEventStillWorksWithoutAWindowMap(t *testing.T) {
	now := time.Now()
	r := limitEvent("five_hour", "rejected", now.Add(time.Hour))
	if r.OK() {
		t.Fatal("a rejected single event must stop work")
	}
	if d := r.Wait(now); d <= 0 || d > 2*time.Hour {
		t.Fatalf("wait = %s, want about an hour", d)
	}
}
