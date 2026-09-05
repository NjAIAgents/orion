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

// ".." is the same "has not thought about scope" case as "." and "/" -- a
// scope of the parent directory read literally would collide with anything
// declared beside it, which is not a prediction anyone made in good faith.
func TestParentDirectoryScopeIsNotAPrediction(t *testing.T) {
	if got := (Scope{Paths: []string{".."}}); got.Declared() {
		t.Errorf("Scope{%q}.Declared() = true, want false", "..")
	}
	if v := Independent(Scope{Key: "OR-1", Paths: []string{".."}},
		[]Scope{{Key: "OR-2", Paths: []string{"internal/watch"}}}); v.Serial {
		t.Errorf("a scope of \"..\" collided with everything: %q", v.Reason)
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

// A directory and a subdirectory two levels below it are still one ground,
// not just one level down. internal/watch/watch/tick.go is inside
// internal/watch exactly as much as internal/watch/tick.go is.
func TestATwoLevelNestedDirectoryIsTheSameGround(t *testing.T) {
	got := Overlap(
		Scope{Paths: []string{"internal/watch"}},
		Scope{Paths: []string{"internal/watch/watch/tick.go"}})
	if len(got) != 1 || got[0] != "internal/watch/watch/tick.go" {
		t.Errorf("Overlap = %v, want [internal/watch/watch/tick.go]", got)
	}
}

// A path written with backslashes -- as planning free text sometimes is --
// is the same ground as one written with forward slashes.
func TestBackslashSeparatedPathsNormalizeToTheSameGround(t *testing.T) {
	got := Overlap(
		Scope{Paths: []string{`internal\watch`}},
		Scope{Paths: []string{"internal/watch/tick.go"}})
	if len(got) != 1 || got[0] != "internal/watch/tick.go" {
		t.Errorf("Overlap = %v, want [internal/watch/tick.go]", got)
	}
}

// Case is preserved as written rather than folded, so two paths differing
// only in case are not read as the same ground.
func TestCaseSensitivityOfPathsIsPreserved(t *testing.T) {
	if got := Overlap(
		Scope{Paths: []string{"Internal/Watch"}},
		Scope{Paths: []string{"internal/watch"}}); len(got) != 0 {
		t.Errorf("Internal/Watch and internal/watch overlap: %v", got)
	}
}

// Overlap reports each shared entry in the order it was first seen while
// walking a's paths against b's, not sorted or grouped some other way.
func TestOverlapReturnsSharedGroundInFirstSeenOrder(t *testing.T) {
	got := Overlap(
		Scope{Paths: []string{"internal/watch"}},
		Scope{Paths: []string{"internal/watch/sub", "internal/watch/sub/deep.go"}})
	want := []string{"internal/watch/sub", "internal/watch/sub/deep.go"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Overlap = %v, want %v in first-seen order", got, want)
	}
}

// An empty scope must never trigger a hold, regardless of how much ground is
// already spoken for or by how many tickets: there is no prediction to judge,
// so there is nothing to collide.
func TestAnEmptyScopeNeverTriggersAHoldNoMatterWhatIsInFlight(t *testing.T) {
	spokenFor := []Scope{
		{Key: "OR-A", Paths: []string{"internal/a"}},
		{Key: "OR-B", Paths: []string{"internal/b"}},
		{Key: "OR-C", Paths: []string{"internal/watch"}},
	}
	if v := Independent(Scope{Key: "OR-1"}, spokenFor); v.Serial {
		t.Errorf("an empty scope was held against %d in-flight tickets: %q", len(spokenFor), v.Reason)
	}
}

// The other direction of the same rule: a ticket that declared nothing is
// already in flight, and it must occupy no ground for whatever comes next.
func TestAnEmptyScopeOccupiesNoGroundForTheNextCandidate(t *testing.T) {
	v := Independent(
		Scope{Key: "OR-2", Paths: []string{"internal/a"}},
		[]Scope{{Key: "OR-1"}})
	if v.Serial {
		t.Errorf("a ticket was held against an in-flight ticket that declared nothing: %q", v.Reason)
	}
}

// One candidate declares real ground, another declares none: the unknown one
// must not interfere with the known one, and both are admitted.
func TestAnUnknownScopeDoesNotInterfereWithAKnownScope(t *testing.T) {
	known := Independent(Scope{Key: "OR-1", Paths: []string{"internal/a"}}, nil)
	unknown := Independent(Scope{Key: "OR-2"}, []Scope{{Key: "OR-1", Paths: []string{"internal/a"}}})
	if known.Serial {
		t.Errorf("the known scope was held for no reason: %q", known.Reason)
	}
	if unknown.Serial {
		t.Errorf("the unknown scope was held against the known one: %q", unknown.Reason)
	}
}

// A spoken-for entry that no longer declares anything -- as if its ticket's
// scope had been edited away after an earlier pass took note of it -- must
// not shadow a real collision reported by a later entry. The candidate is
// still correctly held, just by whichever entry actually still names the
// ground.
func TestDowngradingFromDeclaredToUndeclaredDoesNotReAdmitAHeldTicket(t *testing.T) {
	spokenFor := []Scope{
		{Key: "OR-A"}, // downgraded: used to declare the same path, now declares nothing
		{Key: "OR-B", Paths: []string{"scripts/release.sh"}},
	}
	v := Independent(Scope{Key: "OR-C", Paths: []string{"scripts/release.sh"}}, spokenFor)
	if !v.Serial {
		t.Fatalf("a real collision with OR-B was missed because OR-A no longer declares anything")
	}
	if !strings.Contains(v.Reason, "OR-B") {
		t.Errorf("the reason must name OR-B, the entry that actually still collides: %q", v.Reason)
	}
	if strings.Contains(v.Reason, "OR-A") {
		t.Errorf("OR-A declares nothing now and must not appear in the reason: %q", v.Reason)
	}
}
