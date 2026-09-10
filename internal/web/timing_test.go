package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

var base = time.Date(2026, 9, 9, 13, 31, 4, 0, time.UTC)

func at(d time.Duration, kind string) events.Event {
	return events.Event{At: base.Add(d), Kind: kind, Key: "OR-58", Run: "r1"}
}

// OR-58's done-when, first half: a run whose log carries run-end is over, and
// its duration is the span the log recorded -- first event to last.
func TestRunWithRunEndIsDoneAndSpansFirstToLastEvent(t *testing.T) {
	evs := []events.Event{
		at(0, events.KindRunStart),
		at(2*time.Minute, events.KindCommit),
		at(4*time.Minute+12*time.Second, events.KindRunEnd),
	}

	s := Timing(evs)
	if !s.Done {
		t.Errorf("Done = false for a run with %s, want true", events.KindRunEnd)
	}
	if got, want := s.ElapsedAt(base.Add(time.Hour)), 4*time.Minute+12*time.Second; got != want {
		t.Errorf("ElapsedAt = %s, want %s (first event to last)", got, want)
	}
}

// The second half: a run with no run-end is still going, so its duration runs
// to the instant it is asked about, not to its last event. A card that stops
// at the last event reads as finished four minutes after the agent went quiet.
func TestRunWithoutRunEndIsActiveAndElapsesToNow(t *testing.T) {
	evs := []events.Event{
		at(0, events.KindRunStart),
		at(time.Minute, events.KindSay),
	}

	s := Timing(evs)
	if s.Done {
		t.Errorf("Done = true for a run with no %s, want false", events.KindRunEnd)
	}
	if got, want := s.ElapsedAt(base.Add(9*time.Minute)), 9*time.Minute; got != want {
		t.Errorf("ElapsedAt = %s, want %s (to the given instant)", got, want)
	}
}

// An active run has no fixed span: asking again later, with a later now, must
// grow its Elapsed. A done run would answer the same both times; this is what
// tells the two apart from the caller's side.
func TestActiveRunElapsedGrowsAsNowAdvances(t *testing.T) {
	s := Timing([]events.Event{at(0, events.KindRunStart), at(time.Minute, events.KindSay)})

	first := s.ElapsedAt(base.Add(2 * time.Minute))
	second := s.ElapsedAt(base.Add(20 * time.Minute))
	if first == second {
		t.Errorf("ElapsedAt = %s then %s, want different values as now advances", first, second)
	}
	if first != 2*time.Minute {
		t.Errorf("ElapsedAt(base+2m) = %s, want 2m0s", first)
	}
	if second != 20*time.Minute {
		t.Errorf("ElapsedAt(base+20m) = %s, want 20m0s", second)
	}
}

// The distinction is the point: the same events, minus the terminating one,
// must not report the same duration.
func TestDoneAndActiveRunsOfTheSameLengthReportDifferentElapsed(t *testing.T) {
	active := []events.Event{at(0, events.KindRunStart), at(time.Minute, events.KindSay)}
	finished := append(append([]events.Event{}, active...), at(time.Minute, events.KindRunEnd))

	now := base.Add(10 * time.Minute)
	if got := Timing(finished).ElapsedAt(now); got != time.Minute {
		t.Errorf("finished run ElapsedAt = %s, want 1m0s", got)
	}
	if got := Timing(active).ElapsedAt(now); got != 10*time.Minute {
		t.Errorf("active run ElapsedAt = %s, want 10m0s", got)
	}
}

// A finished run replayed at two different instants reports one duration.
// This is the regression the whole file exists to prevent: an ElapsedAt that
// quietly measures to now would pass every test above except this one.
func TestDoneRunReportsTheSameElapsedWheneverItIsAsked(t *testing.T) {
	s := Timing([]events.Event{at(0, events.KindRunStart), at(3*time.Minute, events.KindRunEnd)})

	first := s.ElapsedAt(base.Add(5 * time.Minute))
	second := s.ElapsedAt(base.Add(500 * time.Hour))
	if first != second || first != 3*time.Minute {
		t.Errorf("ElapsedAt = %s then %s, want 3m0s both times", first, second)
	}
}

