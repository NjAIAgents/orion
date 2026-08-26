package budget

import (
	"strings"
	"testing"
	"time"
)

// Captured verbatim from a real `claude -p "say ok" --output-format json`
// run, trimmed to the fields the parser reads. Using a real payload rather
// than an invented one is the point: the previous version of this project
// guessed at an external contract and was wrong.
const realResult = `{"type":"result","subtype":"success","is_error":false,"duration_ms":1375,"num_turns":1,"result":"ok","session_id":"415ee91a","total_cost_usd":0.186768,"usage":{"input_tokens":34448,"cache_creation_input_tokens":0,"cache_read_input_tokens":28856,"output_tokens":4},"modelUsage":{"claude-opus-4-8[1m]":{"inputTokens":34448,"outputTokens":4,"costUSD":0.186768,"contextWindow":1000000,"maxOutputTokens":64000}}}`

func TestParsesRealResult(t *testing.T) {
	r, ok := FromResultJSON(realResult)
	if !ok {
		t.Fatal("failed to parse a real result payload")
	}
	if r.CostUSD != 0.186768 {
		t.Errorf("CostUSD = %v", r.CostUSD)
	}
	// 34448 + 0 creation + 28856 read: the three counts are disjoint.
	if r.InputTokens != 63304 {
		t.Errorf("InputTokens = %d, want 63304: input, cache creation and cache read do not overlap", r.InputTokens)
	}
	if r.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d", r.ContextWindow)
	}
}

// A warning line on the same stream must not defeat parsing; the CLI emits
// one when stdin is slow.
func TestParsesPastLeadingWarnings(t *testing.T) {
	noisy := "Warning: no stdin data received in 3s, proceeding without it.\n" + realResult
	if _, ok := FromResultJSON(noisy); !ok {
		t.Fatal("a preceding warning line should not defeat parsing")
	}
}

func TestRejectsUnusableOutput(t *testing.T) {
	for _, s := range []string{"", "not json", "{}", `{"total_cost_usd":0,"usage":{}}`} {
		if _, ok := FromResultJSON(s); ok {
			t.Errorf("%q should not yield a recorded run", s)
		}
	}
}

func TestNoLimitsNeverGates(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{CostUSD: 500, InputTokens: 9e6})
	s := l.Status(Limits{})
	if s.Crossed != 0 {
		t.Error("a budget nobody set must never stop a run")
	}
}

func TestThresholdsFireAscending(t *testing.T) {
	tests := []struct {
		spent float64
		want  int
	}{
		{10, 0}, {50, 50}, {74, 50}, {75, 75}, {91, 90}, {96, 95}, {200, 95},
	}
	for _, tc := range tests {
		l := &Ledger{}
		l.Record(Run{CostUSD: tc.spent})
		got := l.Status(Limits{WeeklyUSD: 100}).Crossed
		if got != tc.want {
			t.Errorf("$%.0f of $100: crossed = %d, want %d", tc.spent, got, tc.want)
		}
	}
}

func TestAckSilencesUntilTheNextThreshold(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{CostUSD: 60})
	lim := Limits{WeeklyUSD: 100}

	if l.Status(lim).Crossed != 50 {
		t.Fatal("expected the 50% checkpoint")
	}
	l.Ack(50)
	if c := l.Status(lim).Crossed; c != 0 {
		t.Errorf("after ack, crossed = %d, want 0", c)
	}
	// Crossing the next threshold must stop again: acknowledging 50 is not
	// consent to spend the rest unattended.
	l.Record(Run{CostUSD: 20})
	if c := l.Status(lim).Crossed; c != 75 {
		t.Errorf("crossed = %d, want 75 after passing the next threshold", c)
	}
}

func TestAckExpiresWithTheWindow(t *testing.T) {
	l := &Ledger{Acked: map[string]time.Time{}}
	// An ack from before the current window began has expired with it.
	l.Acked["50"] = time.Now().Add(-8 * 24 * time.Hour)
	l.Record(Run{CostUSD: 60})
	if c := l.Status(Limits{WeeklyUSD: 100}).Crossed; c != 50 {
		t.Errorf("crossed = %d: a stale ack must not silence a new window", c)
	}
}

func TestAckAllCatchesUp(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{CostUSD: 96})
	lim := Limits{WeeklyUSD: 100}
	l.AckAll(l.Status(lim).Crossed)
	if c := l.Status(lim).Crossed; c != 0 {
		t.Errorf("crossed = %d: acknowledging 95%% should not leave 50, 75 and 90 queued", c)
	}
}

func TestWindowExcludesOldRuns(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{At: time.Now().Add(-8 * 24 * time.Hour), CostUSD: 90})
	l.Record(Run{CostUSD: 10})
	s := l.Status(Limits{WeeklyUSD: 100})
	if s.SpentUSD != 10 {
		t.Errorf("SpentUSD = %v, want 10: spend outside the window must not count", s.SpentUSD)
	}
}

func TestGatesOnTheStricterDimension(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{CostUSD: 10, InputTokens: 900_000})
	s := l.Status(Limits{WeeklyUSD: 100, WeeklyTokens: 1_000_000})
	if s.PercentUSD != 10 || s.PercentTok != 90 {
		t.Fatalf("percent usd=%d tok=%d", s.PercentUSD, s.PercentTok)
	}
	if s.Percent != 90 {
		t.Errorf("Percent = %d: setting both limits means both hold, so the stricter binds", s.Percent)
	}
}

// The message must never imply it knows the provider's remaining quota,
// because it cannot: no interface reports it.
func TestMessageDisclaimsProviderQuota(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{CostUSD: 80})
	m := l.Status(Limits{WeeklyUSD: 100}).Message()
	if !strings.Contains(m, "not your Anthropic plan") {
		t.Error("the notice must distinguish Orion's budget from the plan's limit")
	}
	if !strings.Contains(m, "orion budget ack") {
		t.Error("a stop must name the route forward")
	}
}

func TestContextPressure(t *testing.T) {
	if p := ContextPressure(Run{InputTokens: 500_000, ContextWindow: 1_000_000}); p != 50 {
		t.Errorf("pressure = %d, want 50", p)
	}
	if p := ContextPressure(Run{InputTokens: 500_000}); p != 0 {
		t.Errorf("pressure = %d: unknown window must report 0, not divide by zero", p)
	}
}

func TestTokenBreakdownWeighting(t *testing.T) {
	b := TokenBreakdown{Input: 1000, CacheRead: 10000, CacheCreation: 1000, Output: 1000}
	if got := b.Total(); got != 13000 {
		t.Errorf("Total = %d, want 13000", got)
	}
	// 1000 + 10000*0.1 + 1000*2 + 1000*5 = 9000. Raw totals overstate a
	// cache-heavy session, so a budget built on them binds at the wrong time.
	if got := b.Effective(); got != 9000 {
		t.Errorf("Effective = %d, want 9000", got)
	}
}
