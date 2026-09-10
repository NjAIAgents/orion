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
