package web

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// QA coverage for OR-57's Done derivation and the empty/out-of-order edges,
// isolated from the grouping and step-count tests in the other files.

// A run-end event in the log is what marks a session done -- Timing already
// proves this, and Scan must carry it through to the card unchanged.
func TestScanSetsDoneWhenRunEndEventPresent(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
		ev(2*time.Minute, events.KindRunEnd, "OR-57", "r1"),
	}, nil)

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if !cards[0].Session.Done {
		t.Error("Session.Done = false, want true: the run's events include run-end")
	}
}

// No run-end line means the run is still going, and a card must not guess
// otherwise -- Done stays false until the log actually says the run ended.
func TestScanDoneFalseWhenNoRunEndEvent(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
	}, nil)

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if cards[0].Session.Done {
		t.Error("Session.Done = true, want false: no run-end event was in the log")
	}
}

// A machine where nothing has run yet is a normal state, not an error: no
// events in, no cards out, no panic.
func TestScanEmptyLogReturnsZeroCards(t *testing.T) {
	if got := Scan(nil, nil); len(got) != 0 {
		t.Errorf("Scan(nil) returned %d cards, want 0", len(got))
	}
	if got := Scan([]events.Event{}, nil); len(got) != 0 {
		t.Errorf("Scan([]events.Event{}, nil) returned %d cards, want 0", len(got))
	}
}

// A log stitched back together out of order must still yield the newest
// activity AND the newest model by timestamp -- both fields, from the same
// scrambled input, in the same test.
func TestScanActivityAndModelSurviveEventsOutOfFileOrder(t *testing.T) {
	oldest := ev(0, events.KindRunStart, "OR-57", "r1")
	oldest.Model = "opus"

	middle := ev(time.Minute, events.KindTool, "OR-57", "r1")
	middle.Msg, middle.Model = "Read internal/web/cards.go", "sonnet"

	newest := ev(2*time.Minute, events.KindSay, "OR-57", "r1")
	newest.Msg = "writing the done-flag test"

	// Written out of timestamp order: newest first, then oldest, then middle.
	cards := Scan([]events.Event{newest, oldest, middle}, nil)

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Session.Activity, "writing the done-flag test"; got != want {
		t.Errorf("Activity = %q, want %q: the newest event by timestamp, regardless of file position", got, want)
	}
	if got, want := cards[0].Session.Model, "sonnet"; got != want {
		t.Errorf("Model = %q, want %q: the newest non-empty model by timestamp, regardless of file position", got, want)
	}
}
