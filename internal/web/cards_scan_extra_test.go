package web

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// A ticketless line -- explicit empty key -- is real but has no ticket to
// draw a card on, so it must not surface as one, and must not be silently
// folded into a real ticket's card either.
func TestScanExcludesEventsWithEmptyKey(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindNote, "", "r0"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
	})

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Key, "OR-57"; got != want {
		t.Errorf("cards[0].Key = %q, want %q", got, want)
	}
	if got, want := cards[0].Session.Steps, 1; got != want {
		t.Errorf("cards[0].Session.Steps = %d, want %d: the empty-key event must not have joined this card", got, want)
	}
}

// A key that was never set is the same zero value as one set to "", but it
// arrives differently -- a caller that forgot the field, not one that chose
// it -- and must be excluded the same way rather than defaulting onto
// whatever ticket happens to be nearby.
func TestScanExcludesEventsWithMissingKey(t *testing.T) {
	noKey := events.Event{At: base, Kind: events.KindTool}
	cards := Scan([]events.Event{noKey, ev(time.Minute, events.KindTool, "OR-57", "r1")})

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Key, "OR-57"; got != want {
		t.Errorf("cards[0].Key = %q, want %q", got, want)
	}
	if got, want := cards[0].Session.Steps, 1; got != want {
		t.Errorf("cards[0].Session.Steps = %d, want %d: the keyless event must not have joined this card", got, want)
	}
}

// Steps counts tool calls one for one -- three calls is three steps, not a
// rounded-off or capped figure.
func TestScanStepCountEqualsToolEventCount(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
		ev(2*time.Minute, events.KindTool, "OR-57", "r1"),
		ev(3*time.Minute, events.KindTool, "OR-57", "r1"),
		ev(4*time.Minute, events.KindRunEnd, "OR-57", "r1"),
	})

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Session.Steps, 3; got != want {
		t.Errorf("Steps = %d, want %d: one per tool call", got, want)
	}
}

// The kinds that are not a tool call -- say, run-start, run-end, commit,
// stage -- must not add to the step count, or a chatty or long-lived run
// would outrank one that did the same amount of work.
func TestScanStepCountExcludesNonToolKinds(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindSay, "OR-57", "r1"),
		ev(2*time.Minute, events.KindCommit, "OR-57", "r1"),
		ev(3*time.Minute, events.KindStage, "OR-57", "r1"),
		ev(4*time.Minute, events.KindRunEnd, "OR-57", "r1"),
	})

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Session.Steps, 0; got != want {
		t.Errorf("Steps = %d, want %d: none of run-start, say, commit, stage, run-end is a tool call", got, want)
	}
}

// Activity is the newest message the agent itself produced -- a tool call or
// a say -- whichever of the two happens to be newest, not whichever kind is
// checked first.
func TestScanActivityIsLatestMessageFromToolOrSay(t *testing.T) {
	toolFirst := ev(time.Minute, events.KindTool, "OR-57", "r1")
	toolFirst.Msg = "Read internal/web/cards.go"
	sayLatest := ev(2*time.Minute, events.KindSay, "OR-57", "r1")
	sayLatest.Msg = "wiring up the grid"

	cards := Scan([]events.Event{ev(0, events.KindRunStart, "OR-57", "r1"), toolFirst, sayLatest})
	if got, want := cards[0].Session.Activity, "wiring up the grid"; got != want {
		t.Errorf("Activity = %q, want %q: say was the newer of the two", got, want)
	}

	sayFirst := ev(time.Minute, events.KindSay, "OR-58", "r1")
	sayFirst.Msg = "starting on the fixture"
	toolLatest := ev(2*time.Minute, events.KindTool, "OR-58", "r1")
	toolLatest.Msg = "Edit internal/web/cards_test.go"

	cards = Scan([]events.Event{ev(0, events.KindRunStart, "OR-58", "r1"), sayFirst, toolLatest})
	if got, want := cards[0].Session.Activity, "Edit internal/web/cards_test.go"; got != want {
		t.Errorf("Activity = %q, want %q: tool was the newer of the two", got, want)
	}
}
