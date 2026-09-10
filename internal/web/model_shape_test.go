package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"
)

// model.go is a vocabulary, not code: no functions, no methods, no
// package-level variables. These three are split out from
// TestTheModelDeclaresTypesAndDoesNoIO so a regression in one declaration
// kind names itself instead of hiding inside one shared assertion.
func TestModelDeclaresNoFunctions(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "model.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
			t.Errorf("model.go declares func %s; this file is types only", fn.Name.Name)
		}
	}
}

func TestModelDeclaresNoMethods(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "model.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil {
			t.Errorf("model.go declares method %s on %s; this file is types only", fn.Name.Name, fn.Recv.List[0].Type)
		}
	}
}

func TestModelDeclaresNoPackageVars(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "model.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
			t.Error("model.go declares a package-level variable; this file is types only")
		}
	}
}

// The package is what every other ticket in the epic builds against; if it
// doesn't compile, nothing downstream does either. Referencing every
// exported name is the test.
func TestModelCompiles(t *testing.T) {
	_ = Snapshot{At: time.Now(), Cards: []Card{{Key: "OR-55"}}}
	_ = Card{Sessions: []Session{{Actor: "implementer"}}}
	_ = Session{}
}

func TestSnapshotAtHoldsAndComparesTimestamps(t *testing.T) {
	earlier := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)

	first := Snapshot{At: earlier}
	second := Snapshot{At: later}

	if !first.At.Before(second.At) {
		t.Errorf("first.At = %v, want it before second.At = %v", first.At, second.At)
	}
	if !second.At.After(first.At) {
		t.Errorf("second.At = %v, want it after first.At = %v", second.At, first.At)
	}
	if first.At.Equal(second.At) {
		t.Error("distinct timestamps compared equal")
	}

	same := Snapshot{At: earlier}
	if !first.At.Equal(same.At) {
		t.Errorf("first.At = %v, want it equal to same.At = %v", first.At, same.At)
	}
}

func TestSnapshotCardsSliceCanBeEmptySingleOrMultiple(t *testing.T) {
	empty := Snapshot{Cards: []Card{}}
	if len(empty.Cards) != 0 {
		t.Errorf("empty.Cards = %d entries, want 0", len(empty.Cards))
	}

	single := Snapshot{Cards: []Card{{Key: "OR-55"}}}
	if len(single.Cards) != 1 {
		t.Errorf("single.Cards = %d entries, want 1", len(single.Cards))
	}

	multiple := Snapshot{Cards: []Card{{Key: "OR-55"}, {Key: "OR-272"}, {Key: "OR-421"}}}
	if len(multiple.Cards) != 3 {
		t.Errorf("multiple.Cards = %d entries, want 3", len(multiple.Cards))
	}

	var nilCards Snapshot
	if len(nilCards.Cards) != 0 {
		t.Errorf("zero-value Snapshot.Cards = %d entries, want 0", len(nilCards.Cards))
	}
}
