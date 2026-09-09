package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/creds"
	"github.com/orion-sdlc/orion/internal/tracker"
)

type fakeFiler struct {
	types   []tracker.IssueType
	created []tracker.NewIssue
	fields  []tracker.IdeaField
	skip    []string
	err     error
}

func (f *fakeFiler) IssueTypes(string) ([]tracker.IssueType, error) {
	return f.types, nil
}

func (f *fakeFiler) SetIdeaFields(_, _, _ string, want []tracker.IdeaField) ([]string, error) {
	f.fields = append(f.fields, want...)
	return f.skip, nil
}

func (f *fakeFiler) CreateIssue(in tracker.NewIssue) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.created = append(f.created, in)
	return fmt.Sprintf("PRIOR-%d", len(f.created)), nil
}

func discoveryTypes() []tracker.IssueType {
	return []tracker.IssueType{{ID: "10060", Name: "Idea"}}
}

// PRIOR's real shape: one type, named Idea.
func TestAnIdeaIsFiledAsTheProjectsIdeaType(t *testing.T) {
	f := &fakeFiler{types: discoveryTypes()}

	key, err := fileIdea(f, "PRIOR", "CloudLens", "the full answers", "https://x/browse/CL")
	if err != nil {
		t.Fatalf("fileIdea: %v", err)
	}
	if key != "PRIOR-1" {
		t.Errorf("key = %q", key)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %d issues", len(f.created))
	}
	got := f.created[0]
	if got.TypeID != "10060" {
		t.Errorf("TypeID = %q, want the Idea type", got.TypeID)
	}
	if got.Summary != "CloudLens" {
		t.Errorf("Summary = %q, want the project name", got.Summary)
	}
	if !strings.Contains(got.Description, "the full answers") {
		t.Errorf("the interview answers did not reach the idea: %q", got.Description)
	}
}

// An ordinary project has no Idea type, and must still work rather than
// refusing: Story, then Task, then whatever it has that is not a subtask.
func TestTheTypeFallsBackWhenThereIsNoIdeaType(t *testing.T) {
	for _, tc := range []struct {
		name  string
		types []tracker.IssueType
		want  string
	}{
		{"idea wins", []tracker.IssueType{
			{ID: "1", Name: "Task"}, {ID: "2", Name: "Idea"}}, "2"},
		{"story over task", []tracker.IssueType{
			{ID: "1", Name: "Task"}, {ID: "2", Name: "Story"}}, "2"},
		{"task when that is all", []tracker.IssueType{{ID: "1", Name: "Task"}}, "1"},
		{"first non-subtask otherwise", []tracker.IssueType{
			{ID: "1", Name: "Sub-task", Subtask: true}, {ID: "2", Name: "Improvement"}}, "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferredIdeaType(tc.types); got != tc.want {
				t.Errorf("preferredIdeaType = %q, want %q", got, tc.want)
			}
		})
	}
}

// A subtask cannot exist without a parent, and an idea has none.
func TestASubtaskIsNeverChosenForAnIdea(t *testing.T) {
	if got := preferredIdeaType([]tracker.IssueType{
		{ID: "1", Name: "Sub-task", Subtask: true},
	}); got != "" {
		t.Errorf("chose a subtask type: %q", got)
	}
}

// Asked once. A question re-asked every run is one that gets answered wrongly
// to make it stop.
func TestTheIdeasProjectIsAskedOnceAndRemembered(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer

	got := ideasProject(home, bufio.NewReader(strings.NewReader("prior\n")), &out, true)
	if got != "PRIOR" {
		t.Fatalf("first ask returned %q, want PRIOR (upper-cased)", got)
	}
	if !strings.Contains(out.String(), "Which project?") {
		t.Errorf("it never asked:\n%s", out.String())
	}

	// Second call: no prompt, no reader input available.
	out.Reset()
	got = ideasProject(home, bufio.NewReader(strings.NewReader("")), &out, true)
	if got != "PRIOR" {
		t.Errorf("second call returned %q; the answer was not remembered", got)
	}
	if strings.Contains(out.String(), "Which project?") {
		t.Errorf("it asked again:\n%s", out.String())
	}
}

// Declining is remembered too, or the question returns every run.
func TestDecliningIsRememberedAsADeliberateNo(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer

	if got := ideasProject(home, bufio.NewReader(strings.NewReader("\n")), &out, true); got != "" {
		t.Fatalf("a blank answer returned %q, want no project", got)
	}
	if v := creds.Get(home, creds.IdeasProject); v != creds.IdeasNone {
		t.Errorf("the decline was stored as %q, want the explicit no sentinel", v)
	}

	out.Reset()
	if got := ideasProject(home, bufio.NewReader(strings.NewReader("PRIOR\n")), &out, true); got != "" {
		t.Errorf("it asked again after being declined, and got %q", got)
	}
	if strings.Contains(out.String(), "Which project?") {
		t.Errorf("a stored decline was asked about again:\n%s", out.String())
	}
}