// File order is not time order: a log written out of sequence must still date
// the session by its earliest and latest events.
func TestTimingTakesTheEarliestAndLatestEventNotTheFirstAndLastLine(t *testing.T) {
	evs := []events.Event{
		at(5*time.Minute, events.KindSay),
		at(0, events.KindRunStart),
		at(2*time.Minute, events.KindCommit),
	}

	s := Timing(evs)
	if !s.Started.Equal(base) {
		t.Errorf("Started = %s, want %s", s.Started, base)
	}
	if !s.Last.Equal(base.Add(5 * time.Minute)) {
		t.Errorf("Last = %s, want %s", s.Last, base.Add(5*time.Minute))
	}
}

// A run with nothing recorded reports nothing, rather than the span since the
// zero time -- which is what a Started defaulted to zero would give.
func TestTimingOfNoEventsReportsNoSessionAndNoDuration(t *testing.T) {
	s := Timing(nil)
	if s.Done || !s.Started.IsZero() || !s.Last.IsZero() {
		t.Errorf("Timing(nil) = %+v, want the zero Session", s)
	}
	if got := s.ElapsedAt(base); got != 0 {
		t.Errorf("ElapsedAt on an empty session = %s, want 0", got)
	}
}

// An active run never reports less than its own events prove. A now that
// arrives before the newest event is a skewed clock or an old log being
// replayed, and neither is a reason to shorten a run that demonstrably ran.
func TestActiveRunNeverReportsLessThanItsEventsProve(t *testing.T) {
	s := Timing([]events.Event{at(0, events.KindRunStart), at(6*time.Minute, events.KindSay)})

	if got, want := s.ElapsedAt(base.Add(time.Minute)), 6*time.Minute; got != want {
		t.Errorf("ElapsedAt with now before the last event = %s, want %s", got, want)
	}
}

// Done is a property of the log, not of arrival order: the terminating event
// counts wherever it sits in the slice.
func TestRunEndAnywhereInTheLogMarksTheRunDone(t *testing.T) {
	evs := []events.Event{at(2*time.Minute, events.KindRunEnd), at(0, events.KindRunStart)}
	if !Timing(evs).Done {
		t.Errorf("Done = false when %s is not the last line, want true", events.KindRunEnd)
	}
}

// A run-end with no matching run-start still counts: Done reads off the
// presence of the event, not off a start/end pair.
func TestRunWithOnlyRunEndEventReportsDone(t *testing.T) {
	s := Timing([]events.Event{at(0, events.KindRunEnd)})
	if !s.Done {
		t.Errorf("Done = false for a log with only %s, want true", events.KindRunEnd)
	}
}

// One event is both the first and the last: Started and Last land on the same
// instant and the span between them is zero.
func TestSingleEventLogReturnsStartedAndLastAsSameTimestampAndZeroElapsed(t *testing.T) {
	s := Timing([]events.Event{at(2*time.Minute, events.KindRunStart)})
	if !s.Started.Equal(s.Last) {
		t.Errorf("Started = %s, Last = %s, want equal", s.Started, s.Last)
	}
	if got := s.Elapsed(); got != 0 {
		t.Errorf("Elapsed = %s, want 0", got)
	}
}

// An event with no timestamp, or the zero time, cannot move Started or Last:
// it did not happen at a known moment, so it is skipped rather than treated
// as the earliest thing in the log.
func TestEventsWithZeroOrInvalidTimestampsAreSkippedForTimingCalculation(t *testing.T) {
	evs := []events.Event{
		{Kind: events.KindRunStart}, // zero At
		at(3*time.Minute, events.KindCommit),
		{Kind: events.KindRunEnd}, // zero At, but still marks Done
	}

	s := Timing(evs)
	if !s.Started.Equal(base.Add(3 * time.Minute)) {
		t.Errorf("Started = %s, want %s (the only event with a real timestamp)", s.Started, base.Add(3*time.Minute))
	}
	if !s.Last.Equal(base.Add(3 * time.Minute)) {
		t.Errorf("Last = %s, want %s", s.Last, base.Add(3*time.Minute))
	}
	if !s.Done {
		t.Errorf("Done = false, want true (run-end still counts even with a zero timestamp)")
	}
}

