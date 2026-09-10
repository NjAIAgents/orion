package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"
)

// Every field the run view needs, named and typed as the rest of the epic
// will name it. The assertions are dull on purpose: what this test defends is
// that the NAMES and TYPES still exist, because the reader, the handler and
// the page are each written against them separately.
func TestTheThreeTypesCarryTheFieldsTheViewNeeds(t *testing.T) {
	start := time.Date(2026, 9, 9, 13, 31, 4, 0, time.UTC)
	taken := start.Add(4*time.Minute + 12*time.Second)

	snap := Snapshot{
		At: taken,
		Cards: []Card{{
			Key:    "OR-272",
			Title:  "The gate board: everything waiting on me, across projects",
			Status: "working",
			Sessions: []Session{
				{
					Actor:   "implementer",
					Model:   "opus",
					Steps:   3,
					Started: start,
					Last:    start.Add(2 * time.Minute),
					Elapsed: 2 * time.Minute,
					Done:    true,
				},
				{
					Actor:    "qa",
					Model:    "sonnet",
					Steps:    4,
					Activity: "editing internal/web/board.go",
					Started:  start.Add(2 * time.Minute),
					Last:     start.Add(3 * time.Minute),
					Elapsed:  2*time.Minute + 12*time.Second,
				},
			},
		}},
	}

	if !snap.At.Equal(taken) {
		t.Errorf("snapshot At = %v, want %v", snap.At, taken)
	}
	if len(snap.Cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(snap.Cards))
	}

	card := snap.Cards[0]
	if card.Key != "OR-272" || card.Status != "working" || card.Title == "" {
		t.Errorf("card = %+v, want the key, title and one of the five verbs", card)
	}
	if len(card.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2: a ticket is worked by several agents", len(card.Sessions))
	}

	// The finished session and the running one differ in the two fields the
	// renderer branches on, and in nothing else structural.
	if done := card.Sessions[0]; !done.Done || done.Activity != "" {
		t.Errorf("finished session = %+v, want Done with no present-tense activity", done)
	}
	live := card.Sessions[1]
	if live.Done {
		t.Error("running session reported Done")
	}
	if live.Actor != "qa" || live.Model != "sonnet" || live.Steps != 4 {
		t.Errorf("running session = %+v, want the actor, model and step count", live)
	}
	if live.Activity != "editing internal/web/board.go" {
		t.Errorf("activity = %q, want the agent's own last line", live.Activity)
	}
	// Elapsed runs to the snapshot's clock, not to the last log line. A
	// renderer that could derive it from Started..Last would freeze every
	// live card at the moment it last spoke -- which is why it is carried.
	if want := taken.Sub(live.Started); live.Elapsed != want {
		t.Errorf("elapsed = %v, want %v (Snapshot.At - Started)", live.Elapsed, want)
	}
	if !live.Last.Before(taken) {
		t.Error("Last should be able to lag At: that gap is how a hung agent is spotted")
	}
}

// "Compiles with no I/O in the file" is the whole point of declaring the
// types before anything fills them: a vocabulary that cannot open a file
// cannot acquire an opinion about where events.jsonl lives. The reader ticket
// is exactly when someone will be tempted to reach for os here.
func TestTheModelDeclaresTypesAndDoesNoIO(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "model.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}
	for _, imp := range f.Imports {
		if path := imp.Path.Value; path != `"time"` {
			t.Errorf("model.go imports %s; it may import nothing but time", path)
		}
	}

	// Nor any code to do I/O with: no functions, no methods, no package-level
	// state. Declarations only.
	f, err = parser.ParseFile(token.NewFileSet(), "model.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			t.Errorf("model.go declares func %s; this file is types only", decl.Name.Name)
		case *ast.GenDecl:
			if decl.Tok == token.VAR {
				t.Error("model.go declares package-level state; this file is types only")
			}
		}
	}
}
