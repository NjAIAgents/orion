package actors

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
)

// OR-544 G: tickets running side by side get different people in one role,
// each keeps theirs for the ticket's whole life, and a finished ticket frees
// its name.
func TestParallelTicketsGetDifferentPeopleInOneRole(t *testing.T) {
	resetAssigned()
	t.Cleanup(resetAssigned)
	EnableTeams()
	a := DisplayFor("LTA-2", events.ActorImplementer)
	b := DisplayFor("LTA-118", events.ActorImplementer)
	if a == b {
		t.Fatalf("two tickets in flight share %q", a)
	}
	if again := DisplayFor("LTA-2", events.ActorImplementer); again != a {
		t.Errorf("LTA-2 changed person mid-ticket: %q then %q", a, again)
	}
	if !strings.HasSuffix(a, "backend developer") || !strings.HasSuffix(b, "backend developer") {
		t.Errorf("the role was lost: %q, %q", a, b)
	}
	Release("LTA-2")
	if c := DisplayFor("LTA-200", events.ActorImplementer); c != a {
		t.Errorf("a freed name was not reused first: got %q, want %q", c, a)
	}
}

func TestAnExhaustedRosterNumbersTheRoleName(t *testing.T) {
	resetAssigned()
	t.Cleanup(resetAssigned)
	EnableTeams()
	seen := map[string]bool{}
	for _, k := range []string{"A-1", "A-2", "A-3", "A-4", "A-5", "A-6"} {
		n := DisplayFor(k, events.ActorImplementer)
		if seen[n] {
			t.Fatalf("%q given twice", n)
		}
		seen[n] = true
	}
	if !seen[Get(events.ActorImplementer).Name+" #2"+Separator+"backend developer"] {
		t.Errorf("no numbered name once the roster ran out: %v", seen)
	}
}

// Outside a watcher, and for nameless actors, nothing changes.
func TestTeamsOffOrNamelessIsDisplayUnchanged(t *testing.T) {
	resetAssigned()
	t.Cleanup(resetAssigned)
	// Two tickets, because the first would get the role's own name anyway.
	for _, k := range []string{"LTA-2", "LTA-118"} {
		if got := DisplayFor(k, events.ActorImplementer); got != Display(events.ActorImplementer) {
			t.Errorf("teams off, %s got %q", k, got)
		}
	}
	EnableTeams()
	if got := DisplayFor("LTA-2", events.ActorOrion); got != Display(events.ActorOrion) {
		t.Errorf("orion renamed to %q", got)
	}
	if got := DisplayFor("", events.ActorImplementer); got != Display(events.ActorImplementer) {
		t.Errorf("no key, got %q", got)
	}
}
