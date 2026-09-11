package web

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

func t0(s int) time.Time { return time.Date(2026, 1, 1, 0, 0, s, 0, time.UTC) }

func evt(at time.Time, kind, key, run, actor, model, msg string) events.Event {
	return events.Event{At: at, Kind: kind, Key: key, Run: run, Actor: actor, Model: model, Msg: msg}
}

// Given a card, When clicked, Then a detail view shows the full ordered step
// list for that run, every tool and say event, with model attribution.
func TestScanDetailOrdersStepsAndCarriesModel(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindTool, "OR-1", "r1", "implementer", "opus", "editing foo.go"),
		evt(t0(2), events.KindSay, "OR-1", "r1", "implementer", "opus", "running tests"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(d.Steps))
	}
	if d.Steps[0].Text != "editing foo.go" || d.Steps[1].Text != "running tests" {
		t.Errorf("Steps out of order: %+v", d.Steps)
	}
	if d.Steps[0].Model != "opus" {
		t.Errorf("Steps[0].Model = %q, want opus", d.Steps[0].Model)
	}
}

// Given a ticket where the implementer asked a question, Then the detail
// shows the ask/answer exchange.
func TestScanDetailPairsAskWithAnswer(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindAsk, "OR-1", "r1", "implementer", "", "which retry policy?"),
		evt(t0(2), events.KindAnswer, "OR-1", "r1", "architect", "opus", "exponential backoff, 3 tries"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Asks) != 1 {
		t.Fatalf("Asks = %d, want 1", len(d.Asks))
	}
	a := d.Asks[0]
	if a.Question != "which retry policy?" {
		t.Errorf("Question = %q", a.Question)
	}
	if a.Answer != "exponential backoff, 3 tries" {
		t.Errorf("Answer = %q", a.Answer)
	}
	if a.Refused {
		t.Error("Refused = true, want false for an answered question")
	}
}

// A refused question is Refused with no Answer, not both filled.
func TestScanDetailMarksARefusedAskWithNoAnswer(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindAsk, "OR-1", "r1", "implementer", "", "is this compliant with GDPR?"),
		evt(t0(2), events.KindRefuse, "OR-1", "r1", "advisor", "", "cannot ground a legal answer"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Asks) != 1 {
		t.Fatalf("Asks = %d, want 1", len(d.Asks))
	}
	if !d.Asks[0].Refused {
		t.Error("Refused = false, want true")
	}
	if d.Asks[0].Answer != "" {
		t.Errorf("Answer = %q, want empty for a refused ask", d.Asks[0].Answer)
	}
}

// An ask with neither an answer nor a refuse yet (still open) is neither
// Refused nor Answered -- the zero value, which the page reads as pending.
func TestScanDetailLeavesAnOpenAskUnresolved(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindAsk, "OR-1", "r1", "implementer", "", "still waiting"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Asks) != 1 {
		t.Fatalf("Asks = %d, want 1", len(d.Asks))
	}
	if d.Asks[0].Refused || d.Asks[0].Answer != "" {
		t.Errorf("open ask should be unresolved, got %+v", d.Asks[0])
	}
}

// Given a ticket that reached a pull request, Then the detail shows the PR
// from the pr event and the CI verdict from the ci event.
func TestScanDetailAttachesCIVerdictToThePR(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindPR, "OR-1", "r1", "orion", "", "https://github.com/x/y/pull/42"),
		evt(t0(2), events.KindCI, "OR-1", "r1", "orion", "", "green"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if d.PR == nil {
		t.Fatal("PR is nil, want set")
	}
	if d.PR.URL != "https://github.com/x/y/pull/42" {
		t.Errorf("PR.URL = %q", d.PR.URL)
	}
	if d.PR.CI != "green" {
		t.Errorf("PR.CI = %q, want green", d.PR.CI)
	}
}

// A ci event with no preceding pr event is dropped rather than fabricating
// a PR to hang it on -- exactly the shape a card should never have to
// explain (an event log written out of order, or a ci event from a run this
// Detail was not asked about).
func TestScanDetailIgnoresACIEventWithNoPrecedingPR(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindCI, "OR-1", "r1", "orion", "", "green"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if d.PR != nil {
		t.Errorf("PR = %+v, want nil (no pr event ever arrived)", d.PR)
	}
}

// Given a ticket with no PR and no decisions, Then those sections are
// ABSENT, not shown empty -- nil slices and a nil pointer, not zero-length
// non-nil ones a page would have to special-case.
func TestScanDetailLeavesUnvisitedSectionsNil(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindTool, "OR-1", "r1", "implementer", "opus", "editing foo.go"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if d.Asks != nil {
		t.Errorf("Asks = %+v, want nil", d.Asks)
	}
	if d.Decisions != nil {
		t.Errorf("Decisions = %+v, want nil", d.Decisions)
	}
	if d.PR != nil {
		t.Errorf("PR = %+v, want nil", d.PR)
	}
}

// Decisions carry the log's own formatted message verbatim, oldest first --
// not parsed apart, per detail.go's own reasoning about not risking a
// rendering that quietly stops matching what was actually written.
func TestScanDetailCarriesDecisionsInOrderVerbatim(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindDecision, "OR-1", "r1", "orion", "", "exponential backoff -- grounded in retry docs; recorded in docs/decisions/or-1-01.md"),
		evt(t0(2), events.KindDecision, "OR-1", "r1", "orion", "", "use sonnet for routing -- grounded in cost data; recorded in docs/decisions/or-1-02.md"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Decisions) != 2 {
		t.Fatalf("Decisions = %d, want 2", len(d.Decisions))
	}
	if d.Decisions[0].Text != evs[0].Msg || d.Decisions[1].Text != evs[1].Msg {
		t.Errorf("Decisions did not carry the log's message verbatim: %+v", d.Decisions)
	}
}

// ScanDetail filters itself: a caller may hand it the whole log, and it
// picks out only the (key, run) pair asked for -- events from a different
// run of the SAME key must not bleed in, since two runs of one ticket are
// two different stories (cards.go's own reasoning for one-card-per-run).
func TestScanDetailFiltersToExactlyOneKeyAndRun(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindTool, "OR-1", "r1", "implementer", "opus", "run 1 step"),
		evt(t0(2), events.KindTool, "OR-1", "r2", "implementer", "opus", "run 2 step"),
		evt(t0(3), events.KindTool, "OR-2", "r1", "implementer", "opus", "different ticket entirely"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Steps) != 1 {
		t.Fatalf("Steps = %d, want 1 (only OR-1/r1's own step)", len(d.Steps))
	}
	if d.Steps[0].Text != "run 1 step" {
		t.Errorf("Steps[0] = %+v, want run 1's step only", d.Steps[0])
	}
}

// An event with no key at all (a supervisor line before any ticket is
// claimed) must never match an empty-string key/run lookup -- ScanDetail
// is always asked about a specific key, never "".
func TestScanDetailOnAnUnknownKeyAndRunIsEmpty(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindTool, "OR-1", "r1", "implementer", "opus", "a step"),
	}
	d := ScanDetail(evs, "OR-999", "no-such-run")
	if d.Steps != nil || d.Asks != nil || d.Decisions != nil || d.PR != nil {
		t.Errorf("Detail for an unknown key/run should be entirely empty, got %+v", d)
	}
}
