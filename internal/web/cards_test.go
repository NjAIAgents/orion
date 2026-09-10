package web

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// ev is one line of a run's log, at an offset from base (declared in
// timing_test.go, and shared so both files date their fixtures alike).
func ev(d time.Duration, kind, key, run string) events.Event {
	return events.Event{At: base.Add(d), Kind: kind, Key: key, Run: run}
}

// writeLog writes a fixture events.jsonl through the real writer, so the test
// reads back the format Orion actually emits rather than one hand-rolled here.
func writeLog(t *testing.T, evs []events.Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(path, events.Event{})
	if err != nil {
		t.Fatalf("open fixture log: %v", err)
	}
	for _, e := range evs {
		log.Emit(e)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close fixture log: %v", err)
	}
	return path
}

// OR-57's done-when, end to end: a log on disk becomes one card per (key, run)
// pair, each carrying the step count, the latest activity and the model.
func TestScanDerivesACardPerRunFromAFixtureLog(t *testing.T) {
	path := writeLog(t, []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-57", Run: "r1", Model: "opus"},
		{At: base.Add(time.Minute), Kind: events.KindTool, Key: "OR-57", Run: "r1", Model: "opus", Msg: "Read internal/web/model.go"},
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-57", Run: "r1", Model: "opus", Msg: "Edit internal/web/cards.go"},
		{At: base.Add(3 * time.Minute), Kind: events.KindRunEnd, Key: "OR-57", Run: "r1"},

		{At: base.Add(4 * time.Minute), Kind: events.KindRunStart, Key: "OR-58", Run: "r2", Model: "haiku"},
		{At: base.Add(5 * time.Minute), Kind: events.KindTool, Key: "OR-58", Run: "r2", Model: "haiku", Msg: "Grep timing"},
	})

	evs, err := events.Read(path)
	if err != nil {
		t.Fatalf("read fixture log: %v", err)
	}
	cards := Scan(evs)

	if got, want := len(cards), 2; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}

	first, second := cards[0], cards[1]
	if got, want := first.Key, "OR-57"; got != want {
		t.Errorf("cards[0].Key = %q, want %q", got, want)
	}
	if got, want := first.Session.Steps, 2; got != want {
		t.Errorf("cards[0].Session.Steps = %d, want %d", got, want)
	}
	if got, want := first.Session.Activity, "Edit internal/web/cards.go"; got != want {
		t.Errorf("cards[0].Session.Activity = %q, want %q", got, want)
	}
	if got, want := first.Session.Model, "opus"; got != want {
		t.Errorf("cards[0].Session.Model = %q, want %q", got, want)
	}
	if !first.Session.Done {
		t.Error("cards[0].Session.Done = false, want true: the log carries run-end")
	}
	if got, want := first.Session.Elapsed(), 3*time.Minute; got != want {
		t.Errorf("cards[0].Session.Elapsed() = %s, want %s", got, want)
	}

	if got, want := second.Key, "OR-58"; got != want {
		t.Errorf("cards[1].Key = %q, want %q", got, want)
	}
	if got, want := second.Session.Steps, 1; got != want {
		t.Errorf("cards[1].Session.Steps = %d, want %d", got, want)
	}
	if got, want := second.Session.Model, "haiku"; got != want {
		t.Errorf("cards[1].Session.Model = %q, want %q", got, want)
	}
	if second.Session.Done {
		t.Error("cards[1].Session.Done = true, want false: that run has no run-end")
	}
}