// No terminal means nobody to ask, and a question nobody answers must not
// block a command that otherwise works.
func TestWithoutATerminalNothingIsAskedOrFiled(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer

	if got := ideasProject(home, bufio.NewReader(strings.NewReader("PRIOR\n")), &out, false); got != "" {
		t.Errorf("returned %q off a terminal", got)
	}
	if out.String() != "" {
		t.Errorf("it printed a prompt nobody could answer:\n%s", out.String())
	}
	// And nothing was stored, so it can still be asked on a real terminal.
	if v := creds.Get(home, creds.IdeasProject); v != "" {
		t.Errorf("stored %q without asking anyone", v)
	}
}

// An idea given BY KEY must not be copied back beside itself.
func TestAnIdeaFromTheTrackerIsNotFiledAgain(t *testing.T) {
	t.Setenv("ORION_JIRA_IDEAS_PROJECT", "PRIOR")
	ideas := &fakeIdeas{issues: map[string]tracker.Issue{
		"PRIOR-3": {Key: "PRIOR-3", Summary: "Faster releases", Description: writtenIdea},
	}}
	filer := &fakeFiler{types: discoveryTypes()}
	tr := workingTracker()
	var out bytes.Buffer

	if err := newRun(tr, newOptions{
		Idea: "PRIOR-3", Site: "https://x.atlassian.net",
		In: strings.NewReader(""), Out: &out,
		Ideas: ideas, Filer: filer, Home: t.TempDir(),
		Confirm: func(string) bool { return true },
	}); err != nil {
		t.Fatalf("newRun: %v", err)
	}
	if len(filer.created) != 0 {
		t.Errorf("an idea read from the tracker was filed again: %+v", filer.created)
	}
}

// Best effort: the project exists by then, so a failed idea copy warns.
func TestAFailedIdeaFilingDoesNotFailTheRun(t *testing.T) {
	home := t.TempDir()
	if err := creds.Save(home, map[string]string{creds.IdeasProject: "PRIOR"}); err != nil {
		t.Fatal(err)
	}
	filer := &fakeFiler{types: discoveryTypes(), err: fmt.Errorf("403 forbidden")}
	tr := workingTracker()
	var out bytes.Buffer

	err := newRun(tr, newOptions{
		Idea: "a portal for claim status", Site: "https://x.atlassian.net",
		In:  strings.NewReader(answers("", "", "", "", "", "Claims Portal")),
		Out: &out, Filer: filer, Home: home,
		Confirm: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("a failed idea filing failed the run: %v\n%s", err, out.String())
	}
	if tr.creates != 1 {
		t.Fatalf("the project was not created")
	}
	if !strings.Contains(out.String(), "PRIOR") {
		t.Errorf("the warning does not name the project it could not file into:\n%s", out.String())
	}
}

// The link to the created project goes in Documents, so someone reading the
// idea can reach the work.
func TestTheProjectLinkIsPutInDocuments(t *testing.T) {
	f := &fakeFiler{types: discoveryTypes()}

	if _, err := fileIdea(f, "PRIOR", "CloudLens",
		"Replace the vendor tool. More detail.", "https://x/browse/CLOUDLEN"); err != nil {
		t.Fatalf("fileIdea: %v", err)
	}

	byName := map[string]string{}
	for _, fl := range f.fields {
		byName[fl.Name] = fl.Value
	}
	if got := byName["Documents"]; got != "https://x/browse/CLOUDLEN" {
		t.Errorf("Documents = %q, want the project link", got)
	}
	if got := byName["Idea short description"]; got != "Replace the vendor tool." {
		t.Errorf("short description = %q, want the opening sentence", got)
	}
}

// `orion new` makes no model call and has read no URL. A theme or a roadmap
// horizon is a judgement about a product, and the intent stage fills those
// AFTER researching. Guessing here would put a confident wrong answer where a
// blank was honest.
func TestFilingDoesNotGuessTheJudgementFields(t *testing.T) {
	f := &fakeFiler{types: discoveryTypes()}

	if _, err := fileIdea(f, "PRIOR", "CloudLens", "some idea", ""); err != nil {
		t.Fatalf("fileIdea: %v", err)
	}
	for _, fl := range f.fields {
		switch fl.Name {
		case "Theme", "Roadmap", "State", "MoSCoW", "Customer segments":
			t.Errorf("%s was guessed at filing time: %q", fl.Name, fl.Value)
		}
	}
}

// A field that could not be set is reported, and the idea still counts as
// filed: losing the record to a rejected optional field would be trading the
// idea for its trimmings.
func TestASkippedFieldIsReportedButTheIdeaStands(t *testing.T) {
	f := &fakeFiler{types: discoveryTypes(), skip: []string{`Theme="Nope" (not one of its options)`}}

	key, err := fileIdea(f, "PRIOR", "CloudLens", "some idea", "")
	if key == "" {
		t.Fatal("the idea key was lost because a field was skipped")
	}
	if err == nil {
		t.Fatal("a skipped field was not reported at all")
	}
	if !strings.Contains(err.Error(), "Theme") {
		t.Errorf("the report does not name the skipped field: %v", err)
	}
}
