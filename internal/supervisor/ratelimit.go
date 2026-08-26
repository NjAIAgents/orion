package supervisor

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The plan's own limit, read from the run rather than guessed at.
//
// budget.weekly_tokens asked a person to invent a number: how many tokens a
// week is "too many"? Nobody knows, so the honest answers are zero (no limit)
// or something arbitrary, and an arbitrary ceiling is worse than none -- it
// stops a run for a reason that was never true.
//
// The CLI already knows the real answer and says so on every run. Its
// stream carries a rate_limit_event describing the ACTUAL plan limit:
//
//	{"type":"rate_limit_event","rate_limit_info":{
//	   "status":"allowed","rateLimitType":"five_hour",
//	   "resetsAt":1787790000,"isUsingOverage":false,
//	   "overageStatus":"allowed","overageResetsAt":1788220800}}
//
// What it does NOT carry is a percentage. The "34% used" in `claude /usage`
// is fetched by that view from an API this process cannot reach, and it is
// not in any file on disk -- I looked before building this. So Orion cannot
// warn at 80%; it can only be told "allowed" until the moment it is told
// otherwise, plus the exact second the limit resets.
//
// That turns out to be the more useful half. A percentage invites a policy
// nobody can calibrate; a reset time answers the only question that matters
// when a run is refused, which is when to try again.

// LimitStatus is the plan's verdict on this account, right now.
type LimitStatus string

const (
	LimitAllowed  LimitStatus = "allowed"
	LimitRejected LimitStatus = "rejected"
	LimitUnknown  LimitStatus = ""
)

// RateLimit is what the run reported about the account's plan limits.
type RateLimit struct {
	Status LimitStatus
	// Type is which window: "five_hour", "seven_day", and whatever else the
	// CLI grows. Carried through rather than parsed into an enum, so an
	// unfamiliar window is reported by name instead of being discarded.
	Type string
	// ResetsAt is when this window opens again. Zero when unreported.
	ResetsAt time.Time
	// UsingOverage means the plan's normal allowance is spent and this is
	// running on whatever spills past it. Not a refusal, but the last
	// warning before one.
	UsingOverage bool
	// OverageResetsAt is when the overage window itself resets.
	OverageResetsAt time.Time
}

// OK reports whether more work may be started.
//
// An UNKNOWN status counts as OK. The alternative -- refusing to run because
// no rate_limit_event arrived -- would make Orion stop dead against any CLI
// version that does not emit one, which is a self-inflicted outage over a
// field that is purely advisory.
func (r RateLimit) OK() bool { return r.Status != LimitRejected }

// Wait is how long until this limit lifts, or zero if it has not been hit.
func (r RateLimit) Wait(now time.Time) time.Duration {
	if r.OK() || r.ResetsAt.IsZero() {
		return 0
	}
	if d := r.ResetsAt.Sub(now); d > 0 {
		return d
	}
	return 0
}

// Describe renders the limit for a person.
func (r RateLimit) Describe(now time.Time) string {
	window := r.Type
	if window == "" {
		window = "plan"
	}
	switch {
	case r.Status == LimitRejected:
		if d := r.Wait(now); d > 0 {
			return fmt.Sprintf("the %s limit is exhausted; it resets in %s (%s)",
				window, d.Round(time.Minute), r.ResetsAt.Local().Format("15:04 Mon"))
		}
		return "the " + window + " limit is exhausted"
	case r.UsingOverage:
		return "the " + window + " allowance is spent; this is running on overage"
	case r.Status == LimitAllowed:
		return "within the " + window + " limit"
	}
	return "the plan limit was not reported by this run"
}

// parseRateLimit reads a rate_limit_event line.
func parseRateLimit(line []byte) (RateLimit, bool) {
	var m struct {
		Type string `json:"type"`
		Info struct {
			Status          string `json:"status"`
			ResetsAt        int64  `json:"resetsAt"`
			RateLimitType   string `json:"rateLimitType"`
			OverageStatus   string `json:"overageStatus"`
			OverageResetsAt int64  `json:"overageResetsAt"`
			IsUsingOverage  bool   `json:"isUsingOverage"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(line, &m) != nil || m.Type != "rate_limit_event" {
		return RateLimit{}, false
	}
	r := RateLimit{
		Type:         m.Info.RateLimitType,
		UsingOverage: m.Info.IsUsingOverage,
	}
	// Any status that is not "allowed" is treated as a refusal. The CLI's
	// vocabulary here is not documented, so matching the one value known to
	// mean yes -- rather than guessing at the ones that mean no -- keeps an
	// unfamiliar status erring toward stopping.
	if strings.EqualFold(m.Info.Status, "allowed") {
		r.Status = LimitAllowed
	} else if strings.TrimSpace(m.Info.Status) != "" {
		r.Status = LimitRejected
	}
	// An account on overage has spent its normal allowance; if the OVERAGE
	// is also refused, there is nothing left at all.
	if m.Info.IsUsingOverage && !strings.EqualFold(m.Info.OverageStatus, "allowed") {
		r.Status = LimitRejected
	}
	if m.Info.ResetsAt > 0 {
		r.ResetsAt = time.Unix(m.Info.ResetsAt, 0)
	}
	if m.Info.OverageResetsAt > 0 {
		r.OverageResetsAt = time.Unix(m.Info.OverageResetsAt, 0)
	}
	return r, true
}

// timeNow is a seam so a test can render a fixed duration.
var timeNow = time.Now