// A now that lands before the most recent event is a skewed clock or a
// replayed log, not a reason to shorten what the events already prove: the
// span between them is the floor.
func TestActiveRunWithNowBeforeMostRecentEventStillReportsSpanBetweenEvents(t *testing.T) {
	s := Timing([]events.Event{at(0, events.KindRunStart), at(5*time.Minute, events.KindSay)})

	got := s.ElapsedAt(base.Add(2 * time.Minute))
	if got < 5*time.Minute {
		t.Errorf("ElapsedAt(now before last event) = %s, want at least %s", got, 5*time.Minute)
	}
}

// The card itself lives this transition: while a run is active its elapsed
// keeps climbing on every ask, and the instant a run-end event lands, the
// same events (plus that one) freeze it -- asking again later must not move
// it. A card that kept ticking after run-end would look like it is still
// running.
func TestCardElapsedTransitionsFromUpdatingToFixedWhenRunEndAppears(t *testing.T) {
	active := []events.Event{at(0, events.KindRunStart), at(time.Minute, events.KindSay)}

	activeSession := Timing(active)
	firstAsk := activeSession.ElapsedAt(base.Add(2 * time.Minute))
	secondAsk := activeSession.ElapsedAt(base.Add(5 * time.Minute))
	if firstAsk == secondAsk {
		t.Errorf("active card ElapsedAt = %s then %s, want it to keep climbing", firstAsk, secondAsk)
	}

	finished := append(append([]events.Event{}, active...), at(3*time.Minute, events.KindRunEnd))
	doneSession := Timing(finished)
	if !doneSession.Done {
		t.Fatalf("Done = false once run-end lands, want true")
	}
	thirdAsk := doneSession.ElapsedAt(base.Add(6 * time.Minute))
	fourthAsk := doneSession.ElapsedAt(base.Add(time.Hour))
	if thirdAsk != fourthAsk {
		t.Errorf("done card ElapsedAt = %s then %s, want it fixed once run-end lands", thirdAsk, fourthAsk)
	}
	if thirdAsk != 3*time.Minute {
		t.Errorf("done card ElapsedAt = %s, want 3m0s (first event to run-end)", thirdAsk)
	}
}

// Timing reads only Kind and At; it must derive the same Started/Last/Done
// off a log where different actors' events are interleaved -- an
// implementer's commit, QA's verdict, CI's result -- as it does off a single
// actor's own events. Actor is what the reader beside it fills in, not
// something Timing branches on.
func TestTimingDerivesCorrectlyAcrossMultipleSessionTypes(t *testing.T) {
	mixed := []events.Event{
		{At: base, Kind: events.KindRunStart, Actor: events.ActorImplementer, Key: "OR-58", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindCommit, Actor: events.ActorImplementer, Key: "OR-58", Run: "r1"},
		{At: base.Add(2 * time.Minute), Kind: events.KindQA, Actor: events.ActorQA, Key: "OR-58", Run: "r1"},
		{At: base.Add(3 * time.Minute), Kind: events.KindCI, Actor: events.ActorCI, Key: "OR-58", Run: "r1"},
		{At: base.Add(4 * time.Minute), Kind: events.KindRunEnd, Actor: events.ActorDevOps, Key: "OR-58", Run: "r1"},
	}

	s := Timing(mixed)
	if !s.Done {
		t.Errorf("Done = false with a run-end from %s, want true", events.ActorDevOps)
	}
	if !s.Started.Equal(base) {
		t.Errorf("Started = %s, want %s", s.Started, base)
	}
	if !s.Last.Equal(base.Add(4 * time.Minute)) {
		t.Errorf("Last = %s, want %s", s.Last, base.Add(4*time.Minute))
	}
	if got, want := s.ElapsedAt(base.Add(time.Hour)), 4*time.Minute; got != want {
		t.Errorf("ElapsedAt = %s, want %s regardless of the actors involved", got, want)
	}
}