// The grouping decision itself. A ticket worked twice -- an implementer, then
// devops after a red build -- is two runs, and folding them into one card
// yields a card describing neither: four steps spanning both, started at the
// first and still running because the second has no end.
func TestScanSplitsOneKeyIntoOneCardPerRun(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
		ev(2*time.Minute, events.KindRunEnd, "OR-57", "r1"),

		ev(3*time.Minute, events.KindRunStart, "OR-57", "r2"),
		ev(4*time.Minute, events.KindTool, "OR-57", "r2"),
		ev(5*time.Minute, events.KindTool, "OR-57", "r2"),
	})

	if got, want := len(cards), 2; got != want {
		t.Fatalf("two runs of one key gave %d cards, want %d", got, want)
	}
	for i, card := range cards {
		if got, want := card.Key, "OR-57"; got != want {
			t.Errorf("cards[%d].Key = %q, want %q", i, got, want)
		}
	}
	if got, want := cards[0].Session.Steps, 1; got != want {
		t.Errorf("first run's Steps = %d, want %d (the second run's steps are not its own)", got, want)
	}
	if got, want := cards[1].Session.Steps, 2; got != want {
		t.Errorf("second run's Steps = %d, want %d", got, want)
	}
	if !cards[0].Session.Done || cards[1].Session.Done {
		t.Errorf("Done = %v, %v; want true, false -- the first run ended, the second is still going",
			cards[0].Session.Done, cards[1].Session.Done)
	}
}

// The other half of the pair: a run id is unique within a key, not across the
// tree, so two keys sharing one must not collapse into a single card.
func TestScanKeepsTwoKeysApartWhenTheyShareARunID(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindTool, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-58", "r1"),
	})

	if got, want := len(cards), 2; got != want {
		t.Fatalf("two keys sharing run %q gave %d cards, want %d", "r1", got, want)
	}
	if cards[0].Key == cards[1].Key {
		t.Fatalf("both cards are %q: the key is half the grouping", cards[0].Key)
	}
}

// A step is a tool call. Counting every event instead would rank a run that
// talked a lot above one that worked.
func TestScanCountsToolCallsAsStepsAndNothingElse(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindSay, "OR-57", "r1"),
		ev(2*time.Minute, events.KindTool, "OR-57", "r1"),
		ev(3*time.Minute, events.KindCommit, "OR-57", "r1"),
		ev(4*time.Minute, events.KindStage, "OR-57", "r1"),
		ev(5*time.Minute, events.KindTool, "OR-57", "r1"),
		ev(6*time.Minute, events.KindRunEnd, "OR-57", "r1"),
	})

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Session.Steps, 2; got != want {
		t.Errorf("Steps = %d, want %d: only the two tool calls are steps", got, want)
	}
}

// "What it is doing right now" is the newest thing the AGENT did or said. A
// run-end or a CI verdict is Orion reporting on the run, and a card that
// showed one would say the agent is doing something it is not.
func TestScanTakesActivityFromTheAgentsNewestOwnLine(t *testing.T) {
	first := ev(time.Minute, events.KindTool, "OR-57", "r1")
	first.Msg = "Read internal/web/model.go"
	said := ev(2*time.Minute, events.KindSay, "OR-57", "r1")
	said.Msg = "writing the card derivation"
	ended := ev(3*time.Minute, events.KindRunEnd, "OR-57", "r1")
	ended.Msg = "exit 0"

	cards := Scan([]events.Event{ev(0, events.KindRunStart, "OR-57", "r1"), first, said, ended})
	if got, want := cards[0].Session.Activity, "writing the card derivation"; got != want {
		t.Errorf("Activity = %q, want %q", got, want)
	}
}

// A run that has neither acted nor spoken says nothing, rather than borrowing
// a message from an event that is not its own words.
func TestScanLeavesActivityEmptyWhenTheAgentHasNotSpoken(t *testing.T) {
	started := ev(0, events.KindRunStart, "OR-57", "r1")
	started.Msg = "session open: 12 tools"

	cards := Scan([]events.Event{started})
	if got := cards[0].Session.Activity; got != "" {
		t.Errorf("Activity = %q, want empty", got)
	}
}

// The model on the card is what is running now: a run that fell back after a
// capacity error carries both, and the later one is the answer. An event with
// no model at all -- most kinds -- must not clear it.
func TestScanTakesTheNewestModelAndSilenceDoesNotClearIt(t *testing.T) {
	start := ev(0, events.KindRunStart, "OR-57", "r1")
	start.Model = "opus"
	fell := ev(time.Minute, events.KindTool, "OR-57", "r1")
	fell.Model = "sonnet"

	cards := Scan([]events.Event{start, fell, ev(2*time.Minute, events.KindCommit, "OR-57", "r1")})
	if got, want := cards[0].Session.Model, "sonnet"; got != want {
		t.Errorf("Model = %q, want %q", got, want)
	}
}

