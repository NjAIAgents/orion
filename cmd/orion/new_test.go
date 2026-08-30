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

// Truly empty (not just whitespace) is the same refusal path -- a regression
// that special-cased "   " and left "" to reach the tracker would slip past a
// suite that only ever typed whitespace.
func TestATrulyEmptyIdeaIsRefused(t *testing.T) {
	f := workingTracker()
	var buf bytes.Buffer
	err := newRun(f, newOptions{
		Idea: "", In: strings.NewReader(""), Out: &buf,
		Confirm: func(string) bool { return true },
	})
	if err == nil {
		t.Fatal("expected an error for an empty idea")
	}
	if f.creates != 0 {
		t.Errorf("an empty idea reached project creation")
	}
}

// Five questions, in order, each with its own specific prompt text -- not
// just "five somethings were asked". A reordering or a swapped prompt would
// not fail TestTheElaboratedIdeaReachesTheProjectDescription, which only
// checks the final description, so the order has to be asserted on the
// transcript directly.
func TestAllFiveQuestionsAskedInOrderWithTheirOwnText(t *testing.T) {
	f := workingTracker()
	out, err := runNewInto(t, f, fullInterview("Claim Status"), true)
	if err != nil {
		t.Fatal(err)
	}
	positions := make([]int, len(newQuestions))
	for i, q := range newQuestions {
		idx := strings.Index(out, q.Ask)
		if idx < 0 {
			t.Fatalf("question %q never appears in the transcript:\n%s", q.Ask, out)
		}
		positions[i] = idx
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Errorf("question %d (%q) did not appear after question %d (%q):\n%s",
				i, newQuestions[i].Ask, i-1, newQuestions[i-1].Ask, out)
		}
	}
}

// User answers are captured EXACTLY as typed -- not trimmed of internal
// whitespace, not case-folded, not otherwise normalised on the way into the
// description.
func TestAnswersAreCapturedExactlyAsTyped(t *testing.T) {
	f := workingTracker()
	in := answers(
		"  Weird   Spacing  Kept  ",
		"MixedCASE preserved",
		"emoji ok \u2705",
		"n/a",
		"n/a",
		"Claim Status",
	)
	if _, err := runNewInto(t, f, in, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.createdDesc, "For: Weird   Spacing  Kept") {
		t.Errorf("internal whitespace was not preserved verbatim:\n%s", f.createdDesc)
	}
	if !strings.Contains(f.createdDesc, "Problem: MixedCASE preserved") {
		t.Errorf("case was altered:\n%s", f.createdDesc)
	}
	if !strings.Contains(f.createdDesc, "Success: emoji ok \u2705") {
		t.Errorf("unicode content was not preserved:\n%s", f.createdDesc)
	}
}

// The name prompt loops on a blank answer (bare Enter) rather than accepting
// it -- unlike the five elaboration questions, where blank is a legitimate
// "unstated" answer.
func TestNamePromptLoopsPastBlankAnswersUntilNonEmpty(t *testing.T) {
	f := workingTracker()
	in := answers("everyone", "problem", "success", "", "", "", "  ", "Claim Status")
	out, err := runNewInto(t, f, in, true)
	if err != nil {
		t.Fatal(err)
	}
	if f.createdName != "Claim Status" {
		t.Errorf("created name = %q, want %q -- the blank lines should have been re-prompted, not accepted",
			f.createdName, "Claim Status")
	}
	if strings.Count(out, "Project name?") < 2 {
		t.Errorf("the name prompt does not appear to have re-asked after a blank line:\n%s", out)
	}
}

// No validation rules on the name: arbitrary punctuation and symbols are
// accepted verbatim and reach the tracker unmodified. Slugify/DeriveKey still
// run on it for the key, but the NAME itself is not filtered.
func TestArbitraryNameTextIsAcceptedWithoutValidation(t *testing.T) {
	f := workingTracker()
	name := "Claims v2 (beta) #hot!!"
	out, err := runNewInto(t, f, fullInterview(name), true)
	if err != nil {
		t.Fatal(err)
	}
	if f.createdName != name {
		t.Errorf("created name = %q, want the exact typed text %q", f.createdName, name)
	}
	if !strings.Contains(out, name) {
		t.Errorf("the typed name is not echoed back to the user before confirmation:\n%s", out)
	}
}