// A session that has been going for a long time accumulates its elapsed
// duration without wrapping or losing precision -- the span is still plain
// subtraction between two timestamps, however far apart they are.
func TestVeryLongRunningActiveSessionAccumulatesElapsedWithoutOverflow(t *testing.T) {
	started := base
	longAgo := started
	s := Timing([]events.Event{
		{At: longAgo, Kind: events.KindRunStart, Key: "OR-58", Run: "r1"},
		{At: longAgo.Add(time.Hour), Kind: events.KindSay, Key: "OR-58", Run: "r1"},
	})

	now := started.Add(365 * 24 * time.Hour)
	want := 365 * 24 * time.Hour
	if got := s.ElapsedAt(now); got != want {
		t.Errorf("ElapsedAt after a year-long run = %s, want %s", got, want)
	}
}

// Timestamps with sub-second precision must not be truncated: two events a
// few hundred milliseconds apart still produce a sub-second Elapsed.
func TestRunsWithSubSecondPrecisionTimestampsMaintainAccuracy(t *testing.T) {
	evs := []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-58", Run: "r1"},
		{At: base.Add(250 * time.Millisecond), Kind: events.KindCommit, Key: "OR-58", Run: "r1"},
		{At: base.Add(837 * time.Millisecond), Kind: events.KindRunEnd, Key: "OR-58", Run: "r1"},
	}

	s := Timing(evs)
	want := 837 * time.Millisecond
	if got := s.Elapsed(); got != want {
		t.Errorf("Elapsed = %s, want %s (sub-second precision preserved)", got, want)
	}
	if got := s.ElapsedAt(base.Add(time.Hour)); got != want {
		t.Errorf("ElapsedAt = %s, want %s", got, want)
	}
}

// A log can carry more than one run-end -- a retry, a duplicated write -- and
// Done must still land on exactly true, not toggle or double-count.
func TestMultipleRunEndEventsStillMarkDoneOnlyOnceTrue(t *testing.T) {
	evs := []events.Event{
		at(0, events.KindRunStart),
		at(time.Minute, events.KindRunEnd),
		at(2*time.Minute, events.KindRunEnd),
		at(3*time.Minute, events.KindRunEnd),
	}

	s := Timing(evs)
	if !s.Done {
		t.Errorf("Done = false with multiple %s events, want true", events.KindRunEnd)
	}
	if got, want := s.Elapsed(), 3*time.Minute; got != want {
		t.Errorf("Elapsed = %s, want %s (span still first event to last)", got, want)
	}
}

// The UI reads ElapsedAt fresh on every page load with a new "now". For a
// finished run that must not matter: two "page refreshes" separated by real
// time apart must render the identical duration, or a card for a run that
// ended yesterday would silently keep growing today.
func TestElapsedDisplayForFinishedRunDoesNotDriftOnPageRefresh(t *testing.T) {
	s := Timing([]events.Event{
		at(0, events.KindRunStart),
		at(90*time.Second, events.KindCommit),
		at(4*time.Minute+7*time.Second, events.KindRunEnd),
	})
	want := 4*time.Minute + 7*time.Second

	firstPageLoad := s.ElapsedAt(base.Add(10 * time.Minute))
	secondPageLoadNextDay := s.ElapsedAt(base.Add(24 * time.Hour))
	if firstPageLoad != want {
		t.Errorf("first refresh ElapsedAt = %s, want %s", firstPageLoad, want)
	}
	if secondPageLoadNextDay != want {
		t.Errorf("refresh a day later ElapsedAt = %s, want %s (no drift)", secondPageLoadNextDay, want)
	}
	if firstPageLoad != secondPageLoadNextDay {
		t.Errorf("ElapsedAt drifted between refreshes: %s then %s", firstPageLoad, secondPageLoadNextDay)
	}
}

// The clock is an argument, never a call. time.Now here would make a finished
// run's duration depend on when the page was opened, and it is the change that
// looks most reasonable in a diff -- so it fails on the call, not on
// arithmetic that only misbehaves in production.
func TestTimingMakesNoCallsToTimeNowOrSince(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "timing.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if pkg.Name == "time" && (sel.Sel.Name == "Now" || sel.Sel.Name == "Since") {
			t.Errorf("timing.go calls time.%s at %s: the instant must come from the caller, "+
				"or two readers of one log disagree", sel.Sel.Name, fset.Position(sel.Pos()))
		}
		return true
	})
}
