package web

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// QA coverage for OR-57's activity/model derivation: one dedicated case per
// rule, isolated from the grouping and step-count tests in the other files.

// A run that never emitted a tool call or a say has nothing to report as
// activity -- run-start, commit, stage, run-end are Orion's own words about
// the run, not the agent's.
func TestScanLeavesActivityEmptyWhenNoToolOrSayEvents(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindCommit, "OR-57", "r1"),
		ev(2*time.Minute, events.KindStage, "OR-57", "r1"),
		ev(3*time.Minute, events.KindRunEnd, "OR-57", "r1"),
	}, nil)

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got := cards[0].Session.Activity; got != "" {
		t.Errorf("Activity = %q, want empty: no KindTool or KindSay event was in the log", got)
	}
}

// Activity is chosen by the event's own timestamp, not by where the line
// sits in the log -- a log stitched back together out of order must still
// surface the newest thing the agent said, not the last line written.
func TestScanActivitySelectedByTimestampNotFilePosition(t *testing.T) {
	writtenFirstButOlder := ev(time.Minute, events.KindTool, "OR-57", "r1")
	writtenFirstButOlder.Msg = "Read internal/web/model.go"
	writtenLastButNewer := ev(5*time.Minute, events.KindSay, "OR-57", "r1")
	writtenLastButNewer.Msg = "wiring up activity"

	cards := Scan([]events.Event{
		writtenLastButNewer,
		ev(0, events.KindRunStart, "OR-57", "r1"),
		writtenFirstButOlder,
	}, nil)

	if got, want := cards[0].Session.Activity, "wiring up activity"; got != want {
		t.Errorf("Activity = %q, want %q: the newer event by timestamp, not the one written last", got, want)
	}
}

// Model is the latest non-empty model value across the run's events, not the
// first one seen or the one on the newest event regardless of value.
func TestScanModelIsLatestNonEmptyValueFromEvents(t *testing.T) {
	first := ev(0, events.KindRunStart, "OR-57", "r1")
	first.Model = "opus"
	second := ev(time.Minute, events.KindTool, "OR-57", "r1")
	second.Model = "sonnet"
	third := ev(2*time.Minute, events.KindTool, "OR-57", "r1")
	third.Model = "haiku"

	cards := Scan([]events.Event{first, second, third}, nil)
	if got, want := cards[0].Session.Model, "haiku"; got != want {
		t.Errorf("Model = %q, want %q: the newest non-empty model value", got, want)
	}
}

// A run whose events never carry a model value reports one that is empty --
// nothing is guessed or defaulted in its place.
func TestScanModelEmptyWhenNoEventCarriesModel(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
		ev(2*time.Minute, events.KindRunEnd, "OR-57", "r1"),
	}, nil)

	if got := cards[0].Session.Model; got != "" {
		t.Errorf("Model = %q, want empty: no event carried a model value", got)
	}
}

// An event with no model value does not clear a model already established by
// an earlier event -- most kinds carry no model, and silence is not a change
// of model.
func TestScanSilenceDoesNotClearAnExistingModel(t *testing.T) {
	withModel := ev(0, events.KindRunStart, "OR-57", "r1")
	withModel.Model = "opus"

	cards := Scan([]events.Event{
		withModel,
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
		ev(2*time.Minute, events.KindSay, "OR-57", "r1"),
		ev(3*time.Minute, events.KindRunEnd, "OR-57", "r1"),
	}, nil)

	if got, want := cards[0].Session.Model, "opus"; got != want {
		t.Errorf("Model = %q, want %q: later modelless events must not clear it", got, want)
	}
}