// File order is not time order. A log stitched back together after a rotation,
// or written by two goroutines, can carry an older line last -- and the newest
// EVENT is what the card reports, not the newest line.
func TestScanReadsActivityAndModelByTimestampNotByFilePosition(t *testing.T) {
	newer := ev(2*time.Minute, events.KindTool, "OR-57", "r1")
	newer.Msg, newer.Model = "Edit internal/web/cards.go", "sonnet"
	older := ev(time.Minute, events.KindTool, "OR-57", "r1")
	older.Msg, older.Model = "Read internal/web/model.go", "opus"

	cards := Scan([]events.Event{newer, older})
	if got, want := cards[0].Session.Activity, "Edit internal/web/cards.go"; got != want {
		t.Errorf("Activity = %q, want %q", got, want)
	}
	if got, want := cards[0].Session.Model, "sonnet"; got != want {
		t.Errorf("Model = %q, want %q", got, want)
	}
}

// Supervisor lines are emitted before any ticket is claimed. They are real
// events, but there is no ticket to draw them on, and a card keyed on the
// empty string is a card nobody can click.
func TestScanSkipsEventsThatBelongToNoTicket(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindNote, "", ""),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
	})

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Key, "OR-57"; got != want {
		t.Errorf("cards[0].Key = %q, want %q", got, want)
	}
}

// The grid is diffed between refreshes, so the order is a total one: by key,
// then by when the run started, then by run id. Map iteration order would make
// the same log draw a different grid every time.
func TestScanOrdersCardsByKeyThenStart(t *testing.T) {
	evs := []events.Event{
		ev(5*time.Minute, events.KindTool, "OR-58", "b"),
		ev(3*time.Minute, events.KindTool, "OR-57", "later"),
		ev(time.Minute, events.KindTool, "OR-57", "earlier"),
		ev(4*time.Minute, events.KindTool, "OR-58", "a"),
	}

	want := []struct {
		key     string
		started time.Duration
	}{
		{"OR-57", time.Minute},
		{"OR-57", 3 * time.Minute},
		{"OR-58", 4 * time.Minute},
		{"OR-58", 5 * time.Minute},
	}

	for attempt := 0; attempt < 5; attempt++ {
		cards := Scan(evs)
		if got := len(cards); got != len(want) {
			t.Fatalf("Scan returned %d cards, want %d", got, len(want))
		}
		for i, w := range want {
			if got := cards[i].Key; got != w.key {
				t.Fatalf("attempt %d: cards[%d].Key = %q, want %q", attempt, i, got, w.key)
			}
			if got := cards[i].Session.Started; !got.Equal(base.Add(w.started)) {
				t.Fatalf("attempt %d: cards[%d].Session.Started = %s, want %s",
					attempt, i, got, base.Add(w.started))
			}
		}
	}
}

// Two runs of one key that started at the same instant still need an order, or
// the grid reshuffles between two identical scans. The run id breaks the tie.
func TestScanBreaksAStartedTieOnTheRunID(t *testing.T) {
	// Both runs start at the same instant; alpha takes two steps to zulu's one,
	// so the step count says which card the tie-break put first.
	evs := []events.Event{
		ev(0, events.KindTool, "OR-57", "zulu"),
		ev(0, events.KindTool, "OR-57", "alpha"),
		ev(0, events.KindTool, "OR-57", "alpha"),
	}

	for attempt := 0; attempt < 5; attempt++ {
		cards := Scan(evs)
		if got, want := len(cards), 2; got != want {
			t.Fatalf("Scan returned %d cards, want %d", got, want)
		}
		if got, want := cards[0].Session.Steps, 2; got != want {
			t.Fatalf("attempt %d: cards[0].Session.Steps = %d, want %d -- run %q sorts before %q",
				attempt, got, want, "alpha", "zulu")
		}
	}
}

// An empty log is a machine where nothing has run, which is a normal state and
// not an error: no cards, no panic.
func TestScanOnAnEmptyLogReturnsNoCards(t *testing.T) {
	if got := Scan(nil); len(got) != 0 {
		t.Errorf("Scan(nil) returned %d cards, want 0", len(got))
	}
}
