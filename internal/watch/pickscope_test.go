package watch

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

func declaring(key, component string, paths ...string) tracker.Issue {
	i := tracker.Issue{Key: key}
	if component != "" {
		i.Components = []string{component}
	}
	if len(paths) > 0 {
		i.Description = "Files: " + strings.Join(paths, ", ")
	}
	return i
}

// A component is not a file, and it never was. Where the tickets say what they
// will touch, that is what decides -- even when the area heuristic would have
// spread them and been wrong.
func TestPickPrefersTheDeclaredScopeOverTheAreaGuess(t *testing.T) {
	queued := []tracker.Issue{
		// Two different components, so the area heuristic sees no conflict --
		// and both edit the same file.
		declaring("OR-259", "release", "scripts/release.sh"),
		declaring("OR-43", "tooling", "scripts/release.sh"),
		declaring("OR-92", "update", "internal/update/version.go"),
	}
	got, basis := pick(queued, 2)

	if len(got) != 2 || got[0] != "OR-259" || got[1] != "OR-92" {
		t.Fatalf("picked %v, want [OR-259 OR-92]: OR-43 declares the same file as the head", got)
	}
	if basis != "spread by declared scope" {
		t.Errorf("basis = %q; a reader has to be able to tell this from an area guess", basis)
	}
}

// A mixed queue gets the better answer for the tickets that carry a scope
// rather than the worse answer for all of them -- and says that it did.
func TestPickFallsBackToAreaOnlyForTheTicketsThatDeclareNothing(t *testing.T) {
	queued := []tracker.Issue{
		declaring("OR-1", "watch", "internal/watch/watch.go"),
		declaring("OR-2", "watch"), // no declaration: the area rule refuses it
		declaring("OR-3", "notify"),
	}
	got, basis := pick(queued, 2)

	if len(got) != 2 || got[0] != "OR-1" || got[1] != "OR-3" {
		t.Fatalf("picked %v, want [OR-1 OR-3]", got)
	}
	if !strings.Contains(basis, "declared scope") || !strings.Contains(basis, "area") {
		t.Errorf("basis = %q; a mixed spread has to say it was mixed", basis)
	}
}

// Where nothing declares a scope this is exactly the behaviour it always had,
// and it says so rather than implying a decision it did not make.
func TestPickSaysWhenTheSpreadWasOnlyAGuess(t *testing.T) {
	queued := []tracker.Issue{
		declaring("OR-1", "watch"),
		declaring("OR-2", "watch"),
		declaring("OR-3", "notify"),
	}
	got, basis := pick(queued, 2)

	if len(got) != 2 || got[1] != "OR-3" {
		t.Fatalf("picked %v, want the area spread [OR-1 OR-3]", got)
	}
	if !strings.Contains(basis, "no ticket declared a scope") {
		t.Errorf("basis = %q; the fallback must be named as a fallback", basis)
	}
}

// A reordering, not a filter. Refusing to fill a slot would idle an agent to
// avoid a conflict the queue manager has already refused to admit.
func TestPickStillFillsItsSlotsWhenEveryScopeCollides(t *testing.T) {
	queued := []tracker.Issue{
		declaring("OR-1", "watch", "internal/watch"),
		declaring("OR-2", "watch", "internal/watch/watch.go"),
		declaring("OR-3", "watch", "internal/watch/tick.go"),
	}
	got, _ := pick(queued, 2)
	if len(got) != 2 || got[0] != "OR-1" || got[1] != "OR-2" {
		t.Fatalf("picked %v, want [OR-1 OR-2]: with nothing to spread across, rank decides", got)
	}
}

// One slot is the old behaviour exactly: the head of the queue, no reordering
// and nothing to report about a spread that did not happen.
func TestPickAtOneReportsNoBasis(t *testing.T) {
	queued := []tracker.Issue{declaring("OR-1", "watch", "internal/watch")}
	got, basis := pick(queued, 1)
	if len(got) != 1 || got[0] != "OR-1" || basis != "" {
		t.Fatalf("pick = %v, %q; want [OR-1] and no basis", got, basis)
	}
}

// The scope rule needs to see what is already running, and a claimed ticket is
// excluded from the claim query by its label -- so it is read off the
// all-labelled query instead. A ci-wait ticket counts too: its branch is
// pushed and unmerged, which is exactly the state a colliding branch meets.
func TestWorkingCountsClaimedAndCIWaitingTickets(t *testing.T) {
	all := []tracker.Issue{
		{Key: "OR-1", Labels: []string{"ORION"}},
		{Key: "OR-2", Labels: []string{"ORION", tracker.LabelWorking}},
		{Key: "OR-3", Labels: []string{"ORION", tracker.LabelCIWait}},
		{Key: "OR-4", Labels: []string{"ORION", tracker.LabelFailed}},
	}
	got := working(all)
	if len(got) != 2 || got[0].Key != "OR-2" || got[1].Key != "OR-3" {
		t.Fatalf("working = %v, want OR-2 and OR-3", got)
	}
}
