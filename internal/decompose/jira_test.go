package decompose

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// A company-managed Jira project, as createmeta describes one.
var companyManaged = []tracker.IssueType{
	{ID: "10000", Name: "Epic", HierarchyLevel: 1},
	{ID: "10001", Name: "Story", HierarchyLevel: 0},
	{ID: "10002", Name: "Task", HierarchyLevel: 0},
	{ID: "10003", Name: "Sub-task", Subtask: true, HierarchyLevel: -1},
}

// A team-managed one: the bottom level is spelled differently and the types
// are renamed. A mapping keyed on names works on one of these and not the
// other, which is why the mapping is keyed on hierarchy level.
var teamManaged = []tracker.IssueType{
	{ID: "20000", Name: "Initiative", HierarchyLevel: 1},
	{ID: "20001", Name: "Deliverable", HierarchyLevel: 0},
	{ID: "20002", Name: "Subtask", Subtask: true, HierarchyLevel: -1},
}

type fakeJira struct {
	types  []tracker.IssueType
	issues []tracker.Issue
	sent   []tracker.NewIssue
	jql    []string
	n      int
}

func (f *fakeJira) Search(jql string, _ int) ([]tracker.Issue, error) {
	f.jql = append(f.jql, jql)
	return f.issues, nil
}

func (f *fakeJira) IssueTypes(string) ([]tracker.IssueType, error) {
	f.n++
	return f.types, nil
}

func (f *fakeJira) CreateIssue(in tracker.NewIssue) (string, error) {
	f.sent = append(f.sent, in)
	return "CAT-" + string(rune('0'+len(f.sent))), nil
}

func TestJiraMapsTheHierarchyIntoJirasOwnTerms(t *testing.T) {
	for _, tc := range []struct {
		name  string
		types []tracker.IssueType
		// want is the issue type id expected for each of the four positions.
		epic, story, groupedTask, ungroupedTask string
	}{
		{"company-managed", companyManaged, "10000", "10001", "10003", "10002"},
		// Renamed types still map: level 1 is the epic whatever it is
		// called, and the only level-0 type takes both the story and the
		// ungrouped-task position.
		{"team-managed and renamed", teamManaged, "20000", "20001", "20002", "20001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeJira{types: tc.types}
			b := NewJiraBackend(f)

			for _, step := range []struct {
				req  CreateRequest
				want string
				what string
			}{
				{CreateRequest{Project: "CAT", Kind: KindEpic}, tc.epic, "the epic"},
				{CreateRequest{Project: "CAT", Kind: KindStory, Parent: "CAT-1", ParentKind: KindEpic}, tc.story, "a story under the epic"},
				{CreateRequest{Project: "CAT", Kind: KindTask, Parent: "CAT-2", ParentKind: KindStory}, tc.groupedTask, "a task under its story"},
				{CreateRequest{Project: "CAT", Kind: KindTask, Parent: "CAT-1", ParentKind: KindEpic}, tc.ungroupedTask, "an ungrouped task under the epic"},
			} {
				step.req.Summary = step.what
				if _, err := b.Create(step.req); err != nil {
					t.Fatalf("%s: %v", step.what, err)
				}
				got := f.sent[len(f.sent)-1]
				if got.TypeID != step.want {
					t.Errorf("%s got issue type %s, want %s", step.what, got.TypeID, step.want)
				}
				if got.ParentKey != step.req.Parent {
					t.Errorf("%s got parent %q, want %q -- a tree whose children do not\n"+
						"carry their parent is not navigable in Jira", step.what, got.ParentKey, step.req.Parent)
				}
			}

			if f.n != 1 {
				t.Errorf("createmeta was read %d times; it is per-project metadata that\n"+
					"cannot change mid-run", f.n)
			}
		})
	}
}

// A project that cannot express the tree must say so rather than create a
// detached item that looks like part of it.
func TestJiraRefusesAProjectThatCannotExpressTheTree(t *testing.T) {
	flat := []tracker.IssueType{{ID: "1", Name: "Task", HierarchyLevel: 0}}
	f := &fakeJira{types: flat}
	b := NewJiraBackend(f)

	if _, err := b.Create(CreateRequest{Project: "FLAT", Kind: KindEpic, Summary: "e"}); err == nil {
		t.Error("a project with no epic level has no root for the tree; want an error")
	}
	_, err := b.Create(CreateRequest{
		Project: "FLAT", Kind: KindTask, Summary: "t", Parent: "FLAT-2", ParentKind: KindStory})
	if err == nil {
		t.Fatal("a project with no sub-task type cannot put a task under a story; want an error")
	}
	if !strings.Contains(err.Error(), "Task") {
		t.Errorf("the error should name what the project does offer, so the operator can\n"+
			"act on it: %v", err)
	}
	if len(f.sent) != 0 {
		t.Errorf("%d issues were created despite the refusal", len(f.sent))
	}
}

func TestJiraExistingSearchesByProjectAndIdentityLabel(t *testing.T) {
	f := &fakeJira{
		types: companyManaged,
		issues: []tracker.Issue{
			{Key: "CAT-9", Summary: "T001 Create the project structure per implementation plan"},
			{Key: "CAT-10", Summary: "T001 Create the project structure per implementation plan"},
		},
	}
	b := NewJiraBackend(f)

	have, err := b.Existing("CAT", "orion-spec-product-catalogue")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.jql) != 1 {
		t.Fatalf("want one query, got %d", len(f.jql))
	}
	for _, want := range []string{`project = "CAT"`, `labels = "orion-spec-product-catalogue"`} {
		if !strings.Contains(f.jql[0], want) {
			t.Errorf("the query must be scoped by %s, or a re-run links another feature's\n"+
				"T001 to this tree:\n  %s", want, f.jql[0])
		}
	}
	if got := have["T001 Create the project structure per implementation plan"]; got != "CAT-9" {
		t.Errorf("duplicate summaries resolved to %q, want the oldest so a re-run is stable", got)
	}
}
