package fanout

import (
	"strings"
	"testing"
)

// The case that made this a ticket. OR-259 and OR-43 both edit
// scripts/release.sh; a person excluded OR-43 from the first real batch by
// reading the ticket text, and nothing in Orion could have.
func TestTwoTicketsDeclaringTheSameFileAreNotIndependent(t *testing.T) {
	v := Independent(
		Scope{Key: "OR-43", Paths: []string{"scripts/release.sh"}},
		[]Scope{{Key: "OR-259", Paths: []string{"scripts/release.sh"}}})

	if !v.Serial {
		t.Fatalf("admitted two tickets that both declare scripts/release.sh")
	}
	if !strings.Contains(v.Reason, "scripts/release.sh") {
		t.Errorf("the reason must name the overlap, got %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "OR-259") {
		t.Errorf("the reason must name the ticket already holding it, got %q", v.Reason)
	}
	// The reason is grouped by sentence in the report, so it must not carry
	// the key of the ticket it is about -- that would put every collision on
	// its own line (queue.Decision.Line).
	if strings.Contains(v.Reason, "OR-43") {
		t.Errorf("the reason names its own ticket, which defeats grouping: %q", v.Reason)
	}
}

// Two grains of the same ground. A check comparing strings would call these
// independent and then eject the branch at assembly time, which is the
// discovery this moves earlier.
func TestADirectoryAndAFileInsideItAreTheSameGround(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{"internal/watch", "internal/watch/watch.go", "internal/watch/watch.go"},
		{"internal/watch/watch.go", "internal/watch", "internal/watch/watch.go"},
		{"./internal/queue/", "internal/queue", "internal/queue"},
	} {
		got := Overlap(Scope{Paths: []string{c.a}}, Scope{Paths: []string{c.b}})
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want [%s]", c.a, c.b, got, c.want)
		}
	}
}

// Neighbours with a shared prefix are not the same ground. internal/watcher is
// not inside internal/watch, and a prefix test written without the separator
// would hold one of them back forever.
func TestAPrefixThatIsNotAPathBoundaryIsNotAnOverlap(t *testing.T) {
	if got := Overlap(
		Scope{Paths: []string{"internal/watch"}},
		Scope{Paths: []string{"internal/watcher"}}); len(got) != 0 {
		t.Errorf("internal/watch and internal/watcher overlap: %v", got)
	}
}

// Unknown is not conflict -- the rule OR-243 and OR-95 both keep. A ticket
// nobody has written a scope for must not be held behind one that has.
func TestAnAbsentScopeHoldsNothingBack(t *testing.T) {
	spokenFor := []Scope{{Key: "OR-259", Paths: []string{"scripts/release.sh"}}}

	if v := Independent(Scope{Key: "OR-1"}, spokenFor); v.Serial {
		t.Errorf("a ticket declaring nothing was held: %q", v.Reason)
	}
	if v := Independent(Scope{Key: "OR-1", Paths: []string{"  ", ""}}, spokenFor); v.Serial {
		t.Errorf("a scope of blanks was read as a declaration: %q", v.Reason)
	}
	// And the other direction: an in-flight ticket that declared nothing
	// occupies no ground.
	if v := Independent(Scope{Key: "OR-1", Paths: []string{"scripts/release.sh"}},
		[]Scope{{Key: "OR-259"}}); v.Serial {
		t.Errorf("held against a ticket that declared nothing: %q", v.Reason)
	}
}

// "The whole repository" is what a description says when it has not thought
// about scope. Reading it as a universal collision would stop the queue on a
// sentence.
func TestAWholeRepositoryScopeIsNotAPrediction(t *testing.T) {
	for _, p := range []string{".", "/", "./"} {
		if v := Independent(Scope{Key: "OR-1", Paths: []string{p}},
			[]Scope{{Key: "OR-2", Paths: []string{"internal/watch"}}}); v.Serial {
			t.Errorf("a scope of %q collided with everything: %q", p, v.Reason)
		}
	}
}

// Independent walks the spoken-for set in order and the first collision
// decides, so the same input always produces the same sentence.
func TestTheReportedCollisionDoesNotDependOnIteration(t *testing.T) {
	spokenFor := []Scope{
		{Key: "OR-A", Paths: []string{"internal/a"}},
		{Key: "OR-B", Paths: []string{"internal/b"}},
	}
	want := ""
	for i := 0; i < 20; i++ {
		v := Independent(Scope{Key: "OR-C", Paths: []string{"internal/b", "internal/a"}}, spokenFor)
		if !v.Serial {
			t.Fatal("a scope overlapping both was admitted")
		}
		if want == "" {
			want = v.Reason
			continue
		}
		if v.Reason != want {
			t.Fatalf("two runs reported different collisions:\n%q\n%q", want, v.Reason)
		}
	}
	if !strings.Contains(want, "OR-A") {
		t.Errorf("the first spoken-for entry must decide, got %q", want)
	}
}
