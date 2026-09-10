package web

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// QA coverage for OR-57's model-selection and total-ordering rules, each in
// isolation and each its own dedicated case.

// Model is the newest one BY TIMESTAMP, not by file position: an event with
// an earlier timestamp written later in the log must not win.
func TestScanSelectsModelByEventTimestampNotFilePosition(t *testing.T) {
	older := ev(time.Minute, events.KindTool, "OR-57", "r1")
	older.Model = "opus"
	newer := ev(2*time.Minute, events.KindTool, "OR-57", "r1")
	newer.Model = "sonnet"

	// File position is reversed: the newer-timestamped event is written
	// first in the slice, the older-timestamped one last.
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		newer,
		older,
	})

	if got, want := cards[0].Session.Model, "sonnet"; got != want {
		t.Errorf("Model = %q, want %q: the later timestamp must win over the later file position", got, want)
	}
}

// Cards are ordered by key, ascending.
func TestScanOrdersCardsByKeyAscending(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindTool, "OR-58", "r1"),
		ev(time.Minute, events.KindTool, "OR-56", "r1"),
		ev(2*time.Minute, events.KindTool, "OR-57", "r1"),
	})

	if got, want := len(cards), 3; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	gotKeys := []string{cards[0].Key, cards[1].Key, cards[2].Key}
	wantKeys := []string{"OR-56", "OR-57", "OR-58"}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Errorf("cards[%d].Key = %q, want %q (order: %v)", i, gotKeys[i], wantKeys[i], gotKeys)
		}
	}
}

// Cards sharing a key are ordered by run start time, earliest first.
func TestScanOrdersSameKeyCardsByRunStartTimeAscending(t *testing.T) {
	cards := Scan([]events.Event{
		// r2 started later than r1, but is written first in the log.
		ev(5*time.Minute, events.KindRunStart, "OR-57", "r2"),
		ev(6*time.Minute, events.KindTool, "OR-57", "r2"),

		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
	})

	if got, want := len(cards), 2; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Session.Started, base; got != want {
		t.Errorf("cards[0].Session.Started = %v, want %v (r1, the earlier-starting run)", got, want)
	}
	if got, want := cards[1].Session.Started, base.Add(5*time.Minute); got != want {
		t.Errorf("cards[1].Session.Started = %v, want %v (r2, the later-starting run)", got, want)
	}
}

// Cards sharing a key AND a start time fall back to run ID, ascending.
//
// Card and Session carry no run ID (by design -- OR-57 leaves it off the
// model), so the runs are told apart here by their Activity instead: each
// run's own tool-call message identifies which run produced which card.
func TestScanOrdersSameKeySameStartCardsByRunIDAscending(t *testing.T) {
	runR2 := ev(0, events.KindTool, "OR-57", "r2")
	runR2.Msg = "activity from r2"
	runR1 := ev(0, events.KindTool, "OR-57", "r1")
	runR1.Msg = "activity from r1"

	// Both runs start at the same instant; written in descending run-ID
	// order so a passing test can't be an accident of input order.
	cards := Scan([]events.Event{runR2, runR1})

	if got, want := len(cards), 2; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if !cards[0].Session.Started.Equal(cards[1].Session.Started) {
		t.Fatalf("fixture invalid: both runs must share one start time, got %v and %v",
			cards[0].Session.Started, cards[1].Session.Started)
	}
	if got, want := cards[0].Session.Activity, "activity from r1"; got != want {
		t.Errorf("cards[0].Session.Activity = %q, want %q: r1 must sort before r2 when key and start tie", got, want)
	}
	if got, want := cards[1].Session.Activity, "activity from r2"; got != want {
		t.Errorf("cards[1].Session.Activity = %q, want %q", got, want)
	}
}
