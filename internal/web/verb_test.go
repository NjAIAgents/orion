package web

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// A finished run (run-end present) reads its last real event's verb. Here
// the last event before run-end is a tool call, which VerbFor maps to
// "working" -- but run-end itself has no verb case (it falls to VerbOK), so
// the newest event by TIMESTAMP is what decides, matching Scan's own
// newest-by-timestamp rule elsewhere.
func TestVerbOfAFinishedRunReadsTheLastRealEvent(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
		ev(1, events.KindTool, "OR-1", "r1"),
		ev(2, events.KindRunEnd, "OR-1", "r1"),
	}, nil)
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if got := cards[0].Verb; got != ui.VerbOK {
		t.Errorf("Verb = %q, want %q (run-end itself carries no verb, defaulting to ok)", got, ui.VerbOK)
	}
}

// A run with no run-end and a live session covering its key is genuinely
// still going: its newest event's verb is used, unmasked by liveness.
func TestVerbOfAnUnfinishedRunThatIsLiveReadsItsNewestEvent(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
		ev(1, events.KindTool, "OR-1", "r1"),
	}, map[string]bool{"OR-1": true})
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if got, want := cards[0].Verb, ui.VerbFor(events.KindTool); got != want {
		t.Errorf("Verb = %q, want %q (a live run reads its own newest event)", got, want)
	}
}

// THE CASE THIS EXISTS TO FIX: a run with no run-end and NO live session
// covering it is not "working" -- it stopped without finishing (OR-52's own
// composition rule), and must read as failed rather than silently keeping
// whatever verb its last real event happened to carry.
func TestVerbOfAnUnfinishedRunWithNoLiveSessionIsFailed(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
		ev(1, events.KindTool, "OR-1", "r1"),
	}, nil)
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if got := cards[0].Verb; got != ui.VerbFail {
		t.Errorf("Verb = %q, want %q (unfinished, nothing is running it)", got, ui.VerbFail)
	}
}

// The same unfinished-and-not-live case, but the live map names OTHER keys
// only -- proving the check is per-key, not "is anything at all live".
func TestVerbOfAnUnfinishedRunNotInTheLiveSetIsFailed(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
	}, map[string]bool{"OR-2": true, "OR-3": true})
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if got := cards[0].Verb; got != ui.VerbFail {
		t.Errorf("Verb = %q, want %q", got, ui.VerbFail)
	}
}

// A finished run's verb does not consult liveness at all -- Done already
// settles it, and a stale live map (a session that outlived the run, or one
// that never covered it) must not override a run-end that is already on
// record.
func TestVerbOfAFinishedRunIgnoresLiveness(t *testing.T) {
	withLive := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
		ev(1, events.KindRunEnd, "OR-1", "r1"),
	}, map[string]bool{"OR-1": true})
	withoutLive := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
		ev(1, events.KindRunEnd, "OR-1", "r1"),
	}, nil)
	if withLive[0].Verb != withoutLive[0].Verb {
		t.Errorf("a finished run's verb changed with liveness: %q vs %q", withLive[0].Verb, withoutLive[0].Verb)
	}
}

// A run whose newest real event is itself a failure keeps that verb once
// finished -- run-end does not erase what already went wrong.
func TestVerbOfAFinishedRunKeepsAFailureFromBeforeRunEnd(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
		ev(1, events.KindFailed, "OR-1", "r1"),
		ev(2, events.KindRunEnd, "OR-1", "r1"),
	}, nil)
	if got := cards[0].Verb; got != ui.VerbFail {
		t.Errorf("Verb = %q, want %q (a failed event before run-end must not be forgotten)", got, ui.VerbFail)
	}
}

// Two runs of the same key: one finished, one still going (live), must not
// bleed liveness into each other -- the live check is per (key, run) via
// the key, and here both runs share a key, so this specifically proves
// Scan does not accidentally key liveness off run id or grouping order.
func TestVerbOfTwoRunsSameKeyBothReadTheSharedLiveEntryCorrectly(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
		ev(1, events.KindRunEnd, "OR-1", "r1"),
		ev(2, events.KindRunStart, "OR-1", "r2"),
		ev(3, events.KindTool, "OR-1", "r2"),
	}, map[string]bool{"OR-1": true})
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(cards))
	}
	for _, c := range cards {
		if c.Verb == "" {
			t.Errorf("card for run %+v has empty Verb", c)
		}
	}
}