// The success message names the created key and prints a browse link built
// from the tracker's own site -- the thing a human actually needs to go look
// at what was just created.
func TestSuccessMessageShowsKeyAndBrowseLink(t *testing.T) {
	f := workingTracker()
	out, err := runNewInto(t, f, fullInterview("Claim Status Self Service"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CSSS", "https://example.atlassian.net/browse/CSSS"} {
		if !strings.Contains(out, want) {
			t.Errorf("success output missing %q:\n%s", want, out)
		}
	}
}

// No workspace ID and no workspace path are ever printed -- there is no
// workspace. A leftover message from before OR-148 would point at something
// that was never created.
func TestNoWorkspaceIdOrPathIsPrinted(t *testing.T) {
	f := workingTracker()
	out, err := runNewInto(t, f, fullInterview("Claim Status Self Service"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"workspace id", "Workspace ID", "workspace path", "Workspace path"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output mentions %q, but orion new provisions no workspace:\n%s", unwanted, out)
		}
	}
}

// A tracker that cannot be reached at all fails before the interview starts,
// same as an explicit permission refusal -- Probe() itself is the first call
// made, and its error is not swallowed or deferred past the five questions.
func TestTrackerProbeErrorRefusesBeforeAnyQuestion(t *testing.T) {
	f := &probeErrTracker{err: fmt.Errorf("dial tcp: connection refused")}
	var buf bytes.Buffer
	err := newRun(f, newOptions{
		Idea:    "customers should see claim status in the portal",
		Site:    "https://example.atlassian.net",
		In:      strings.NewReader(fullInterview("Claim Status")),
		Out:     &buf,
		Confirm: func(string) bool { return true },
	})
	if err == nil {
		t.Fatal("expected the unreachable tracker's error to surface")
	}
	if strings.Contains(buf.String(), newQuestions[0].Ask) {
		t.Errorf("the interview started despite an unreachable tracker:\n%s", buf.String())
	}
}

// Name identical to the idea text is not special-cased: it is asked for like
// any other name, accepted verbatim, and slugified the same way.
func TestNameIdenticalToIdeaIsAcceptedAndSlugified(t *testing.T) {
	f := workingTracker()
	idea := "Fix login bug"
	var buf bytes.Buffer
	err := newRun(f, newOptions{
		Idea: idea, Site: "https://example.atlassian.net",
		In:  strings.NewReader(answers("a", "b", "c", "d", "e", idea)),
		Out: &buf, Confirm: func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.createdName != idea {
		t.Errorf("created name = %q, want %q", f.createdName, idea)
	}
	if f.createdKey != "FLB" {
		t.Errorf("created key = %q, want FLB derived from %q", f.createdKey, idea)
	}
}

// Unicode in the idea itself survives into the description verbatim.
func TestUnicodeIdeaIsPreservedInFull(t *testing.T) {
	f := workingTracker()
	idea := "客户应该能看到理赔状态 -- caf\u00e9 \U0001F680"
	var buf bytes.Buffer
	err := newRun(f, newOptions{
		Idea: idea, Site: "https://example.atlassian.net",
		In:  strings.NewReader(fullInterview("Claim Status")),
		Out: &buf, Confirm: func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.createdDesc, idea) {
		t.Errorf("the unicode idea was not preserved verbatim:\n%s", f.createdDesc)
	}
}

// Very long idea text is preserved in full, not truncated on the way into
// the description.
func TestVeryLongIdeaIsPreservedInFull(t *testing.T) {
	f := workingTracker()
	idea := strings.TrimSpace(strings.Repeat("customers should see claim status in the portal and also ", 200))
	var buf bytes.Buffer
	err := newRun(f, newOptions{
		Idea: idea, Site: "https://example.atlassian.net",
		In:  strings.NewReader(fullInterview("Claim Status")),
		Out: &buf, Confirm: func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.createdDesc, idea) {
		t.Errorf("the long idea was truncated or altered on the way into the description")
	}
}

// A name whose slug collides with an ALREADY-EXISTING project (not just one
// created earlier in the same run) still resolves to the next free key --
// exercising ResolveKey's collision path from a name-driven slug rather than
// a hand-picked key string.
func TestNameCollidingWithAnExistingProjectSlugGetsTheNextKey(t *testing.T) {
	f := workingTracker()
	f.existing["CS"] = "existing-project-id"
	out, err := runNewInto(t, f, fullInterview("Claim Status"), true)
	if err != nil {
		t.Fatal(err)
	}
	if f.createdKey != "CS2" {
		t.Errorf("created key = %q, want CS2 -- CS already belongs to another project", f.createdKey)
	}
	if !strings.Contains(out, "CS2") {
		t.Errorf("the resolved key is not shown to the user:\n%s", out)
	}
}

// probeErrTracker fails at Probe(), the very first call `newRun` makes --
// simulating an unreachable tracker or a network error, independent of the
// authentication/permission fields Capability would otherwise carry.
type probeErrTracker struct{ err error }

func (p *probeErrTracker) Name() string                       { return "jira" }
func (p *probeErrTracker) Probe() (tracker.Capability, error) { return tracker.Capability{}, p.err }
func (p *probeErrTracker) ProjectExists(key string) (bool, string, error) {
	return false, "", fmt.Errorf("should not be reached")
}
func (p *probeErrTracker) Project(key string) (tracker.Project, error) {
	return tracker.Project{}, fmt.Errorf("should not be reached")
}
func (p *probeErrTracker) CreateProject(key, name, description, lead string) (tracker.Binding, error) {
	return tracker.Binding{}, fmt.Errorf("should not be reached: creation must not happen when Probe fails")
}
