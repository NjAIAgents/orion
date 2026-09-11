package web

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
)

// Card.Run must name the exact run it summarises -- without it, a click on
// a card has no way to ask /api/detail for the right run when a ticket has
// been worked more than once (cards.go's own reasoning for grouping by
// (key, run) rather than by key alone).
func TestCardCarriesItsOwnRunID(t *testing.T) {
	cards := Scan([]events.Event{
		ev(0, events.KindRunStart, "OR-1", "r1"),
		ev(1, events.KindRunEnd, "OR-1", "r1"),
		ev(2, events.KindRunStart, "OR-1", "r2"),
		ev(3, events.KindRunEnd, "OR-1", "r2"),
	}, nil)
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(cards))
	}
	if cards[0].Run != "r1" {
		t.Errorf("cards[0].Run = %q, want r1", cards[0].Run)
	}
	if cards[1].Run != "r2" {
		t.Errorf("cards[1].Run = %q, want r2", cards[1].Run)
	}
}
