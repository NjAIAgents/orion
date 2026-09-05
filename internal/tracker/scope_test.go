package tracker

import (
	"reflect"
	"testing"
)

// The contract in one line: `orion decompose` writes it, the queue reads it,
// and neither knows anything about the other beyond this spelling.
func TestTheDeclaredScopeIsReadOffTheDescription(t *testing.T) {
	i := Issue{Description: "Add the gate.\n\n## Scope\nWhat is in.\n" +
		"Files: internal/queue/plan.go, internal/fanout\n\n## Tests\n..."}

	want := []string{"internal/queue/plan.go", "internal/fanout"}
	if got := i.DeclaredScope(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DeclaredScope = %v, want %v", got, want)
	}
}

// Absence is the normal case, and it means unknown -- never "touches
// nothing". Every caller treats the empty result as no prediction to judge.
func TestATicketThatDeclaresNothingReadsAsNothing(t *testing.T) {
	for _, desc := range []string{
		"",
		"No scope here at all.",
		"The files: it changed are described below.", // not at the start of a line
	} {
		if got := (Issue{Description: desc}).DeclaredScope(); len(got) != 0 {
			t.Errorf("DeclaredScope(%q) = %v, want nothing", desc, got)
		}
	}
}

// A description picks up markdown in passing, and a declaration lost to a
// bullet or a bold marker is a collision nobody predicted.
func TestTheDeclarationSurvivesMarkdownDecoration(t *testing.T) {
	for _, line := range []string{
		"Files: internal/watch",
		"- Files: internal/watch",
		"**Files:** internal/watch",
		"  files:  `internal/watch`  ",
	} {
		got := DeclaredScope(line)
		if len(got) != 1 || got[0] != "internal/watch" {
			t.Errorf("DeclaredScope(%q) = %v, want [internal/watch]", line, got)
		}
	}
}

// Every matching line, unioned and de-duplicated. Taking only the first would
// silently narrow a scope, which is the direction that produces the collision
// this whole axis exists to design out.
func TestEveryDeclarationLineCounts(t *testing.T) {
	got := DeclaredScope("Files: internal/a\nsomething else\nFiles: internal/b, internal/a\n")
	want := []string{"internal/a", "internal/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeclaredScope = %v, want %v", got, want)
	}
}

// A wrong path is worse than a missing one: it is read as a claim on ground
// the ticket never meant, and holds a different ticket back for a collision
// that does not exist.
func TestProseOnTheDeclarationLineIsNotReadAsAPath(t *testing.T) {
	got := DeclaredScope("Files: internal/queue, the whole of the fix loop")
	want := []string{"internal/queue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeclaredScope = %v, want %v -- an entry with spaces is prose", got, want)
	}
}
