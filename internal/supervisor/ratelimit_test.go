package supervisor

import (
	"testing"
	"time"
)

var now = time.Unix(1787700000, 0)

// The shape below is captured from a live run, not written from memory. A
// parser built on an imagined schema would pass its tests and read nothing.
const allowedLine = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed",` +
	`"resetsAt":1787790000,"rateLimitType":"five_hour","overageStatus":"allowed",` +
	`"overageResetsAt":1788220800,"isUsingOverage":false},"session_id":"s1"}`

func TestAnAllowedRunReportsItsWindowAndReset(t *testing.T) {
	r, ok := parseRateLimit([]byte(allowedLine))
	if !ok {
		t.Fatal("a real rate_limit_event was not recognised")
	}
	if !r.OK() {
		t.Error("an allowed status must not stop work")
	}
	if r.Type != "five_hour" {
		t.Errorf("Type = %q", r.Type)
	}
	if r.ResetsAt.Unix() != 1787790000 {
		t.Errorf("ResetsAt = %v", r.ResetsAt)
	}
	if r.Wait(now) != 0 {
		t.Error("nothing should wait while the plan is allowing work")
	}
}

// The CLI's vocabulary for refusal is not documented. Matching the one value
// known to mean YES -- rather than guessing the ones that mean no -- makes an
// unfamiliar status err toward stopping rather than toward spending.
func TestAnUnfamiliarStatusStopsWork(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"some_new_state",` +
		`"resetsAt":1787790000,"rateLimitType":"seven_day"}}`
	r, _ := parseRateLimit([]byte(line))
	if r.OK() {
		t.Fatal("an unrecognised status was treated as permission to continue")
	}
	if got := r.Wait(now); got <= 0 {
		t.Fatal("a refusal must produce a wait, or a watcher polls through it")
	}
}

// An absent event must NOT stop work. Refusing because no rate_limit_event
// arrived would make Orion halt against any CLI version that does not emit
// one -- a self-inflicted outage over an advisory field.
func TestAMissingEventDoesNotStopWork(t *testing.T) {
	if _, ok := parseRateLimit([]byte(`{"type":"assistant"}`)); ok {
		t.Fatal("a non-limit line was parsed as one")
	}
	var absent RateLimit
	if !absent.OK() {
		t.Fatal("an unreported limit was treated as a refusal")
	}
}

// Overage is the last warning before a refusal: the normal allowance is
// spent. Work continues, but a watcher should say so.
func TestOverageIsReportedWithoutStoppingWork(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed",` +
		`"rateLimitType":"seven_day","isUsingOverage":true,"overageStatus":"allowed"}}`
	r, _ := parseRateLimit([]byte(line))
	if !r.OK() {
		t.Fatal("overage still allows work")
	}
	if !r.UsingOverage {
		t.Fatal("overage was not recorded")
	}
	if got := r.Describe(now); got == "" || !contains(got, "overage") {
		t.Errorf("Describe = %q, want it to name the overage", got)
	}
}

// Spent allowance AND a refused overage means there is nothing left at all.
func TestARefusedOverageStopsWork(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed",` +
		`"rateLimitType":"seven_day","isUsingOverage":true,"overageStatus":"rejected"}}`
	r, _ := parseRateLimit([]byte(line))
	if r.OK() {
		t.Fatal("work continued with both the allowance and the overage spent")
	}
}

// A refusal's message must answer the only question it raises: when can I
// try again. "Rate limited" alone sends someone to a dashboard.
func TestARefusalNamesWhenItLifts(t *testing.T) {
	r := RateLimit{Status: LimitRejected, Type: "five_hour", ResetsAt: now.Add(90 * time.Minute)}
	got := r.Describe(now)
	if !contains(got, "1h30m") || !contains(got, "five_hour") {
		t.Errorf("Describe = %q, want the window and the time remaining", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
