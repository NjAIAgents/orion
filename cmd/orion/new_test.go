package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// fakeTracker records what it was asked to create, so a test can prove the
// elaborated description actually reached the tracker rather than being
// printed and dropped.
type fakeTracker struct {
	cap      tracker.Capability
	existing map[string]string

	createdKey  string
	createdName string
	createdDesc string
	createdLead string
	creates     int
}

func workingTracker() *fakeTracker {
	return &fakeTracker{
		cap: tracker.Capability{
			Reachable: true, Authenticated: true,
			AccountID: "acct-1", CanCreateProject: true,
		},
		existing: map[string]string{},
	}
}

func (f *fakeTracker) Name() string                       { return "jira" }
func (f *fakeTracker) Probe() (tracker.Capability, error) { return f.cap, nil }

func (f *fakeTracker) ProjectExists(key string) (bool, string, error) {
	id, ok := f.existing[key]
	return ok, id, nil
}

func (f *fakeTracker) Project(key string) (tracker.Project, error) {
	return tracker.Project{}, fmt.Errorf("no project %s", key)
}

func (f *fakeTracker) CreateProject(key, name, description, lead string) (tracker.Binding, error) {
	f.creates++
	f.createdKey, f.createdName, f.createdDesc, f.createdLead = key, name, description, lead
	return tracker.Binding{Provider: "jira", Key: key, ProjectID: "10001", Name: name, Created: true}, nil
}

// answers turns a script of typed lines into stdin. Order is the five
// questions, then the project name.
func answers(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func fullInterview(name string) string {
	return answers(
		"claims agents in the contact centre",
		"they phone the customer back to read out a status",
		"call volume for status chasing drops",
		"no changes to the claims engine itself",
		"must use the existing portal auth",
		name,
	)
}

func runNewInto(t *testing.T, f *fakeTracker, in string, yes bool) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := newRun(f, newOptions{
		Idea:    "customers should see claim status in the portal",
		Site:    "https://example.atlassian.net",
		In:      strings.NewReader(in),
		Out:     &buf,
		Confirm: func(string) bool { return yes },
	})
	return buf.String(), err
}

// THE HANDOFF ARTIFACT. `orion plan` reads Project.Description back as the
// statement of the work and designs from it, so every answer given here has to
// reach the tracker. Before OR-148 the description was the fixed string
// "Provisioned by Orion.", which is what plan was designing from.
func TestTheElaboratedIdeaReachesTheProjectDescription(t *testing.T) {
	f := workingTracker()
	out, err := runNewInto(t, f, fullInterview("Claim Status Self Service"), true)
	if err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatalf("created %d projects, want exactly 1\n%s", f.creates, out)
	}
	for _, want := range []string{
		"customers should see claim status in the portal", // the original flat text
		"For: claims agents in the contact centre",
		"Problem: they phone the customer back to read out a status",
		"Success: call volume for status chasing drops",
		"Out of scope: no changes to the claims engine itself",
		"Constraints: must use the existing portal auth",
	} {
		if !strings.Contains(f.createdDesc, want) {
			t.Errorf("the description does not carry %q:\n%s", want, f.createdDesc)
		}
	}
	if strings.Contains(f.createdDesc, "Provisioned by Orion") {
		t.Errorf("the description is still the placeholder:\n%s", f.createdDesc)
	}
}

// An unanswered question is NAMED, not dropped. The description is the only
// artifact this command leaves, so a later stage has to be able to tell
// "nobody decided this" from "this does not apply" -- silence collapses the
// two and the stage invents the answer.
func TestUnansweredQuestionsAreRecordedRatherThanDropped(t *testing.T) {
	f := workingTracker()
	in := answers("everyone", "", "", "", "", "Claim Status")
	if _, err := runNewInto(t, f, in, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.createdDesc, "Not stated") {
		t.Fatalf("nothing records what went unanswered:\n%s", f.createdDesc)
	}
	for _, want := range []string{"problem", "success", "out of scope", "constraints"} {
		if !strings.Contains(f.createdDesc, want) {
			t.Errorf("%q is unanswered but not named as such:\n%s", want, f.createdDesc)
		}
	}
	if strings.Contains(f.createdDesc, "for,") || strings.Contains(f.createdDesc, ": for") {
		t.Errorf("an ANSWERED question is listed as unstated:\n%s", f.createdDesc)
	}
}

// THE NAME IS THE HUMAN'S. It labels the tracker project, the workspace and
// the git repo (docs/decisions/0009), so it is asked rather than derived from
// the idea text -- and the Jira key is derived from that finalised name.
func TestTheProjectIsNamedByTheHumanAndTheKeyFollowsIt(t *testing.T) {
	f := workingTracker()
	out, err := runNewInto(t, f, fullInterview("Claim Status Self Service"), true)
	if err != nil {
		t.Fatal(err)
	}
	if f.createdName != "Claim Status Self Service" {
		t.Errorf("created name = %q, want the one that was typed", f.createdName)
	}
	if f.createdKey != "CSSS" {
		t.Errorf("created key = %q, want CSSS derived from the finalised name", f.createdKey)
	}
	// Jira refuses a project with no lead, and reports it only after the
	// confirmation has been given -- so the probed account is resolved up front
	// and passed through.
	if f.createdLead != "acct-1" {
		t.Errorf("created lead = %q, want the probed account", f.createdLead)
	}
	if !strings.Contains(out, "claim-status-self-service") {
		t.Errorf("the canonical slug is not shown, so nobody can see the three names line up:\n%s", out)
	}
}

