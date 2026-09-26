package web

// OR-59 QA coverage: two more fixture-log edge cases from the same table in
// fixture_logs_test.go, broken out the way fixture_logs_qa_test.go already
// broke out the truncated-log and concurrent-runs cases -- one dedicated case
// per rule so a regression fails by name.

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// crossKeyFixture is two different tickets, each with a single run, sharing
// one workspace's log -- a supervisor working OR-59 while another agent picks
// up OR-61 in the same session. Shared by every cross-key case below.
func crossKeyFixture(t *testing.T, path string) []events.Event {
	t.Helper()
	writeRunLog(t, path, []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindRunStart, Key: "OR-61", Run: "r1"},
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
		{At: base.Add(3 * time.Minute), Kind: events.KindTool, Key: "OR-61", Run: "r1", Msg: "Grep internal/web/timing.go"},
		{At: base.Add(4 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Edit internal/web/cards.go"},
		{At: base.Add(5 * time.Minute), Kind: events.KindRunEnd, Key: "OR-59", Run: "r1"},
		{At: base.Add(6 * time.Minute), Kind: events.KindTool, Key: "OR-61", Run: "r1", Msg: "Edit internal/web/timing.go"},
	})

	evs, err := events.Read(path)
	if err != nil {
		t.Fatalf("events.Read(%s) = %v, want the log to read cleanly", path, err)
	}
	return evs
}

// Two ticket keys sharing one log must become two cards, ordered by key --
// Scan's documented total order -- not by which run started first.
func TestScanCrossKeyEventsProduceOneCardPerKey(t *testing.T) {
	evs := crossKeyFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := len(cards), 2; got != want {
		t.Fatalf("Scan returned %d cards, want %d (one per ticket key)", got, want)
	}
	if got, want := cards[0].Key, "OR-59"; got != want {
		t.Errorf("cards[0].Key = %q, want %q", got, want)
	}
	if got, want := cards[1].Key, "OR-61"; got != want {
		t.Errorf("cards[1].Key = %q, want %q", got, want)
	}
}

// Each key's card counts, times and narrates only its own events. A step
// count, activity or done-state that leaked from the other key's line would
// be the tell that grouping crossed tickets instead of just runs.
func TestScanCrossKeyEventsHaveNoCrosstalkBetweenKeys(t *testing.T) {
	evs := crossKeyFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	or59, or61 := cards[0], cards[1]

	if got, want := or59.Session.Steps, 2; got != want {
		t.Errorf("OR-59 Session.Steps = %d, want %d (OR-61's tool call must not count)", got, want)
	}
	if got, want := or59.Session.Activity, "Edit internal/web/cards.go"; got != want {
		t.Errorf("OR-59 Session.Activity = %q, want %q", got, want)
	}
	if !or59.Session.Done {
		t.Error("OR-59 Session.Done = false, want true: OR-59's log carries its own run-end")
	}
	if got, want := or59.Session.Started, base; !got.Equal(want) {
		t.Errorf("OR-59 Session.Started = %s, want %s", got, want)
	}
	if got, want := or59.Session.Elapsed(), 5*time.Minute; got != want {
		t.Errorf("OR-59 Session.Elapsed() = %s, want %s", got, want)
	}

	if got, want := or61.Session.Steps, 2; got != want {
		t.Errorf("OR-61 Session.Steps = %d, want %d (OR-59's tool calls must not count)", got, want)
	}
	if got, want := or61.Session.Activity, "Edit internal/web/timing.go"; got != want {
		t.Errorf("OR-61 Session.Activity = %q, want %q", got, want)
	}
	if or61.Session.Done {
		t.Error("OR-61 Session.Done = true, want false: OR-61 has no run-end in this log")
	}
	if got, want := or61.Session.Started, base.Add(time.Minute); !got.Equal(want) {
		t.Errorf("OR-61 Session.Started = %s, want %s", got, want)
	}
	if got, want := or61.Session.Elapsed(), 5*time.Minute; got != want {
		t.Errorf("OR-61 Session.Elapsed() = %s, want %s", got, want)
	}
}

// scrambledOrderFixture is one run whose four events are written to the log
// in an order severely unlike their timestamps: the newest event first, the
// oldest event last, with the middle two swapped as well. Nothing about
// events.Read or Scan may assume the file was appended to in time order --
// this is the fixture that would catch it if either did.
func scrambledOrderFixture(t *testing.T, path string) []events.Event {
	t.Helper()
	writeRunLog(t, path, []events.Event{
		{At: base.Add(3 * time.Minute), Kind: events.KindRunEnd, Key: "OR-59", Run: "r1"},
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Edit internal/web/cards.go"},
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
	})

	evs, err := events.Read(path)
	if err != nil {
		t.Fatalf("events.Read(%s) = %v, want the log to read cleanly", path, err)
	}
	return evs
}

// events.Read must hand back exactly what was written, in the order it was
// written -- reordering is Scan's job to be indifferent to, not Read's job to
// perform. Pinning this first makes the Scan assertions below a claim about
// Scan, not an accident of a reader that happened to sort.
func TestScanScrambledOrderReadPreservesFileOrder(t *testing.T) {
	evs := scrambledOrderFixture(t, events.Path(t.TempDir()))
	if got, want := len(evs), 4; got != want {
		t.Fatalf("events.Read returned %d events, want %d", got, want)
	}
	wantKinds := []string{events.KindRunEnd, events.KindTool, events.KindRunStart, events.KindTool}
	for i, want := range wantKinds {
		if evs[i].Kind != want {
			t.Fatalf("evs[%d].Kind = %v, want %v (file order must survive the read)", i, evs[i].Kind, want)
		}
	}
}

// Steps counts every tool call regardless of where in the file it sits.
func TestScanScrambledOrderStepCountIsUnaffected(t *testing.T) {
	evs := scrambledOrderFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Session.Steps, 2; got != want {
		t.Errorf("cards[0].Session.Steps = %d, want %d", got, want)
	}
}

// Started and Elapsed come from the minimum and maximum timestamp seen, not
// from the first and last line in the file -- the file's first line here
// carries the newest timestamp and its last line the oldest.
func TestScanScrambledOrderTimingComesFromTimestampsNotFilePosition(t *testing.T) {
	evs := scrambledOrderFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Started, base; !got.Equal(want) {
		t.Errorf("cards[0].Session.Started = %s, want %s (the oldest timestamp, even though it is the third line)", got, want)
	}
	if got, want := cards[0].Session.Elapsed(), 3*time.Minute; got != want {
		t.Errorf("cards[0].Session.Elapsed() = %s, want %s", got, want)
	}
	if !cards[0].Session.Done {
		t.Error("cards[0].Session.Done = false, want true: the run-end is present, regardless of it being the first line written")
	}
}

// Activity is the newest thing the agent said BY TIMESTAMP, not the last tool
// or say event physically written to the file. Here the tool call with the
// later timestamp ("Edit ...") was written before the one with the earlier
// timestamp ("Read ..."), so a file-position-based reader would report the
// wrong one.
func TestScanScrambledOrderActivityComesFromNewestTimestampNotLastLine(t *testing.T) {
	evs := scrambledOrderFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Activity, "Edit internal/web/cards.go"; got != want {
		t.Errorf("cards[0].Session.Activity = %q, want %q (the tool call at the later timestamp, even though it was written earlier in the file)", got, want)
	}
}
