package quota

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)

func TestDetection(t *testing.T) {
	exhausted := map[string]string{
		"rate limit":     "Error: rate_limit_error: too many requests",
		"429":            "HTTP 429 Too Many Requests",
		"quota exceeded": "Your quota has been exceeded for this period",
		"usage limit":    "Usage limit reached. Try again later.",
		"out of credits": "You're out of credits",
		"overloaded":     "overloaded_error: server is busy",
		"insufficient":   "insufficient quota for this request",
	}
	for name, out := range exhausted {
		t.Run("detects/"+name, func(t *testing.T) {
			if v := Inspect(out, 1, base); !v.Exhausted {
				t.Errorf("should detect exhaustion in %q", out)
			}
		})
	}

	// A genuine bug must never be misread as a quota wall, or Orion would
	// sleep for an hour and retry a broken build.
	notExhausted := []string{
		"panic: runtime error: index out of range",
		"FAIL github.com/x/y 0.2s",
		"error: cannot find module",
		"compilation failed: 3 errors",
		"",
	}
	for _, out := range notExhausted {
		t.Run("ignores/"+truncate(out, 20), func(t *testing.T) {
			if v := Inspect(out, 1, base); v.Exhausted {
				t.Errorf("must not treat %q as quota exhaustion", out)
			}
		})
	}
}

func TestResetParsing(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		wantWait time.Duration
		parsed   bool
	}{
		{
			name:     "retry-after seconds",
			out:      `rate limit exceeded. {"retry-after": 300}`,
			wantWait: 300*time.Second + 15*time.Second,
			parsed:   true,
		},
		{
			name:     "try again in minutes",
			out:      "rate limited. Try again in 10 minutes.",
			wantWait: 10*time.Minute + 15*time.Second,
			parsed:   true,
		},
		{
			name:     "unix reset timestamp",
			out:      fmt.Sprintf(`quota exceeded {"reset_at": %d}`, base.Add(20*time.Minute).Unix()),
			wantWait: 20*time.Minute + 15*time.Second,
			parsed:   true,
		},
		{
			name:     "rfc3339 reset",
			out:      `quota exceeded, resets_at: "` + base.Add(45*time.Minute).Format(time.RFC3339) + `"`,
			wantWait: 45*time.Minute + 15*time.Second,
			parsed:   true,
		},
		{
			name:   "no reset time falls back to backoff",
			out:    "rate_limit_error",
			parsed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := Inspect(tc.out, 1, base)
			if !v.Exhausted {
				t.Fatal("precondition: should be detected as exhausted")
			}
			if v.Parsed != tc.parsed {
				t.Fatalf("Parsed = %v, want %v", v.Parsed, tc.parsed)
			}
			if tc.parsed {
				if d := v.Wait - tc.wantWait; d > time.Second || d < -time.Second {
					t.Errorf("Wait = %s, want ~%s", v.Wait, tc.wantWait)
				}
			} else if v.Wait <= 0 || v.Wait > backoffCap {
				t.Errorf("fallback wait %s outside sane bounds", v.Wait)
			}
		})
	}
}

// An estimated wait must never be reported as if it were a stated reset
// time. Presenting a guess as a fact is how a tool loses trust.
func TestMessageDistinguishesEstimateFromFact(t *testing.T) {
	parsed := Inspect(`{"retry-after": 60}`, 1, base)
	if !strings.Contains(parsed.Message(1), "resets at") {
		t.Error("a parsed reset should be stated as a reset time")
	}
	est := Inspect("rate_limit_error", 1, base)
	if !strings.Contains(est.Message(1), "estimate") {
		t.Error("an unparsed wait must be labelled an estimate")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	var prev time.Duration
	for attempt := 1; attempt <= 8; attempt++ {
		d := backoff(attempt)
		if d > backoffCap {
			t.Fatalf("attempt %d: %s exceeds cap %s", attempt, d, backoffCap)
		}
		if attempt > 1 && d < prev {
			t.Fatalf("attempt %d: backoff shrank from %s to %s", attempt, prev, d)
		}
		prev = d
	}
}

func TestShouldWaitInline(t *testing.T) {
	short := Verdict{Exhausted: true, Wait: 5 * time.Minute}
	if !short.ShouldWaitInline(1) {
		t.Error("a short wait should be sat through")
	}
	long := Verdict{Exhausted: true, Wait: 5 * time.Hour}
	if long.ShouldWaitInline(1) {
		t.Error("a five hour wait must hand back to the human, not hold a process open")
	}
	if short.ShouldWaitInline(MaxAttempts) {
		t.Error("must stop once attempts are exhausted")
	}
	if (Verdict{Exhausted: false, Wait: time.Minute}).ShouldWaitInline(1) {
		t.Error("a non-quota failure must never trigger a wait")
	}
}

// A parse that yields a wait below the floor would retry into the same
// limit immediately.
func TestMinimumWaitEnforced(t *testing.T) {
	v := Inspect(`rate limit. {"retry-after": 0}`, 1, base)
	if v.Wait < minWait {
		t.Errorf("wait %s is below the floor %s", v.Wait, minWait)
	}
}
