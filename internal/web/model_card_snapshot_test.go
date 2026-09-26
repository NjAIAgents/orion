package web

import (
	"go/parser"
	"go/token"
	"testing"
	"time"
)

// Key is the tracker identifier, and the axis internal/ui colours by -- the
// browser and the terminal must agree on the same ticket, not a display name
// that could drift between the two surfaces.
func TestCardKeyHoldsTheTrackerIdentifier(t *testing.T) {
	card := Card{Key: "OR-55"}
	if got, want := card.Key, "OR-55"; got != want {
		t.Errorf("Card.Key = %q, want %q", got, want)
	}
}

// Title is the ticket's summary as the tracker states it, independent of Key
// -- a reader fills this from the tracker, not by deriving it from the key.
func TestCardTitleHoldsTheTicketSummary(t *testing.T) {
	card := Card{Key: "OR-55", Title: "Define the snapshot types"}
	if got, want := card.Title, "Define the snapshot types"; got != want {
		t.Errorf("Card.Title = %q, want %q", got, want)
	}
	if card.Title == card.Key {
		t.Errorf("Card.Title (%q) must be distinct from Card.Key (%q)", card.Title, card.Key)
	}
}

// At is the timestamp of the last event read, not the moment the page was
// opened -- the same log must produce the same snapshot on every replay, so
// this field is set from the log, never from time.Now.
func TestSnapshotAtRepresentsTheInstantOfTheLastEventRead(t *testing.T) {
	lastEvent := time.Date(2026, 9, 9, 13, 35, 16, 0, time.UTC)
	snap := Snapshot{At: lastEvent}
	if !snap.At.Equal(lastEvent) {
		t.Errorf("Snapshot.At = %s, want %s", snap.At, lastEvent)
	}
}

// Started is when the run began -- the mockup's "since 13:31:04" -- and it
// must not collapse to At, which moves as the log grows while Started does
// not.
func TestSnapshotStartedRepresentsWhenTheRunBegan(t *testing.T) {
	begin := time.Date(2026, 9, 9, 13, 31, 4, 0, time.UTC)
	later := begin.Add(4 * time.Minute)
	snap := Snapshot{Started: begin, At: later}
	if !snap.Started.Equal(begin) {
		t.Errorf("Snapshot.Started = %s, want %s", snap.Started, begin)
	}
	if snap.Started.Equal(snap.At) {
		t.Errorf("Snapshot.Started (%s) must be distinct from Snapshot.At (%s)", snap.Started, snap.At)
	}
}

// model.go declares types and nothing else -- no package that talks to a
// file, a socket or a process has any business in a file whose whole job is
// naming fields.
func TestModelImportsNoIOPackages(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "model.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}

	ioPackages := map[string]bool{
		"os": true, "io": true, "bufio": true, "net": true, "net/http": true,
		"os/exec": true, "database/sql": true, "log": true, "syscall": true,
	}
	for _, imp := range f.Imports {
		path := imp.Path.Value
		path = path[1 : len(path)-1] // strip quotes
		if ioPackages[path] {
			t.Errorf("model.go imports %q: the model must not do I/O", path)
		}
	}
}
