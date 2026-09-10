package web

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// Same Latest, same Key, different Runs: the tie breaks on Run ascending, and
// that order must hold across repeated scans -- not just be right once by
// coincidence of map iteration.
func TestCardsWithSameLatestAndKeyKeepStableOrderAcrossMultipleScans(t *testing.T) {
	evs := []events.Event{
		{At: at(7), Kind: events.KindSay, Key: "OR-9", Run: "c"},
		{At: at(7), Kind: events.KindSay, Key: "OR-9", Run: "a"},
		{At: at(7), Kind: events.KindSay, Key: "OR-9", Run: "b"},
	}

	want := []string{"a", "b", "c"}
	for i := 0; i < 20; i++ {
		got := Scan(evs)
		if len(got) != 3 {
			t.Fatalf("scan %d: got %d cards, want 3: %+v", i, len(got), got)
		}
		var runs []string
		for _, c := range got {
			runs = append(runs, c.Run)
		}
		for j := range want {
			if runs[j] != want[j] {
				t.Fatalf("scan %d: order = %v, want %v (run ascending, every time)", i, runs, want)
			}
		}
	}
}

// A run that logs thousands of lines is still one run: the step count must
// track the event count exactly, not saturate, round, or drift at scale.
func TestVeryLargeNumberOfEventsForOnePairProducesExactStepCount(t *testing.T) {
	const n = 50000
	evs := make([]events.Event, 0, n)
	for i := 0; i < n; i++ {
		evs = append(evs, events.Event{At: at(0).Add(time.Duration(i) * time.Millisecond), Kind: events.KindSay, Key: "OR-1", Run: "r1"})
	}

	got := Scan(evs)
	if len(got) != 1 {
		t.Fatalf("got %d cards, want 1: %+v", len(got), got)
	}
	if got[0].Steps != n {
		t.Errorf("steps = %d, want %d: the count must not drift at scale", got[0].Steps, n)
	}
}

// Timestamps at the extremes of what time.Time can represent must survive
// Scan exactly -- neither clamped to some default nor overflowed into a
// nonsensical or reordered value.
func TestExtremeTimestampsAreHandledWithoutOverflowOrTruncation(t *testing.T) {
	veryOld := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	veryNew := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)

	got := Scan([]events.Event{
		{At: veryOld, Kind: events.KindSay, Key: "OR-1", Run: "old"},
		{At: veryNew, Kind: events.KindSay, Key: "OR-2", Run: "new"},
	})

	if len(got) != 2 {
		t.Fatalf("got %d cards, want 2: %+v", len(got), got)
	}
	// Newest first: the far-future run leads, the far-past run trails.
	if got[0].Run != "new" || got[1].Run != "old" {
		t.Fatalf("order = [%s %s], want [new old]", got[0].Run, got[1].Run)
	}
	if !got[0].Latest.Equal(veryNew) {
		t.Errorf("new card latest = %v, want %v exactly", got[0].Latest, veryNew)
	}
	if !got[1].Latest.Equal(veryOld) {
		t.Errorf("old card latest = %v, want %v exactly", got[1].Latest, veryOld)
	}
}

// An event missing Run names no run, so it is attributable to no card --
// even when it shares a Key with a real run, it must not inflate that run's
// step count just because the key matches.
func TestEventMissingRunIsNotIncludedInAnyCardOrStepCount(t *testing.T) {
	got := Scan([]events.Event{
		{At: at(1), Kind: events.KindClaimed, Key: "OR-57", Run: "r1"},
		{At: at(2), Kind: events.KindNote, Key: "OR-57"}, // no Run
	})

	if len(got) != 1 {
		t.Fatalf("got %d cards, want 1: %+v", len(got), got)
	}
	if got[0].Steps != 1 {
		t.Errorf("steps = %d, want 1: the run-less event must not count", got[0].Steps)
	}
}

// A single (key, run) pair driven by many events, arriving out of order and
// interleaved with other pairs, must still resolve to exactly one card --
// never a duplicate, however the events are scattered through the log.
func TestEachKeyRunPairAppearsExactlyOnceRegardlessOfEventCountOrOrdering(t *testing.T) {
	var evs []events.Event
	pairs := [][2]string{{"OR-1", "r1"}, {"OR-2", "r1"}, {"OR-1", "r2"}}
	// Ten events per pair, interleaved round-robin so no pair's events are
	// contiguous, and with timestamps that don't move monotonically with
	// insertion order.
	for round := 0; round < 10; round++ {
		for pi, p := range pairs {
			sec := (round*len(pairs) + (len(pairs) - pi)) % 30
			evs = append(evs, events.Event{At: at(sec), Kind: events.KindSay, Key: p[0], Run: p[1]})
		}
	}

	got := Scan(evs)
	if len(got) != len(pairs) {
		t.Fatalf("got %d cards, want %d (one per distinct pair): %+v", len(got), len(pairs), got)
	}
	seen := map[[2]string]int{}
	for _, c := range got {
		seen[[2]string{c.Key, c.Run}]++
	}
	for _, p := range pairs {
		if seen[p] != 1 {
			t.Errorf("pair %v appeared %d times, want exactly 1", p, seen[p])
		}
	}
}