// The key search has to route around what already exists, or creation fails
// on a collision after the human has answered everything.
func TestAnAlreadyTakenKeyIsSteppedPast(t *testing.T) {
	f := workingTracker()
	f.existing["CSSS"] = "1"
	if _, err := runNewInto(t, f, fullInterview("Claim Status Self Service"), true); err != nil {
		t.Fatal(err)
	}
	if f.createdKey != "CSSS2" {
		t.Errorf("created key = %q, want CSSS2 -- CSSS was taken", f.createdKey)
	}
}

// MANDATORY (OR-148): creation goes through adopt.RemotePlan.Describe()'s
// gate, and that gate's whole reason for existing is the sentence about
// deletion. A confirmation that does not say what is irreversible is not one.
func TestCreationIsGatedByTheDescribeThenConfirmGate(t *testing.T) {
	f := workingTracker()
	out, err := runNewInto(t, f, fullInterview("Claim Status"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Orion will create:", "cannot be deleted without admin rights"} {
		if !strings.Contains(out, want) {
			t.Errorf("the gate did not say %q before asking:\n%s", want, out)
		}
	}
}

// Declining creates nothing. A gate whose "no" still creates the project is
// not a gate, and this one guards something a non-admin cannot undo.
func TestDecliningTheGateCreatesNothing(t *testing.T) {
	f := workingTracker()
	out, err := runNewInto(t, f, fullInterview("Claim Status"), false)
	if err != nil {
		t.Fatal(err)
	}
	if f.creates != 0 {
		t.Fatalf("declined, but %d project(s) were created", f.creates)
	}
	if !strings.Contains(out, "cancelled") {
		t.Errorf("declining is silent:\n%s", out)
	}
}

// NO WORKSPACE (docs/decisions/0013). `orion plan` provisions the one
// workspace this project gets; a second one made here would be the orphan
// 0012 refuses.
func TestNewLeavesNothingOnDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	f := workingTracker()
	if _, err := runNewInto(t, f, fullInterview("Claim Status Self Service"), true); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(home, "projects", "claim-status-self-service"),
		filepath.Join(home, "projects"),
	} {
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("orion new provisioned %s; the workspace belongs to orion plan", dir)
		}
	}
}

// The handoff is stated, or the human is left holding a project key and no
// idea it is the input to the next command.
func TestTheNextStepIsOrionPlanWithTheNewKey(t *testing.T) {
	f := workingTracker()
	out, err := runNewInto(t, f, fullInterview("Claim Status Self Service"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "orion plan CSSS") {
		t.Errorf("output does not hand off to orion plan:\n%s", out)
	}
}

// The permission is checked BEFORE the interview. Asking five questions and
// then reporting that the account could never have created a project spends
// the only thing this command spends, which is the human's attention.
func TestAnAccountThatCannotCreateIsRefusedBeforeAnyQuestion(t *testing.T) {
	f := workingTracker()
	f.cap.CanCreateProject = false
	f.cap.Detail = "no create-project permission"

	out, err := runNewInto(t, f, fullInterview("Claim Status"), true)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(out, newQuestions[0].Ask) {
		t.Errorf("the interview started anyway:\n%s", out)
	}
	if !strings.Contains(err.Error(), "orion plan") {
		t.Errorf("the refusal names no way forward: %v", err)
	}
}

// "Undetermined" is not "no". Collapsing the two sends someone to fix a
// permission they may already have.
func TestAnUndeterminedPermissionStillRuns(t *testing.T) {
	f := workingTracker()
	f.cap.CanCreateProject = false
	f.cap.Undetermined = true

	if _, err := runNewInto(t, f, fullInterview("Claim Status"), true); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Errorf("created %d projects, want 1", f.creates)
	}
}

// A name is required, and a closed stdin has to end the loop rather than spin
// printing the same demand forever.
func TestExhaustedInputEndsTheNamePromptInsteadOfLooping(t *testing.T) {
	f := workingTracker()
	_, err := runNewInto(t, f, answers("a", "b", "c", "d", "e"), true)
	if err == nil {
		t.Fatal("expected an error when no name was given")
	}
	if f.creates != 0 {
		t.Errorf("a nameless project was created anyway")
	}
}

// An empty idea has nothing to elaborate.
func TestAnEmptyIdeaIsRefused(t *testing.T) {
	var buf bytes.Buffer
	err := newRun(workingTracker(), newOptions{
		Idea: "   ", In: strings.NewReader(""), Out: &buf,
		Confirm: func(string) bool { return true },
	})
	if err == nil {
		t.Fatal("expected an error for an empty idea")
	}
}
