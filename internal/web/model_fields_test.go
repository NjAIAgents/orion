package web

import (
	"go/parser"
	"go/token"
	"reflect"
	"testing"
	"time"
)

// Field-by-field checks for Card and Session, kept separate from the
// behavioral test in model_test.go so each covers one field's name and type
// without the two competing over the same struct literal.
func TestCardFieldTypes(t *testing.T) {
	typ := reflect.TypeOf(Card{})

	title, ok := typ.FieldByName("Title")
	if !ok || title.Type.Kind() != reflect.String {
		t.Errorf("Card.Title = %v, want string field", title.Type)
	}

	status, ok := typ.FieldByName("Status")
	if !ok || status.Type.Kind() != reflect.String {
		t.Errorf("Card.Status = %v, want string field", status.Type)
	}

	sessions, ok := typ.FieldByName("Sessions")
	if !ok || sessions.Type != reflect.TypeOf([]Session{}) {
		t.Errorf("Card.Sessions = %v, want []Session", sessions.Type)
	}
}

func TestSessionFieldTypes(t *testing.T) {
	typ := reflect.TypeOf(Session{})

	actor, ok := typ.FieldByName("Actor")
	if !ok || actor.Type.Kind() != reflect.String {
		t.Errorf("Session.Actor = %v, want string field", actor.Type)
	}

	model, ok := typ.FieldByName("Model")
	if !ok || model.Type.Kind() != reflect.String {
		t.Errorf("Session.Model = %v, want string field", model.Type)
	}

	steps, ok := typ.FieldByName("Steps")
	if !ok || steps.Type.Kind() != reflect.Int {
		t.Errorf("Session.Steps = %v, want int field", steps.Type)
	}

	activity, ok := typ.FieldByName("Activity")
	if !ok || activity.Type.Kind() != reflect.String {
		t.Errorf("Session.Activity = %v, want string field", activity.Type)
	}

	started, ok := typ.FieldByName("Started")
	if !ok || started.Type != reflect.TypeOf(time.Time{}) {
		t.Errorf("Session.Started = %v, want time.Time field", started.Type)
	}

	last, ok := typ.FieldByName("Last")
	if !ok || last.Type != reflect.TypeOf(time.Time{}) {
		t.Errorf("Session.Last = %v, want time.Time field", last.Type)
	}

	elapsed, ok := typ.FieldByName("Elapsed")
	if !ok || elapsed.Type != reflect.TypeOf(time.Duration(0)) {
		t.Errorf("Session.Elapsed = %v, want time.Duration field", elapsed.Type)
	}

	done, ok := typ.FieldByName("Done")
	if !ok || done.Type.Kind() != reflect.Bool {
		t.Errorf("Session.Done = %v, want bool field", done.Type)
	}
}

// model.go is the vocabulary alone -- it must not acquire an opinion about
// where events.jsonl lives, so the only import it may carry is time.
func TestModelFileImportsOnlyTime(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "model.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}
	if len(f.Imports) != 1 || f.Imports[0].Path.Value != `"time"` {
		t.Errorf("model.go imports = %v, want only \"time\"", f.Imports)
	}
}
