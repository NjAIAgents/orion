// Package quota detects model quota and rate-limit exhaustion, works out
// when the limit resets, and decides whether to wait.
//
// A caveat worth stating plainly: the wording of these errors is not a
// stable contract. Providers change the text, and Claude Code wraps it
// differently in different versions. So this package is built to fail
// safely rather than cleverly:
//
//   - Detection is a list of patterns, not a single regex. A miss means
//     Orion treats a quota error as an ordinary failure, which is
//     annoying but correct.
//   - Parsing tries several shapes and falls back to capped exponential
//     backoff when none match, rather than inventing a reset time.
//   - Anything detected as exhaustion but unparseable gets its raw text
//     logged verbatim, so a new pattern can be added from evidence.
//
// Never silently sleep for hours. Every wait is announced, logged, capped,
// and recorded in task.json so a killed process can be resumed.
package quota

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Verdict is the outcome of inspecting a failed run.
type Verdict struct {
	// Exhausted is true when the failure looks like a quota or rate limit
	// rather than a genuine error in the work.
	Exhausted bool
	// ResetAt is when the limit is expected to clear. Zero when unknown.
	ResetAt time.Time
	// Wait is how long to sleep before retrying.
	Wait time.Duration
	// Parsed reports whether ResetAt came from the error text or from
	// backoff. Surfaced to the user so an estimated wait is never
	// presented as a fact.
	Parsed bool
	// Kind distinguishes a short rate limit from a long quota exhaustion,
	// because the right response differs: one waits, the other may mean
	// stopping for the day.
	Kind string
	// Raw is the matched line, kept for logging.
	Raw string
}

// Limits on waiting. A five-hour sleep inside a supervised run is almost
// never what someone wants; past this, Orion records a resume time and
// exits so the user decides.
const (
	MaxInlineWait = 90 * time.Minute
	MaxAttempts   = 5
	minWait       = 20 * time.Second
	backoffBase   = 60 * time.Second
	backoffCap    = 30 * time.Minute
)

// exhaustionPatterns are matched case-insensitively against combined
// stdout and stderr. Kept as data so a new provider message is a one-line
// addition rather than a code change.
var exhaustionPatterns = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`(?i)\brate[_ -]?limit(ed|_error)?\b`), "rate_limit"},
	{regexp.MustCompile(`(?i)\b429\b.*\b(too many requests|rate)\b`), "rate_limit"},
	{regexp.MustCompile(`(?i)too many requests`), "rate_limit"},
	{regexp.MustCompile(`(?i)\bquota\b.*\b(exceeded|exhausted|reached)\b`), "quota"},
	{regexp.MustCompile(`(?i)\b(usage|message) limit reached\b`), "quota"},
	{regexp.MustCompile(`(?i)you('| a)re out of (credits|usage)`), "quota"},
	{regexp.MustCompile(`(?i)insufficient (quota|credits)`), "quota"},
	{regexp.MustCompile(`(?i)\bovercapacity\b|\boverloaded_error\b`), "overloaded"},
	// A server sending Retry-After is throttling us. Without this, a response
	// that states the wait exactly is not recognised as exhaustion at all, so
	// the precise answer the server gave is discarded and replaced by a guess.
	{regexp.MustCompile(`(?i)retry[-_ ]?after["':\s]+\d+`), "rate_limit"},
}

// resetPatterns extract when the limit clears. Ordered most reliable
// first: an explicit retry-after beats a parsed wall-clock time, which
// beats a vague human phrase.
var (
	reRetryAfterSecs = regexp.MustCompile(`(?i)retry[-_ ]?after["':\s]+(\d+)`)
	reResetUnix      = regexp.MustCompile(`(?i)reset[a-z_]*["':\s]+(\d{10,13})\b`)
	// The tail must exclude quotes, commas and braces: \S* greedily swallows
	// the closing quote of "2026-08-25T10:45:00Z", and every parse layout
	// then fails on the stray character.
	reResetRFC3339  = regexp.MustCompile(`(?i)reset[a-z_]*["':\s]+(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[^"'\s,}]*)`)
	reResetsAtClock = regexp.MustCompile(`(?i)reset(?:s|ting)?\s+at\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?`)
	reTryAgainIn    = regexp.MustCompile(`(?i)try again in\s+(\d+)\s*(second|minute|hour)s?`)
)

