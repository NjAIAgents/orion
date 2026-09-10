package web

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// QA coverage for OR-57's grouping contract in isolation from the rest of the
// derivation (steps/activity/model), one dedicated case per rule.

// One event, one card: the smallest input the grouping has to handle.
func TestScanOneEventForOneKeyRunProducesOneCard(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindTool, "OR-57", "r1"),
	})

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Key, "OR-57"; got != want {
		t.Errorf("cards[0].Key = %q, want %q", got, want)
	}
}

// Several events on the same (key, run) fold into one card, not one per event.
func TestScanMultipleEventsForSameKeyRunProduceOneCard(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
		ev(2*time.Minute, events.KindTool, "OR-57", "r1"),
		ev(3*time.Minute, events.KindRunEnd, "OR-57", "r1"),
	})

	if got, want := len(cards), 1; got != want {
		t.Fatalf("four events on one (key, run) gave %d cards, want %d", got, want)
	}
}

// Same key, different runs: the run half of the pair earns its own card.
func TestScanSameKeyDifferentRunsProduceSeparateCards(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindTool, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r2"),
	})

	if got, want := len(cards), 2; got != want {
		t.Fatalf("one key with two runs gave %d cards, want %d", got, want)
	}
	if cards[0].Key != "OR-57" || cards[1].Key != "OR-57" {
		t.Fatalf("cards[0].Key, cards[1].Key = %q, %q, want both %q", cards[0].Key, cards[1].Key, "OR-57")
	}
}

// Different keys sharing one run ID: the key half of the pair earns its own
// card, since a run ID is only unique within a key, not across the tree.
func TestScanDifferentKeysSameRunIDProduceSeparateCards(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindTool, "OR-57", "shared"),
		ev(time.Minute, events.KindTool, "OR-58", "shared"),
	})

	if got, want := len(cards), 2; got != want {
		t.Fatalf("two keys sharing a run ID gave %d cards, want %d", got, want)
	}
	if cards[0].Key == cards[1].Key {
		t.Fatalf("both cards keyed %q: a shared run ID must not collapse different keys", cards[0].Key)
	}
}

// No duplicates: re-scanning the same log twice must not double the count,
// and no (key, run) pair should ever appear on more than one card.
func TestScanProducesNoDuplicateCardsForTheSamePair(t *testing.T) {
	evs := []events.Event{
		ev(0, events.KindRunStart, "OR-57", "r1"),
		ev(time.Minute, events.KindTool, "OR-57", "r1"),
		ev(2*time.Minute, events.KindTool, "OR-58", "r2"),
	}

	cards := Scan(evs)
	if got, want := len(cards), 2; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}

	seen := map[[2]string]bool{}
	for _, c := range cards {
		for _, e := range evs {
			if e.Key != c.Key {
				continue
			}
			pair := [2]string{c.Key, e.Run}
			if seen[pair] {
				continue
			}
			seen[pair] = true
		}
	}
	if got, want := len(seen), 2; got != want {
		t.Fatalf("distinct (key, run) pairs observed on cards = %d, want %d (a duplicate slipped through)", got, want)
	}
}
