package web

import (
	"testing"
	"time"
)

// Card.Status is a plain string, not one of internal/ui's constants (see the
// comment on the field in model.go) -- so the type must accept any of the
// five verbs the renderer actually branches on, not just one.
func TestCardStatusHoldsAnyOfTheFiveVerbs(t *testing.T) {
	for _, want := range []string{"ok", "working", "waiting", "warning", "failed"} {
		card := Card{Status: want}
		if card.Status != want {
			t.Errorf("Card.Status = %q, want %q", card.Status, want)
		}
	}
}

// Sessions is a history, oldest first, and the card renders "3 steps" out of
// that order -- so the slice must preserve insertion order rather than the
// type silently permitting a reordering.
func TestCardSessionsPreservesInsertionOrder(t *testing.T) {
	first := Session{Actor: "implementer"}
	second := Session{Actor: "qa"}
	third := Session{Actor: "ci"}

	card := Card{Sessions: []Session{first, second, third}}

	if len(card.Sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(card.Sessions))
	}
	if card.Sessions[0].Actor != "implementer" || card.Sessions[1].Actor != "qa" || card.Sessions[2].Actor != "ci" {
		t.Errorf("sessions = %+v, want implementer, qa, ci in that order", card.Sessions)
	}
}

// Model empty means the event carried none and the actor's default applies --
// a display fallback the renderer resolves, not a claim this type should
// forbid.
func TestSessionModelCanBeEmpty(t *testing.T) {
	s := Session{Actor: "implementer", Model: ""}
	if s.Model != "" {
		t.Errorf("Session.Model = %q, want empty", s.Model)
	}
}

// Activity is only meaningful while Done is false; a finished or not-yet-
// started session must still be representable with no present-tense line.
func TestSessionActivityCanBeEmpty(t *testing.T) {
	s := Session{Actor: "implementer", Done: true, Activity: ""}
	if s.Activity != "" {
		t.Errorf("Session.Activity = %q, want empty", s.Activity)
	}
}

// A session that has just started has run for zero time -- the zero value,
// not a sentinel the renderer has to special-case.
func TestSessionElapsedCanBeZero(t *testing.T) {
	s := Session{Actor: "implementer", Elapsed: 0}
	if s.Elapsed != 0 {
		t.Errorf("Session.Elapsed = %v, want 0", s.Elapsed)
	}
	if s.Elapsed != time.Duration(0) {
		t.Errorf("Session.Elapsed = %v, want the zero Duration", s.Elapsed)
	}
}

// Done is the field the finished/running branch turns on, and the two
// sessions in a history must be able to disagree about it independently of
// everything else they carry.
func TestSessionDoneIndependentPerSession(t *testing.T) {
	running := Session{Actor: "implementer", Done: false}
	finished := Session{Actor: "implementer", Done: true}

	if running.Done {
		t.Error("running session reported Done")
	}
	if !finished.Done {
		t.Error("finished session did not report Done")
	}
}
