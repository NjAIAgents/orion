package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

func TestSessionElapsedIsTheSpanBetweenFirstAndLastEvent(t *testing.T) {
	start := time.Date(2026, 9, 9, 13, 31, 4, 0, time.UTC)
	s := Session{Started: start, Last: start.Add(4*time.Minute + 12*time.Second)}
	if got, want := s.Elapsed(), 4*time.Minute+12*time.Second; got != want {
		t.Errorf("Elapsed() = %s, want %s", got, want)
	}
}

// A session that has not started yet reports nothing, not the span since the
// zero time -- which is the number a stored-and-defaulted field would give.
func TestSessionElapsedIsZeroWhenThereIsNoSession(t *testing.T) {
	if got := (Session{}).Elapsed(); got != 0 {
		t.Errorf("Elapsed() on the zero Session = %s, want 0", got)
	}
}

// Out-of-order timestamps are a log problem; a negative duration on a card is
// a rendering bug wearing a measurement's clothes.
func TestSessionElapsedNeverGoesNegative(t *testing.T) {
	start := time.Date(2026, 9, 9, 13, 31, 4, 0, time.UTC)
	s := Session{Started: start, Last: start.Add(-time.Minute)}
	if got := s.Elapsed(); got != 0 {
		t.Errorf("Elapsed() with Last before Started = %s, want 0", got)
	}
}

// OR-55's done-when, enforced rather than stated.
//
// The model is derived from the event log and nothing else: the same log must
// produce the same snapshot on every replay, which it stops doing the moment
// this file opens a file, dials a socket or reads a clock. Those are the three
// ways the property is lost, and each of them looks locally reasonable in a
// diff -- a helper that "just" stats the log, an Elapsed that "just" uses
// time.Now for a running session. That last one is the likely one: it makes a
// live card tick, and it makes a finished run report a different duration
// every time anybody opens the page.
func TestModelDeclaresTypesWithNoIOAndNoClock(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Import paths that mean this file left its own memory.
	banned := []string{"os", "io", "bufio", "net", "net/http", "os/exec", "database/sql", "log"}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, bad := range banned {
			if path == bad || strings.HasPrefix(path, bad+"/") {
				t.Errorf("model.go imports %q: the model must not do I/O", path)
			}
		}
	}

	// A clock read is not an import the list above catches -- time is a legal
	// dependency, time.Now is not.
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if pkg.Name == "time" && (sel.Sel.Name == "Now" || sel.Sel.Name == "Since") {
			t.Errorf("model.go calls time.%s at %s: every timestamp must come from the event log, "+
				"or two readers of one log disagree", sel.Sel.Name, fset.Position(sel.Pos()))
		}
		return true
	})
}

// The names the rest of the epic inherits. A field renamed here after the
// reader, the handler and the page are written is renamed in four places, so
// the epic's shared vocabulary is pinned where a rename has to notice it.
func TestTheSnapshotVocabularyIsWhatTheEpicWasToldToExpect(t *testing.T) {
	var (
		snap Snapshot
		card Card
		sess Session
	)

	snap.At = time.Time{}
	snap.Started = time.Time{}
	snap.Cards = []Card{card}

	card.Key = ""
	card.Title = ""
	card.Verb = ""
	card.Gate = ""
	card.Session = sess

	sess.Actor = ""
	sess.Role = ""
	sess.Model = ""
	sess.Steps = 0
	sess.Activity = ""
	sess.Started = time.Time{}
	sess.Last = time.Time{}
	sess.Done = false

	if len(snap.Cards) != 1 {
		t.Fatalf("a Snapshot holds its Cards: got %d", len(snap.Cards))
	}
}

// A Session that never started reports zero, not the span since the zero
// time -- a stored-and-defaulted field would give a huge, wrong number.
func TestSessionElapsedIsZeroForUninitializedSession(t *testing.T) {
	if got := (Session{}).Elapsed(); got != 0 {
		t.Errorf("Elapsed() on an uninitialized Session = %s, want 0", got)
	}
}

// The grid is the whole point of Snapshot: one card per ticket, drawn in the
// order the reader hands them. A field that only ever holds one Card would
// still compile against every other test here, so this asserts the plural.
func TestSnapshotCardsHoldsMultipleCards(t *testing.T) {
	cards := []Card{
		{Key: "OR-55"},
		{Key: "OR-56"},
		{Key: "OR-57"},
	}
	snap := Snapshot{Cards: cards}

	if got, want := len(snap.Cards), 3; got != want {
		t.Fatalf("Snapshot.Cards len = %d, want %d", got, want)
	}
	for i, want := range []string{"OR-55", "OR-56", "OR-57"} {
		if got := snap.Cards[i].Key; got != want {
			t.Errorf("Snapshot.Cards[%d].Key = %q, want %q", i, got, want)
		}
	}
}

// Verb is a plain string, not an enum -- internal/ui's five outcome words are
// the contract, not a constant this package imports. Each must round-trip
// through the field unchanged.
func TestCardVerbAcceptsTheFiveOutcomeWords(t *testing.T) {
	for _, verb := range []string{"ok", "working", "waiting", "warning", "failed"} {
		card := Card{Verb: verb}
		if got := card.Verb; got != verb {
			t.Errorf("Card{Verb: %q}.Verb = %q, want %q", verb, got, verb)
		}
	}
}

// Gate explains a waiting card; a card with an agent on it has nothing to
// explain. Both are valid states of the same string field.
func TestCardGateCanBeEmptyOrHoldAWaitingReason(t *testing.T) {
	active := Card{Verb: "working", Gate: ""}
	if got := active.Gate; got != "" {
		t.Errorf("active Card.Gate = %q, want empty", got)
	}

	waiting := Card{Verb: "waiting", Gate: "pull request #482 opened, awaiting CI -- no agent is running"}
	if got, want := waiting.Gate, "pull request #482 opened, awaiting CI -- no agent is running"; got != want {
		t.Errorf("waiting Card.Gate = %q, want %q", got, want)
	}
}

// Actor is the stable key matched against internal/events' Actor constants --
// it must survive independently of Role, which is only what a person reads.
func TestSessionActorIsTheStableEventLogIdentifier(t *testing.T) {
	s := Session{Actor: "implementer", Role: "backend developer"}
	if got, want := s.Actor, "implementer"; got != want {
		t.Errorf("Session.Actor = %q, want %q", got, want)
	}
}

// Role is the display name, carried alongside Actor rather than looked up at
// draw time, and it must not collapse to (or overwrite) Actor.
func TestSessionRoleIsTheDisplayNameDistinctFromActor(t *testing.T) {
	s := Session{Actor: "qa", Role: "QA engineer"}
	if got, want := s.Role, "QA engineer"; got != want {
		t.Errorf("Session.Role = %q, want %q", got, want)
	}
	if s.Role == s.Actor {
		t.Errorf("Session.Role (%q) must be distinct from Session.Actor (%q)", s.Role, s.Actor)
	}
}
