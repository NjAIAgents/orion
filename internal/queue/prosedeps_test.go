package queue

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// issueDesc builds a tracker.Issue carrying a description -- plan_test.go's
// own issue(key) helper takes no description, and this package's tests
// share that file, so this is named distinctly rather than changing a
// helper 13 other call sites already depend on.
func issueDesc(key, description string) tracker.Issue {
	return tracker.Issue{Key: key, Description: description}
}

// Given a ticket whose description says "Depends on: OR-60" while
// BlockedBy lacks OR-60, Then it is held, not dispatched, and the reason
// quotes the sentence.
func TestProseBlockedByHoldsAnUnlinkedKeyDependency(t *testing.T) {
	i := issueDesc("OR-65", "Depends on: OR-60. Everything else is ready.")
	dep, held := proseBlockedBy(i)
	if !held {
		t.Fatal("held = false, want true")
	}
	if dep.Key != "OR-60" {
		t.Errorf("Key = %q, want OR-60", dep.Key)
	}
	if dep.Unmapped() {
		t.Error("Unmapped() = true, want false (a key was found)")
	}
	if got := dep.reason(); got == "" || !strings.Contains(got, "Depends on: OR-60") {
		t.Errorf("reason = %q, want it to quote the sentence", got)
	}
}

// Given a ticket whose description names an unmappable dependency
// ("Depends on: server skeleton"), Then it is held and reported as
// unmapped.
func TestProseBlockedByHoldsAndReportsAnUnmappedDependency(t *testing.T) {
	i := issueDesc("OR-65", "Depends on: snapshot and stream endpoints.")
	dep, held := proseBlockedBy(i)
	if !held {
		t.Fatal("held = false, want true")
	}
	if !dep.Unmapped() {
		t.Error("Unmapped() = false, want true (no key in the sentence)")
	}
	if got := dep.reason(); !strings.Contains(got, "unmapped") {
		t.Errorf("reason = %q, want it to say unmapped", got)
	}
}

// Given a ticket whose prose dependency IS already recorded as a link,
// Then it dispatches normally -- the check adds no friction to a
// correctly linked ticket.
func TestProseBlockedByDoesNotHoldADependencyAlreadyLinked(t *testing.T) {
	i := issueDesc("OR-1", "Depends on: OR-60.")
	i.BlockedBy = []string{"OR-60"}
	_, held := proseBlockedBy(i)
	if held {
		t.Error("held = true, want false (OR-60 is already a real link)")
	}
}

// Given a ticket merely mentioning another key in passing, Then it is not
// held -- a bare "OR-123" with no dependency phrase in front of it must
// not be treated as a dependency, or most of the backlog would hold.
func TestProseBlockedByIgnoresABareKeyMention(t *testing.T) {
	for _, desc := range []string{
		"See OR-123 for context on the original design.",
		"Related to OR-55 and OR-56.",
		"Follows the same pattern as OR-90.",
		"",
	} {
		i := issueDesc("OR-1", desc)
		if _, held := proseBlockedBy(i); held {
			t.Errorf("description %q: held = true, want false", desc)
		}
	}
}

// The exact real-world case the ticket cites: OR-65's own description.
func TestProseBlockedByCatchesTheOR65Case(t *testing.T) {
	i := issueDesc("OR-65", "Depends on: snapshot and stream endpoints.")
	_, held := proseBlockedBy(i)
	if !held {
		t.Fatal("the OR-65 case must be caught: held = false, want true")
	}
}

// The exact real-world case the ticket cites: OR-64's own description.
func TestProseBlockedByCatchesTheOR64Case(t *testing.T) {
	i := issueDesc("OR-64", "Depends on: server skeleton.")
	_, held := proseBlockedBy(i)
	if !held {
		t.Fatal("the OR-64 case must be caught: held = false, want true")
	}
}

// Every phrase OR-424 names, each recognised on its own.
func TestProseDependenciesRecognisesEveryNamedPhrase(t *testing.T) {
	cases := []struct {
		name string
		desc string
		want string // expected resolved key, "" for unmapped-but-still-held
	}{
		{"depends on", "Depends on: OR-10.", "OR-10"},
		{"blocked by", "Blocked by OR-11.", "OR-11"},
		{"requires", "Requires OR-12 to land first.", "OR-12"},
		{"prerequisite", "Prerequisite: OR-13.", "OR-13"},
		{"after lands", "This work starts after OR-14 lands.", "OR-14"},
		{"once done", "Wait until once OR-15 is done to start.", "OR-15"},
		{"case insensitive", "DEPENDS ON: or-16.", "OR-16"},
		{"unmapped thing", "Requires the auth layer to exist first.", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := proseDependencies(c.desc)
			if len(deps) == 0 {
				t.Fatalf("proseDependencies(%q) = nil, want at least one match", c.desc)
			}
			if deps[0].Key != c.want {
				t.Errorf("Key = %q, want %q", deps[0].Key, c.want)
			}
		})
	}
}

// decide() itself holds a candidate on a prose dependency, with the
// "prose-dependency" rule -- proving the wiring into plan.go, not just the
// standalone function.
func TestDecideHoldsOnAProseDependency(t *testing.T) {
	f := Facts{
		Candidates: []tracker.Issue{issueDesc("OR-65", "Depends on: snapshot and stream endpoints.")},
		Free:       10,
	}
	decisions := Plan(f)
	if len(decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(decisions))
	}
	d := decisions[0]
	if d.Verdict != Hold {
		t.Errorf("Verdict = %q, want hold", d.Verdict)
	}
	if d.Rule != "prose-dependency" {
		t.Errorf("Rule = %q, want prose-dependency", d.Rule)
	}
}

// A candidate with a prose dependency already satisfied by a real link
// dispatches through Plan normally -- proven at the Plan level, not just
// proseBlockedBy in isolation, since decide()'s ordering (link check
// before prose check) is exactly what must not double-hold it.
func TestDecideAdmitsWhenTheProseDependencyIsAlreadyLinkedAndResolved(t *testing.T) {
	i := issueDesc("OR-1", "Depends on: OR-60.")
	i.BlockedBy = []string{"OR-60"}
	f := Facts{
		Candidates: []tracker.Issue{i},
		Free:       10,
		Resolved:   func(string) (bool, bool) { return true, true }, // OR-60 done
	}
	decisions := Plan(f)
	if len(decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(decisions))
	}
	if decisions[0].Verdict != Admit {
		t.Errorf("Verdict = %q, want admit", decisions[0].Verdict)
	}
}

// A prose dependency that IS linked but not yet RESOLVED is held by the
// existing link-based check (rule "blocked"), never by the prose check --
// decide() tries the real link check first, so this proves the two do not
// double-fire on the same sentence.
func TestDecideHoldsOnTheLinkCheckNotTheProseCheckWhenBothWouldFire(t *testing.T) {
	i := issueDesc("OR-1", "Depends on: OR-60.")
	i.BlockedBy = []string{"OR-60"}
	f := Facts{
		Candidates: []tracker.Issue{i},
		Free:       10,
		Resolved:   func(string) (bool, bool) { return false, true }, // OR-60 known, not done
	}
	decisions := Plan(f)
	if len(decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(decisions))
	}
	if decisions[0].Verdict != Hold {
		t.Errorf("Verdict = %q, want hold", decisions[0].Verdict)
	}
	if decisions[0].Rule != "blocked" {
		t.Errorf("Rule = %q, want blocked (the link check, not prose-dependency)", decisions[0].Rule)
	}
}
