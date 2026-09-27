package decompose

import (
	"fmt"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// pagedJira returns only the first page from Search, as Jira does, and every
// match from SearchAll.
type pagedJira struct {
	fakeJira
	all []tracker.Issue
}

func (p *pagedJira) Search(jql string, _ int) ([]tracker.Issue, error) {
	if len(p.all) > 100 {
		return p.all[:100], nil
	}
	return p.all, nil
}

func (p *pagedJira) SearchAll(jql string) ([]tracker.Issue, error) { return p.all, nil }

// OR-542: a 150-ticket tree was read as 100, so a re-run offered to create
// the other 50 again.
func TestExistingReadsEveryTicketNotOnlyTheFirstPage(t *testing.T) {
	p := &pagedJira{}
	for i := 1; i <= 150; i++ {
		p.all = append(p.all, tracker.Issue{Key: fmt.Sprintf("LTA-%d", i), Summary: fmt.Sprintf("T%03d task", i)})
	}
	got, err := NewJiraBackend(p).Existing("LTA", "orion-spec-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 150 {
		t.Fatalf("found %d existing tickets, want all 150", len(got))
	}
	if got["T150 task"] != "LTA-150" {
		t.Errorf("the last page was not read: T150 -> %q", got["T150 task"])
	}
}
