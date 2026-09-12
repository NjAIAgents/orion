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

// A stage boundary opens a new Stage and closes the one before it -- the
// same "crossing IN to crossing OUT" pairing OR-448's own comment on Stage
// describes. The run's LAST stage stays Done=false: there is no next
// crossing to end it.
func TestScanDetailBuildsStagesFromStageEvents(t *testing.T) {
	evs := []events.Event{
		{At: t0(1), Kind: events.KindStage, Key: "OR-1", Run: "r1", Actor: "implementer",
			Detail: map[string]any{"from": "routing", "to": "implementing"}},
		{At: t0(2), Kind: events.KindStage, Key: "OR-1", Run: "r1", Actor: "qa",
			Detail: map[string]any{"from": "implementing", "to": "qa"}},
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Stages) != 2 {
		t.Fatalf("Stages = %d, want 2", len(d.Stages))
	}
	if !d.Stages[0].Done {
		t.Error("the first stage should be Done: a later crossing ended it")
	}
	if d.Stages[1].Done {
		t.Error("the last stage should not be Done: nothing has ended it yet")
	}
	if d.Stages[1].Name != "qa" || d.Stages[1].Actor != "qa" {
		t.Errorf("Stages[1] = %+v, want Name=qa Actor=qa", d.Stages[1])
	}
}

// KindUsage's cost_usd sums into both the run's total Cost and whichever
// stage was open when it was recorded -- usage carries no stage name of its
// own, so attribution is by timestamp, the same rule Timing already uses
// for activity.
func TestScanDetailAttributesCostToTheOpenStageByTimestamp(t *testing.T) {
	evs := []events.Event{
		{At: t0(1), Kind: events.KindStage, Key: "OR-1", Run: "r1", Actor: "implementer",
			Detail: map[string]any{"from": "routing", "to": "implementing"}},
		{At: t0(2), Kind: events.KindUsage, Key: "OR-1", Run: "r1", Actor: "implementer",
			Detail: map[string]any{"cost_usd": 2.06}},
		{At: t0(3), Kind: events.KindStage, Key: "OR-1", Run: "r1", Actor: "qa",
			Detail: map[string]any{"from": "implementing", "to": "qa"}},
		{At: t0(4), Kind: events.KindUsage, Key: "OR-1", Run: "r1", Actor: "qa",
			Detail: map[string]any{"cost_usd": 0.41}},
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if got, want := d.Cost, 2.47; got != want {
		t.Errorf("Cost = %v, want %v", got, want)
	}
	if got, want := d.Stages[0].Cost, 2.06; got != want {
		t.Errorf("Stages[0].Cost = %v, want %v", got, want)
	}
	if got, want := d.Stages[1].Cost, 0.41; got != want {
		t.Errorf("Stages[1].Cost = %v, want %v", got, want)
	}
}

// Usage recorded before any stage boundary contributes to the run's total
// but has no stage to attribute to -- it is not lost, just unattributed.
func TestScanDetailUsageBeforeAnyStageStillCountsTowardTotal(t *testing.T) {
	evs := []events.Event{
		{At: t0(1), Kind: events.KindUsage, Key: "OR-1", Run: "r1", Actor: "router",
			Detail: map[string]any{"cost_usd": 0.01}},
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if got, want := d.Cost, 0.01; got != want {
		t.Errorf("Cost = %v, want %v", got, want)
	}
	if len(d.Stages) != 0 {
		t.Errorf("Stages = %+v, want none: no boundary was ever crossed", d.Stages)
	}
}

// A plain ask/answer with no router note and no escalation still gets one
// Turn recorded (the answer itself) -- Turns is never nil just because the
// exchange was the simple, one-advisor case.
func TestScanDetailAskWithNoRoutingStillGetsAnAnswerTurn(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindAsk, "OR-1", "r1", "implementer", "", "which retry policy?"),
		evt(t0(2), events.KindAnswer, "OR-1", "r1", "architect", "sonnet", "exponential backoff"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Asks[0].Turns) != 1 {
		t.Fatalf("Turns = %d, want 1: the answer itself", len(d.Asks[0].Turns))
	}
	if d.Asks[0].Turns[0].Kind != events.KindAnswer {
		t.Errorf("Turns[0].Kind = %q, want KindAnswer", d.Asks[0].Turns[0].Kind)
	}
}

// The real shape from internal/work's consult(): router routes, the first
// advisor escalates, the second advisor answers -- three turns, in order,
// closing exactly one ask. This is the exact sequence ask-broker's mockup
// draws (OR-448): ask -> route -> refuse/escalate -> forward -> answer.
func TestScanDetailCapturesTheFullRouteEscalateAnswerExchange(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindAsk, "OR-1", "r1", "implementer", "", "which store owns the gate list?"),
		evt(t0(2), events.KindNote, "OR-1", "r1", events.ActorRouter, "haiku", "routed to the architect"),
		evt(t0(3), events.KindEscalate, "OR-1", "r1", "architect", "sonnet", "escalated to the pm: not grounded in the repo"),
		evt(t0(4), events.KindAnswer, "OR-1", "r1", "pm", "sonnet", "the gate board owns it"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Asks) != 1 {
		t.Fatalf("Asks = %d, want 1", len(d.Asks))
	}
	a := d.Asks[0]
	if len(a.Turns) != 3 {
		t.Fatalf("Turns = %d, want 3 (route, escalate, answer): %+v", len(a.Turns), a.Turns)
	}
	if a.Turns[0].Kind != events.KindNote || a.Turns[0].Actor != events.ActorRouter {
		t.Errorf("Turns[0] = %+v, want the router's note", a.Turns[0])
	}
	if a.Turns[1].Kind != events.KindEscalate || a.Turns[1].Actor != "architect" {
		t.Errorf("Turns[1] = %+v, want the architect's escalation", a.Turns[1])
	}
	if a.Turns[2].Kind != events.KindAnswer || a.Turns[2].Actor != "pm" {
		t.Errorf("Turns[2] = %+v, want the pm's answer", a.Turns[2])
	}
	if a.Answer != "the gate board owns it" || a.Refused {
		t.Errorf("a.Answer=%q a.Refused=%v, want the pm's answer and not refused", a.Answer, a.Refused)
	}
}

// A note from an actor OTHER than the router (e.g. an ordinary narration
// line) must not be swept into Turns -- only the router's own routing
// decision is part of the exchange.
func TestScanDetailIgnoresNonRouterNotesInsideAnOpenAsk(t *testing.T) {
	evs := []events.Event{
		evt(t0(1), events.KindAsk, "OR-1", "r1", "implementer", "", "which retry policy?"),
		evt(t0(2), events.KindNote, "OR-1", "r1", "implementer", "opus", "waiting on the advisor"),
		evt(t0(3), events.KindAnswer, "OR-1", "r1", "architect", "opus", "exponential backoff"),
	}
	d := ScanDetail(evs, "OR-1", "r1")
	if len(d.Asks[0].Turns) != 1 {
		t.Fatalf("Turns = %d, want 1 (only the answer -- the implementer's own note is not routing)",
			len(d.Asks[0].Turns))
	}
}
