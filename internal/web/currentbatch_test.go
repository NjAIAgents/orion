package web

import (
	"testing"
	"time"
)

// A card whose key is currently live survives regardless of whether it is
// Done -- the ordinary shape of a run still in progress.
func TestCurrentBatchKeepsALiveCard(t *testing.T) {
	now := time.Now()
	cards := []Card{{Key: "OR-1", Session: Session{Done: false}}}
	got := CurrentBatch(cards, map[string]bool{"OR-1": true}, now)
	if len(got) != 1 {
		t.Fatalf("cards = %d, want 1 (live key must survive)", len(got))
	}
}

// A finished run stays visible for GraceAfterFinish -- the "2 done" cards
// the mockup shows immediately after a run completes.
func TestCurrentBatchKeepsARecentlyFinishedCard(t *testing.T) {
	now := time.Now()
	cards := []Card{{Key: "OR-1", Session: Session{
		Done: true, Last: now.Add(-30 * time.Second),
	}}}
	got := CurrentBatch(cards, nil, now)
	if len(got) != 1 {
		t.Fatalf("cards = %d, want 1 (within the grace window)", len(got))
	}
}

// THE CASE THIS EXISTS TO FIX (OR-435): a finished run from well outside the
// grace window, with no live session, is history -- not the current batch.
// This is the 89-card gap between the run page's real behaviour (95 cards,
// weeks of history) and the mockup's 6.
func TestCurrentBatchDropsAnOldFinishedCard(t *testing.T) {
	now := time.Now()
	cards := []Card{{Key: "OR-1", Session: Session{
		Done: true, Last: now.Add(-3 * time.Hour),
	}}}
	got := CurrentBatch(cards, nil, now)
	if len(got) != 0 {
		t.Fatalf("cards = %d, want 0 (well outside the grace window, not live)", len(got))
	}
}

// A run that stopped without finishing (not Done, not live) is exactly
// OR-52's "stopped" case -- not part of what's running now, however
// recently it stopped.
func TestCurrentBatchDropsAStoppedUnfinishedCard(t *testing.T) {
	now := time.Now()
	cards := []Card{{Key: "OR-1", Session: Session{
		Done: false, Last: now.Add(-10 * time.Second),
	}}}
	got := CurrentBatch(cards, nil, now)
	if len(got) != 0 {
		t.Fatalf("cards = %d, want 0 (stopped, not live, and never finished)", len(got))
	}
}

// The grace window boundary itself: exactly at GraceAfterFinish still
// counts (<=, not <), one tick past it does not.
func TestCurrentBatchGraceWindowBoundary(t *testing.T) {
	now := time.Now()
	atBoundary := []Card{{Key: "OR-1", Session: Session{
		Done: true, Last: now.Add(-GraceAfterFinish),
	}}}
	if got := CurrentBatch(atBoundary, nil, now); len(got) != 1 {
		t.Errorf("at the boundary: cards = %d, want 1", len(got))
	}

	pastBoundary := []Card{{Key: "OR-1", Session: Session{
		Done: true, Last: now.Add(-GraceAfterFinish - time.Second),
	}}}
	if got := CurrentBatch(pastBoundary, nil, now); len(got) != 0 {
		t.Errorf("past the boundary: cards = %d, want 0", len(got))
	}
}

// A card with no session at all -- the zero value, Done false and Last the
// zero time -- must not be kept by a zero-time coincidence: now.Sub(zero)
// is enormous, not "just finished". Written down because a naive
// !c.Session.Last.IsZero() omission is the one-line bug that would let
// every session-less card in the log back in wholesale.
func TestCurrentBatchDropsACardWithNoSessionAtAll(t *testing.T) {
	now := time.Now()
	cards := []Card{{Key: "OR-1"}}
	got := CurrentBatch(cards, nil, now)
	if len(got) != 0 {
		t.Fatalf("cards = %d, want 0 (zero-value session is not live and not recently finished)", len(got))
	}
}

// Multiple cards: the filter is per-card, not all-or-nothing across the
// batch -- proven with one of each shape in a single call.
func TestCurrentBatchFiltersEachCardIndependently(t *testing.T) {
	now := time.Now()
	cards := []Card{
		{Key: "OR-1", Session: Session{Done: false}},                                  // live
		{Key: "OR-2", Session: Session{Done: true, Last: now.Add(-time.Minute)}},      // just finished
		{Key: "OR-3", Session: Session{Done: true, Last: now.Add(-24 * time.Hour)}},   // old history
		{Key: "OR-4", Session: Session{Done: false, Last: now.Add(-5 * time.Second)}}, // stopped
	}
	live := map[string]bool{"OR-1": true}
	got := CurrentBatch(cards, live, now)
	if len(got) != 2 {
		t.Fatalf("cards = %d, want 2 (OR-1 live, OR-2 just finished)", len(got))
	}
	keys := map[string]bool{got[0].Key: true, got[1].Key: true}
	if !keys["OR-1"] || !keys["OR-2"] {
		t.Errorf("got keys %v, want OR-1 and OR-2", keys)
	}
}
