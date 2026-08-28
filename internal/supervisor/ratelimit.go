package supervisor

import (
	"encoding/json"
	"fmt"
	"sort"
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
//
// Three states, not two. The original pair treated every status that was not
// the literal "allowed" as a refusal, which turned a graded warning near the
// ceiling into the sentence "the seven_day limit is exhausted" -- and then
// into a watcher that slept until Monday while the account still had a fifth
// of its weekly allowance (OR-162). Failing closed was right; saying
// something false about the account was not.
type LimitStatus string

const (
	LimitAllowed  LimitStatus = "allowed"
	LimitRejected LimitStatus = "rejected"
	// LimitUnrecognised is a status the CLI sent that this build has never
	// seen. It still stops work -- the vocabulary is undocumented, so an
	// unknown value could mean anything -- but it is REPORTED as unknown
	// rather than asserted as exhaustion, and it carries the raw string so
	// the next one is diagnosable instead of guessed at again.
	LimitUnrecognised LimitStatus = "unrecognised"
	LimitUnknown      LimitStatus = ""
)

// allowedish are the statuses that mean "keep going". Matched case
// insensitively, and by prefix for the graded ones, because the CLI reports
// pressure near a ceiling before it refuses: a value like allowed_warning is
// a heads-up, not a stop.
func classifyLimit(status string) LimitStatus {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case s == "":
		return LimitUnknown
	case s == "allowed" || strings.HasPrefix(s, "allowed"):
		return LimitAllowed
	case s == "rejected" || s == "exhausted" || s == "blocked" || s == "denied":
		return LimitRejected
	}
	return LimitUnrecognised
}

// RateLimit is what the run reported about the account's plan limits.
//
// An account has SEVERAL concurrent windows -- five_hour and seven_day today,
// whatever the CLI grows tomorrow -- and the CLI emits one event per window.
// Windows holds them all, keyed by type. The top-level fields describe the
// window this value itself came from, so a single parsed event is still a
// usable RateLimit and every existing caller keeps working.
//
// Keeping only the last window seen is what made a five-hour pause
// indistinguishable from a weekly one, and let Orion sleep on the wrong
// clock (OR-162).
type RateLimit struct {
	Status LimitStatus
	// Type is which window: "five_hour", "seven_day", and whatever else the
	// CLI grows. Carried through rather than parsed into an enum, so an
	// unfamiliar window is reported by name instead of being discarded.
	Type string
	// Raw is the status string exactly as the CLI sent it, kept so an
	// unrecognised value can be named in the message rather than guessed at.
	Raw string
	// ResetsAt is when this window opens again. Zero when unreported.
	ResetsAt time.Time
	// UsingOverage means the plan's normal allowance is spent and this is
	// running on whatever spills past it. Not a refusal, but the last
	// warning before one.
	UsingOverage bool
	// OverageResetsAt is when the overage window itself resets.
	OverageResetsAt time.Time
	// Windows is every window this run reported, keyed by Type. Nil on a
	// value parsed from a single event; populated by the stream reader as
	// events arrive.
	Windows map[string]RateLimit
}

// each iterates every known window, falling back to the value itself when no
// per-window map was collected.
func (r RateLimit) each() []RateLimit {
	if len(r.Windows) == 0 {
		return []RateLimit{r}
	}
	out := make([]RateLimit, 0, len(r.Windows))
	for _, v := range r.Windows {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Merge records w as the current state of its window, returning the combined
// value. The receiver's own top-level fields are replaced by w's, so the
// most recent event is what a single-window caller sees.
func (r RateLimit) Merge(w RateLimit) RateLimit {
	windows := map[string]RateLimit{}
	for k, v := range r.Windows {
		windows[k] = v
	}
	key := w.Type
	if key == "" {
		key = "plan"
	}
	w.Windows = nil
	windows[key] = w

	out := w
	out.Windows = windows
	return out
}

// OK reports whether more work may be started.
//
// An UNKNOWN status counts as OK. The alternative -- refusing to run because
// no rate_limit_event arrived -- would make Orion stop dead against any CLI
// version that does not emit one, which is a self-inflicted outage over a
// field that is purely advisory.
// It is false only when SOME window is actually stopping work. A window near
// its ceiling reports a graded status and stays OK, which is the difference
// between a warning and an outage.
func (r RateLimit) OK() bool {
	for _, w := range r.each() {
		if w.Status == LimitRejected || w.Status == LimitUnrecognised {
			return false
		}
	}
	return true
}

// Wait is how long until work can resume: the SOONEST reset among the windows
// that are actually blocking, never whichever event happened to arrive last.
//
// Waiting on the wrong window is how a two-hour five-hour pause became a
// sleep until Monday (OR-162).
func (r RateLimit) Wait(now time.Time) time.Duration {
	var soonest time.Duration
	for _, w := range r.each() {
		if w.Status != LimitRejected && w.Status != LimitUnrecognised {
			continue
		}
		if w.ResetsAt.IsZero() {
			continue
		}
		if d := w.ResetsAt.Sub(now); d > 0 && (soonest == 0 || d < soonest) {
			soonest = d
		}
	}
	return soonest
}

// blocking returns the window whose reset Wait is counting down to, so a
// message can name the right one.
func (r RateLimit) blocking(now time.Time) (RateLimit, bool) {
	var best RateLimit
	var found bool
	for _, w := range r.each() {
		if w.Status != LimitRejected && w.Status != LimitUnrecognised {
			continue
		}
		if !found || (!w.ResetsAt.IsZero() &&
			(best.ResetsAt.IsZero() || w.ResetsAt.Before(best.ResetsAt))) {
			best, found = w, true
		}
	}
	return best, found
}

func (r RateLimit) window() string {
	if r.Type == "" {
		return "plan"
	}
	return r.Type
}

// Describe renders the limit for a person, naming the window that is actually
// blocking rather than the one most recently reported.
func (r RateLimit) Describe(now time.Time) string {
	if b, ok := r.blocking(now); ok {
		// An unrecognised status is reported AS unrecognised. Saying "the
		// seven_day limit is exhausted" about a status nobody has decoded is
		// a false statement about the account, and it trains the operator to
		// ignore the warning that will eventually be true (OR-162).
		if b.Status == LimitUnrecognised {
			return fmt.Sprintf(
				"the %s limit reported a status this build does not recognise (%q); "+
					"stopping to be safe. If work should have continued, this is a bug",
				b.window(), b.Raw)
		}
		if d := r.Wait(now); d > 0 {
			return fmt.Sprintf("the %s limit is exhausted; it resets in %s (%s)",
				b.window(), d.Round(time.Minute), b.ResetsAt.Local().Format("15:04 Mon"))
		}
		return "the " + b.window() + " limit is exhausted"
	}

	// Nothing is blocking. Report overage, which is the last warning before
	// a refusal, then the ordinary allowed case.
	for _, w := range r.each() {
		if w.UsingOverage {
			return "the " + w.window() + " allowance is spent; this is running on overage"
		}
	}
	var named []string
	for _, w := range r.each() {
		if w.Status == LimitAllowed {
			named = append(named, w.window())
		}
	}
	if len(named) > 0 {
		return "within the " + strings.Join(named, " and ") + " limit"
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
		Raw:          m.Info.Status,
		UsingOverage: m.Info.IsUsingOverage,
		Status:       classifyLimit(m.Info.Status),
	}
	// An account on overage has spent its normal allowance; if the OVERAGE
	// is also refused, there is nothing left at all. Classified the same way,
	// so a graded overage status is a warning rather than a hard stop.
	if m.Info.IsUsingOverage && classifyLimit(m.Info.OverageStatus) == LimitRejected {
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
