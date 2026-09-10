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
