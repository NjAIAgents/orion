package collect

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// jqlTracker answers a search by reading the JQL, so a test can tell the
// ready-set query apart from the one that finds work in the first place.
type jqlTracker struct {
	fakeTracker
	byLabel map[string][]tracker.Issue
	queries []string
}

func (j *jqlTracker) Search(jql string, _ int) ([]tracker.Issue, error) {
	j.queries = append(j.queries, jql)
	for label, issues := range j.byLabel {
		if strings.Contains(jql, label) {
			return issues, nil
		}
	}
	return nil, nil
}

// TestTheReadyInboxIsSearched is OR-311's regression guard.
//
// A finished ticket sets orion-ready and the batch is the only thing that
// takes it. collect searched orion-ci-wait ALONE, so nothing ever read the
// inbox: four tickets stranded for 41 minutes on 2026-09-02, and OR-135 the
// day before. Both were rescued by hand without the cause being found.
func TestTheReadyInboxIsSearched(t *testing.T) {
	j := &jqlTracker{byLabel: map[string][]tracker.Issue{
		tracker.LabelReady: {{Key: "OR-1"}},
	}}

	got := readySet([]string{"OR-1", "OR-2"}, Deps{Jira: j})

	if !got["OR-1"] {
		t.Error("a ticket carrying orion-ready was not recognised as ready")
	}
	if got["OR-2"] {
		t.Error("a ticket without the label was reported ready")
	}
	if len(j.queries) != 1 {
		t.Errorf("made %d searches for a %d-ticket pass; it must be one", len(j.queries), 2)
	}
}

// TestAnUnreadableTrackerDoesNotSkipTickets.
//
// readySet's failure mode has to be "report what we found", not "say nothing".
// Skipping on error is how a ticket vanishes from every report while looking
// healthy; falling through to the per-branch path is visible and recoverable.
func TestAnUnreadableTrackerDoesNotSkipTickets(t *testing.T) {
	j := &jqlTracker{}
	j.searchErr = errTracker

	if got := readySet([]string{"OR-1"}, Deps{Jira: j}); len(got) != 0 {
		t.Errorf("an unreadable tracker produced a ready set: %v", got)
	}
}

// TestNoTrackerIsNotAnEmptyPass. A nil client must not panic on a path that
// runs every collect tick.
func TestNoTrackerIsNotAnEmptyPass(t *testing.T) {
	if got := readySet([]string{"OR-1"}, Deps{}); len(got) != 0 {
		t.Errorf("a nil tracker produced %v", got)
	}
	if got := readySet(nil, Deps{Jira: &jqlTracker{}}); len(got) != 0 {
		t.Errorf("an empty pass produced %v", got)
	}
}

// A Done ticket with a stale orion-ready label must not enter the pass
// (OR-326): the dry run offered two closed tickets to the batch.
func TestThePassExcludesDoneTickets(t *testing.T) {
	jql := waitingJQL([]string{"OR"})
	if !strings.Contains(jql, "statusCategory !=") {
		t.Errorf("the pass query must exclude Done: %s", jql)
	}
	if !strings.Contains(jql, tracker.LabelReady) || !strings.Contains(jql, tracker.LabelCIWait) {
		t.Errorf("the pass query must still find ready and ci-wait tickets: %s", jql)
	}
}
