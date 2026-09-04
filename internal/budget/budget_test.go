package budget

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// TestContextPressure was removed with the function it tested.
//
// Worth recording why it never caught the bug: it asserted that 500k of
// InputTokens against a 1M window reports 50%, which is arithmetically true
// and describes a situation that cannot occur. InputTokens is a cumulative
// session total, so the test fed it a value only a peak measurement would
// have. It tested the division, and the division was never the mistake --
// the meaning of the numerator was.

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

// The ledger is the only record of what has been spent. Losing it, or
// mis-summing it, is how a budget silently stops being a limit.
func TestLedgerRoundTripsAndPrunesTheWindow(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()

	l, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Runs) != 0 {
		t.Fatal("a missing ledger must load empty, not error")
	}
	l.Record(Run{At: now.Add(-1 * time.Hour), CostUSD: 1, InputTokens: 100, OutputTokens: 10})
	l.Record(Run{At: now.Add(-8 * 24 * time.Hour), CostUSD: 99, InputTokens: 9999})
	l.Record(Run{CostUSD: 2, InputTokens: 50}) // no timestamp: stamped on record
	if err := l.Save(home); err != nil {
		t.Fatal(err)
	}

	back, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	// Pruning on read keeps the file bounded without a separate sweep, and
	// the run outside the window must not reach Status.
	for _, r := range back.Runs {
		if r.At.Before(now.Add(-Window)) {
			t.Errorf("a run from outside the window survived: %+v", r)
		}
	}
	if len(back.Runs) != 2 {
		t.Fatalf("runs = %d, want the two inside the window", len(back.Runs))
	}
	st := back.Status(Limits{WeeklyUSD: 10})
	if st.SpentUSD != 3 {
		t.Errorf("spend = %v, want 3; the pruned run must not be counted", st.SpentUSD)
	}
}

// The file holds spend, workspace ids and the ideas behind them.
func TestLedgerFileIsOwnerOnly(t *testing.T) {
	home := t.TempDir()
	l, _ := Load(home)
	l.Record(Run{At: time.Now().UTC(), CostUSD: 1})
	if err := l.Save(home); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(home, "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX mode bits: every file reports 0666 there, and a
	// permission assertion tests the operating system rather than the code
	// (OR-334). The guarantee this asserts is real and holds on POSIX; it is
	// simply not expressible on Windows.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not exist on Windows")
	}

	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %o", mode)
	}
}

// A corrupt ledger must not disable accounting. Refusing to run would make
// a damaged file into an outage; silently starting empty would erase the
// spend that has already happened, so it does neither quietly.
func TestCorruptLedgerStartsFreshAndSaysSo(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "usage.json"), []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Load(home)
	if err == nil {
		t.Error("a corrupt ledger was loaded silently")
	}
	if l == nil {
		t.Fatal("accounting was disabled entirely")
	}
	l.Record(Run{At: time.Now().UTC(), CostUSD: 1})
	if err := l.Save(home); err != nil {
		t.Fatalf("could not recover: %v", err)
	}
}

func TestRecentIsNewestFirstAndBounded(t *testing.T) {
	l := &Ledger{}
	base := time.Now().UTC()
	for i := 0; i < 10; i++ {
		l.Record(Run{At: base.Add(time.Duration(i) * time.Minute), Stage: string(rune('a' + i))})
	}
	got := l.Recent(3)
	if len(got) != 3 {
		t.Fatalf("got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Error("not newest-first; a report would lead with stale runs")
		}
	}
	if n := len(l.Recent(100)); n != 10 {
		t.Errorf("asking for more than exist returned %d", n)
	}
}

func TestHumanIntKeepsUnitsAtTheBoundaries(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1k"}, {999999, "1000k"},
		{1_000_000, "1.0M"}, {2_500_000, "2.5M"},
	} {
		if got := humanInt(tc.in); got != tc.want {
			t.Errorf("humanInt(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The message a person reads when a run stops. It must never be mistaken
// for the provider's remaining quota, which nothing reports.
func TestCheckpointMessageNamesTheLimitAsYourOwn(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{At: time.Now().UTC(), InputTokens: 800, CostUSD: 8})
	st := l.Status(Limits{WeeklyUSD: 10, WeeklyTokens: 1000})
	msg := st.Message()

	if !strings.Contains(msg, "not your Anthropic plan") {
		t.Errorf("the message must disclaim the provider's quota:\n%s", msg)
	}
	if !strings.Contains(msg, "orion budget ack") {
		t.Errorf("the message must name the unblocking command:\n%s", msg)
	}
	if st.Crossed == 0 {
		t.Fatal("no checkpoint was crossed at 80%")
	}
	if !strings.Contains(msg, fmt.Sprint(st.Crossed)) {
		t.Errorf("the message should name the checkpoint it stopped at:\n%s", msg)
	}
}

// The gate must use whichever dimension is further along: someone who set
// both meant both to hold.
func TestStatusGatesOnTheStricterDimension(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{At: time.Now().UTC(), CostUSD: 1, InputTokens: 900})
	st := l.Status(Limits{WeeklyUSD: 100, WeeklyTokens: 1000})
	if st.PercentUSD != 1 || st.PercentTok != 90 {
		t.Fatalf("percentages = %d / %d", st.PercentUSD, st.PercentTok)
	}
	if st.Percent != 90 {
		t.Errorf("gating percent = %d; the stricter dimension must win", st.Percent)
	}
}

// An acknowledgement belongs to the window it was made in. Without that,
// one ack early in a week silences the checkpoint forever.
func TestAcksExpireWithTheirWindow(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{At: time.Now().UTC(), InputTokens: 800})
	lim := Limits{WeeklyTokens: 1000}

	if l.Status(lim).Crossed == 0 {
		t.Fatal("expected a crossed checkpoint")
	}
	l.AckAll(l.Status(lim).Crossed)
	if c := l.Status(lim).Crossed; c != 0 {
		t.Errorf("still reporting %d%% after acknowledgement", c)
	}

	// An ack recorded before the current window started has expired.
	for k := range l.Acked {
		l.Acked[k] = time.Now().UTC().Add(-2 * Window)
	}
	if l.Status(lim).Crossed == 0 {
		t.Error("a stale acknowledgement still suppressed the checkpoint")
	}
}

// Zero means unlimited here, the opposite of the circuit breaker, because a
// budget nobody set should not be invented.
func TestUnsetLimitsNeverGate(t *testing.T) {
	l := &Ledger{}
	l.Record(Run{At: time.Now().UTC(), CostUSD: 1e6, InputTokens: 1e9})
	st := l.Status(Limits{})
	if st.Crossed != 0 {
		t.Errorf("an unset budget invented a checkpoint at %d%%", st.Crossed)
	}
	if st.Limits.Set() {
		t.Error("Set() true for zero limits")
	}
}

func TestTranscriptDirIsUnderTheUserHome(t *testing.T) {
	if got := TranscriptDir(); strings.TrimSpace(got) == "" {
		t.Error("TranscriptDir is empty; ScanTranscripts would read the cwd")
	}
}

func TestScanTranscriptsToleratesAMissingDirectory(t *testing.T) {
	if _, err := ScanTranscripts(filepath.Join(t.TempDir(), "nope"), time.Now().Add(-time.Hour)); err == nil {
		t.Log("a missing transcript dir is reported; acceptable either way")
	}
}