// Inspect examines the combined output of a failed run.
// now is injected so the behaviour is testable without a clock.
func Inspect(output string, attempt int, now time.Time) Verdict {
	v := Verdict{}

	for _, p := range exhaustionPatterns {
		if loc := p.re.FindStringIndex(output); loc != nil {
			v.Exhausted = true
			v.Kind = p.kind
			v.Raw = lineAround(output, loc[0])
			break
		}
	}
	if !v.Exhausted {
		return v
	}

	if reset, okParsed := parseReset(output, now); okParsed {
		v.ResetAt = reset
		v.Parsed = true
		// A small cushion past the stated reset: retrying at the exact
		// second frequently trips the same limit again.
		v.Wait = reset.Sub(now) + 15*time.Second
	} else {
		v.Wait = backoff(attempt)
		v.ResetAt = now.Add(v.Wait)
	}

	if v.Wait < minWait {
		v.Wait = minWait
		v.ResetAt = now.Add(minWait)
	}
	return v
}

func parseReset(out string, now time.Time) (time.Time, bool) {
	if m := reRetryAfterSecs.FindStringSubmatch(out); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 0 && n < 86400 {
			return now.Add(time.Duration(n) * time.Second), true
		}
	}
	if m := reResetUnix.FindStringSubmatch(out); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			// Distinguish seconds from milliseconds by magnitude.
			t := time.Unix(n, 0)
			if n > 1e12 {
				t = time.UnixMilli(n)
			}
			if t.After(now) && t.Sub(now) < 24*time.Hour {
				return t, true
			}
		}
	}
	if m := reResetRFC3339.FindStringSubmatch(out); m != nil {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, strings.TrimSpace(m[1])); err == nil {
				if t.After(now) && t.Sub(now) < 24*time.Hour {
					return t, true
				}
			}
		}
	}
	if m := reTryAgainIn.FindStringSubmatch(out); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			var d time.Duration
			switch strings.ToLower(m[2]) {
			case "second":
				d = time.Duration(n) * time.Second
			case "minute":
				d = time.Duration(n) * time.Minute
			case "hour":
				d = time.Duration(n) * time.Hour
			}
			if d > 0 && d < 24*time.Hour {
				return now.Add(d), true
			}
		}
	}
	// Human clock time, e.g. "resets at 3pm". Interpreted in local time,
	// rolled to tomorrow if it has already passed today.
	if m := reResetsAtClock.FindStringSubmatch(out); m != nil {
		hour, err := strconv.Atoi(m[1])
		if err != nil || hour > 23 {
			return time.Time{}, false
		}
		min := 0
		if m[2] != "" {
			min, _ = strconv.Atoi(m[2])
		}
		switch strings.ToLower(m[3]) {
		case "pm":
			if hour < 12 {
				hour += 12
			}
		case "am":
			if hour == 12 {
				hour = 0
			}
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if t.Sub(now) < 24*time.Hour {
			return t, true
		}
	}
	return time.Time{}, false
}

// backoff is exponential with a cap. Used only when no reset time could
// be read, so it must be conservative: retrying too eagerly against a
// quota wall burns the quota that is being waited for.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := time.Duration(float64(backoffBase) * math.Pow(2, float64(attempt-1)))
	if d > backoffCap {
		d = backoffCap
	}
	return d
}

// ShouldWaitInline reports whether Orion should sleep and retry, or record
// a resume time and hand control back.
func (v Verdict) ShouldWaitInline(attempt int) bool {
	return v.Exhausted && attempt < MaxAttempts && v.Wait <= MaxInlineWait
}

// Message renders the user-facing explanation. It always distinguishes a
// parsed reset from an estimated one, because presenting a guess as a
// fact is how a tool loses trust.
func (v Verdict) Message(attempt int) string {
	var b strings.Builder
	switch v.Kind {
	case "quota":
		b.WriteString("Model quota exhausted.")
	case "overloaded":
		b.WriteString("Model is overloaded upstream.")
	default:
		b.WriteString("Rate limited.")
	}
	if v.Parsed {
		b.WriteString(fmt.Sprintf(" Limit resets at %s (in %s).",
			v.ResetAt.Local().Format("15:04:05 MST"), v.Wait.Round(time.Second)))
	} else {
		b.WriteString(fmt.Sprintf(" No reset time in the error, so backing off %s (estimate, attempt %d/%d).",
			v.Wait.Round(time.Second), attempt, MaxAttempts))
	}
	if v.Raw != "" {
		b.WriteString("\n  provider said: " + truncate(strings.TrimSpace(v.Raw), 160))
	}
	return b.String()
}

func lineAround(s string, idx int) string {
	start := strings.LastIndexByte(s[:idx], '\n') + 1
	end := strings.IndexByte(s[idx:], '\n')
	if end < 0 {
		return s[start:]
	}
	return s[start : idx+end]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
