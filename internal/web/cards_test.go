package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

func at(sec int) time.Time {
	return time.Date(2026, 9, 9, 13, 31, sec, 0, time.UTC)
}

// fixtureLog writes lines to a file and reads them back through the real
// parser, so the test exercises the field names the log actually carries
// rather than a struct built in Go that can drift from them.
func fixtureLog(t *testing.T, lines string) []events.Event {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(p, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	evs, err := events.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// The done-when, read off a file: one card per (key, run), each carrying its
// step count, its latest activity and its model.
func TestScanReturnsOneCardPerKeyAndRun(t *testing.T) {
	evs := fixtureLog(t, `
{"at":"2026-09-09T13:31:00Z","kind":"claimed","actor":"orion","key":"OR-183","run":"r1","msg":"claimed"}
{"at":"2026-09-09T13:31:04Z","kind":"run-start","actor":"implementer","key":"OR-183","run":"r1","model":"opus","msg":"started"}
{"at":"2026-09-09T13:31:09Z","kind":"run-end","actor":"implementer","key":"OR-183","run":"r1","model":"opus","msg":"exit 0"}
{"at":"2026-09-09T13:31:02Z","kind":"claimed","actor":"orion","key":"OR-266","run":"r2","msg":"claimed"}
{"at":"2026-09-09T13:31:06Z","kind":"say","actor":"qa","key":"OR-266","run":"r2","model":"sonnet","msg":"reading the diff"}
`)

	got := Scan(evs)
	want := []Card{
		{Key: "OR-183", Run: "r1", Steps: 3, Latest: at(9), Model: "opus"},
		{Key: "OR-266", Run: "r2", Steps: 2, Latest: at(6), Model: "sonnet"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d cards, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("card %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The grouping decision itself. Two attempts at one ticket are two runs, and
// collapsing them would report a card whose step count belongs to neither.
func TestTwoRunsOfOneTicketAreTwoCards(t *testing.T) {
	got := Scan([]events.Event{
		{At: at(1), Kind: events.KindRunStart, Key: "OR-290", Run: "r1", Model: "haiku"},
		{At: at(2), Kind: events.KindRunEnd, Key: "OR-290", Run: "r1", Model: "haiku"},
		{At: at(3), Kind: events.KindRunStart, Key: "OR-290", Run: "r2", Model: "opus"},
	})

	if len(got) != 2 {
		t.Fatalf("got %d cards, want 2 (one per run): %+v", len(got), got)
	}
	if got[0].Run != "r2" || got[0].Steps != 1 || got[0].Model != "opus" {
		t.Errorf("newest card = %+v, want run r2, 1 step, opus", got[0])
	}
	if got[1].Run != "r1" || got[1].Steps != 2 || got[1].Model != "haiku" {
		t.Errorf("older card = %+v, want run r1, 2 steps, haiku", got[1])
	}
}

// A ticket is worked by several models, so the card reports the most recent
// one named -- and a line that names none (a CI verdict, an escalation) must
// not blank it, which would read as a run with no agent.
func TestModelIsTheLatestOneNamedAndSurvivesEventsWithoutOne(t *testing.T) {
	got := Scan([]events.Event{
		{At: at(1), Kind: events.KindRunStart, Key: "OR-1", Run: "r1", Model: "haiku"},
		{At: at(2), Kind: events.KindRunStart, Key: "OR-1", Run: "r1", Model: "opus"},
		{At: at(3), Kind: events.KindCI, Key: "OR-1", Run: "r1", Actor: events.ActorCI},
	})

	if len(got) != 1 {
		t.Fatalf("got %d cards, want 1: %+v", len(got), got)
	}
	if got[0].Model != "opus" {
		t.Errorf("model = %q, want opus (the latest one named)", got[0].Model)
	}
	if got[0].Latest != at(3) {
		t.Errorf("latest = %v, want %v: the CI line is still activity", got[0].Latest, at(3))
	}
	if got[0].Steps != 3 {
		t.Errorf("steps = %d, want 3", got[0].Steps)
	}
}

// Supervisor-level lines name no ticket or no run. They are attributable to
// no card, and inventing one for them would put a run on the grid that nobody
// ever started.
func TestEventsWithoutAKeyOrARunAreNotCards(t *testing.T) {
	got := Scan([]events.Event{
		{At: at(1), Kind: events.KindNote, Actor: events.ActorOrion, Msg: "queue swept"},
		{At: at(2), Kind: events.KindNote, Key: "OR-1", Msg: "no run id"},
		{At: at(3), Kind: events.KindNote, Run: "r1", Msg: "no ticket"},
		{At: at(4), Kind: events.KindClaimed, Key: "OR-1", Run: "r1"},
	})

	if len(got) != 1 {
		t.Fatalf("got %d cards, want 1: %+v", len(got), got)
	}
	if got[0].Steps != 1 {
		t.Errorf("steps = %d, want 1: only the attributable event counts", got[0].Steps)
	}
}

// Cards lead with what is happening now, and a card is dated by its newest
// event rather than by whichever line sits last in the file.
func TestCardsAreOrderedByLatestActivityWhateverTheFileOrder(t *testing.T) {
	got := Scan([]events.Event{
		{At: at(5), Kind: events.KindSay, Key: "OR-2", Run: "r2"},
		{At: at(9), Kind: events.KindSay, Key: "OR-3", Run: "r3"},
		{At: at(1), Kind: events.KindSay, Key: "OR-1", Run: "r1"},
		{At: at(2), Kind: events.KindSay, Key: "OR-3", Run: "r3"}, // out of order, older
	})

	var keys []string
	for _, c := range got {
		keys = append(keys, c.Key)
	}
	if len(keys) != 3 || keys[0] != "OR-3" || keys[1] != "OR-2" || keys[2] != "OR-1" {
		t.Fatalf("order = %v, want [OR-3 OR-2 OR-1] (newest activity first)", keys)
	}
	if got[0].Latest != at(9) {
		t.Errorf("OR-3 latest = %v, want %v: the newer event dates the card", got[0].Latest, at(9))
	}
}

// A total order, so two scans of the same log never reshuffle the grid.
func TestCardsWithTheSameLatestActivityHaveAStableOrder(t *testing.T) {
	evs := []events.Event{
		{At: at(1), Kind: events.KindSay, Key: "OR-2", Run: "b"},
		{At: at(1), Kind: events.KindSay, Key: "OR-1", Run: "b"},
		{At: at(1), Kind: events.KindSay, Key: "OR-1", Run: "a"},
	}
	first := Scan(evs)
	if len(first) != 3 {
		t.Fatalf("got %d cards, want 3", len(first))
	}
	if first[0].Key != "OR-1" || first[0].Run != "a" || first[2].Key != "OR-2" {
		t.Errorf("order = %+v, want OR-1/a, OR-1/b, OR-2/b", first)
	}
	for i, c := range Scan(evs) {
		if c != first[i] {
			t.Fatalf("second scan differs at %d: %+v vs %+v", i, c, first[i])
		}
	}
}
