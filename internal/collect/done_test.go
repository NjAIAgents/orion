package collect

import (
	"bytes"
	"github.com/orion-sdlc/orion/internal/testproc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/done"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The -count=2 re-run, against a real repository and a real toolchain
// (OR-229).
//
// The defect it reproduces is the one that shipped green: a test that passes
// when run once and fails when run twice, because it depends on state the
// first run leaves behind. Nothing about the branch, the diff or the check
// rollup distinguishes it from a correct one -- only running it again does.
//
// The test asserts BOTH halves, and the first is what makes it evidence about
// the flag rather than about the harness: the same test passes at -count=1,
// which is exactly what CI saw.
func TestARerunAtCountTwoCatchesATestThatOnlyPassesOnce(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH")
	}
	origin, clone, files := branchWithAFlakyTest(t)

	// What CI saw. If this fails the fixture is wrong, not the code.
	if !goTestPasses(t, origin, "-count=1") {
		t.Fatal("the fixture's test does not pass at -count=1, so it proves nothing " +
			"about a green check that was hiding a defect")
	}

	res := rerunAtCountTwo(clone, "develop", "orion/x-2", done.Diff{Files: files})

	if res.Skipped != "" {
		t.Fatalf("the re-run declined to run: %s", res.Skipped)
	}
	if !res.Failed {
		t.Fatalf("a test that only passes once was re-run at -count=2 and reported "+
			"as passing\npackages: %v\n%s", res.Packages, res.Output)
	}
	if !strings.Contains(res.Output, "TestOnlyPassesOnce") {
		t.Errorf("the failure does not name the test that failed:\n%s", res.Output)
	}
	// And the whole point: this is what the verdict is built from.
	if v := done.Triage(done.Evidence{Key: "OR-229", Rerun: res}, nil); v.Done {
		t.Errorf("a branch whose new test fails at -count=2 was reported done:\n%s", v.Report())
	}
}

// A branch whose new tests survive being run twice is not handed back. Most
// branches are this one, and a pass that cried wolf here would be switched off
// before it ever caught anything.
func TestARerunAtCountTwoPassesAHonestTest(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH")
	}
	_, clone, files := branchWithATest(t, `package x

import "testing"

func TestAddsUp(t *testing.T) {
	if Add(2, 2) != 4 {
		t.Fatal("2 + 2")
	}
}
`)

	res := rerunAtCountTwo(clone, "develop", "orion/x-2", done.Diff{Files: files})

	if res.Failed {
		t.Fatalf("an honest test was reported as failing at -count=2:\n%s", res.Output)
	}
	if res.Skipped != "" {
		t.Fatalf("the re-run declined to run: %s", res.Skipped)
	}
}

// A branch that changes no test file has nothing to re-run, and says so.
// Reported rather than dropped: a check that did not run is not a check that
// passed, and the difference belongs in the verdict.
func TestABranchWithNoNewGoTestsSaysWhyNothingWasRerun(t *testing.T) {
	res := rerunAtCountTwo(t.TempDir(), "develop", "orion/x-2",
		done.Diff{Files: []string{"README.md", "internal/x/x.go"}})

	if res.Failed {
		t.Fatal("a branch with no new tests was reported as failing them")
	}
	if !strings.Contains(res.Skipped, "-count=2") {
		t.Errorf("the skip does not say why nothing was re-run: %q", res.Skipped)
	}
}

// A checkout that could not be made is not a failing test. Handing a ticket
// back because git refused would be a worse fault than the one this catches,
// and would look identical to a person reading the verdict.
func TestARerunThatCouldNotCheckOutIsSkippedNotFailed(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH")
	}
	res := rerunAtCountTwo(t.TempDir(), "develop", "orion/nope",
		done.Diff{Files: []string{"internal/x/x_test.go"}})

	if res.Failed {
		t.Fatal("a repository that could not be read was reported as a failing test")
	}
	if res.Skipped == "" {
		t.Error("nothing ran and the verdict does not say why")
	}
}

// The re-run has to happen in a fresh checkout of origin/<branch>, never in
// whatever the passed-in directory has lying around uncommitted -- the job
// worktree is exactly where the branch's OWN uncommitted residue (what
// strandedTests looks for) sits, and testing it would test something the
// pull request does not carry.
//
// Dirtying the passed-in repository's working tree with a file that would
// break the build if it rode along proves the re-run used a clean checkout
// rather than the directory it was given.
func TestARerunAtCountTwoUsesAFreshCheckoutNotTheGivenDirsWorkingTree(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH")
	}
	_, clone, files := branchWithATest(t, `package x

import "testing"

func TestAddsUp(t *testing.T) {
	if Add(2, 2) != 4 {
		t.Fatal("2 + 2")
	}
}
`)
	// Uncommitted, and syntactically broken: if rerunAtCountTwo ran the
	// suite against clone's own working tree instead of an ephemeral
	// checkout of origin/orion/x-2, this alone would fail the build.
	write(t, clone, "internal/x/broken_test.go", "package x\n\nfunc this is not valid go {\n")

	res := rerunAtCountTwo(clone, "develop", "orion/x-2", done.Diff{Files: files})

	if res.Skipped != "" {
		t.Fatalf("the re-run declined to run: %s", res.Skipped)
	}
	if res.Failed {
		t.Fatalf("the re-run picked up an uncommitted file from the given directory "+
			"instead of a clean checkout of the branch:\n%s", res.Output)
	}
}

func TestGoTestPackagesMapsChangedTestFilesToTheirPackages(t *testing.T) {
	got := goTestPackages([]string{
		"internal/x/x.go",
		"internal/x/x_test.go",
		"internal/x/more_test.go", // same package, named once
		"cmd/orion/main_test.go",
		"docs/notes.md",
		"web/app.test.ts", // not Go; -count is a go test flag
	})

	want := []string{"./internal/x", "./cmd/orion"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("packages = %v, want %v", got, want)
	}
}

// The OR-217 shape, mechanically: a test file sitting in the ticket's own
// worktree that the branch does not carry. CI tested a commit without it, so
// the green check was never about the change.
func TestStrandedTestsFindsATestTheBranchDoesNotCarry(t *testing.T) {
	ws, branch := workspaceWithAJobWorktree(t)
	wt := jobTree(ws, branch)

	// Committed: this one reached the pull request.
	write(t, wt, "internal/x/x_test.go", "package x\n")
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "commit", "--quiet", "-m", "the test QA committed")
	// Left on disk: this one did not. OR-217, exactly.
	write(t, wt, "internal/x/offbyone_test.go", "package x\n")
	// And a non-test file, which is somebody's business but not this check's.
	write(t, wt, "scratch.txt", "notes")

	got := strandedTests(ws, branch, []string{"internal/x/x_test.go"})

	if len(got) != 1 {
		t.Fatalf("stranded = %v, want exactly the uncommitted test file", got)
	}
	if !strings.Contains(got[0], "offbyone_test.go") {
		t.Errorf("stranded = %v, want the uncommitted test file", got)
	}
}

// A clean worktree strands nothing. This is the ordinary case since OR-234
// made Orion commit what the QA stage leaves behind, and it must stay quiet.
func TestACleanWorktreeStrandsNothing(t *testing.T) {
	ws, branch := workspaceWithAJobWorktree(t)
	wt := jobTree(ws, branch)
	write(t, wt, "internal/x/x_test.go", "package x\n")
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "commit", "--quiet", "-m", "committed")

	if got := strandedTests(ws, branch, []string{"internal/x/x_test.go"}); len(got) != 0 {
		t.Errorf("a clean worktree reported stranded files: %v", got)
	}
}

// Only the ticket's OWN worktree is read. worktreeOrRepo falls back to the
// shared clone, whose uncommitted files belong to whatever else is using it --
// reporting those would hand this ticket back for somebody else's residue.
func TestStrandedTestsIgnoresTheSharedCloneWhenThereIsNoJobWorktree(t *testing.T) {
	home, _ := bound(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}

	if got := strandedTests(ws, "orion/fcia-6", nil); got != nil {
		t.Errorf("a ticket with no worktree of its own reported stranded files: %v", got)
	}
}

// ---------------------------------------------------------------------------
// The gate, end to end.
// ---------------------------------------------------------------------------

// The acceptance criterion, wired up: a green pull request that is not done is
// handed back, and NOBODY is asked to approve it.
//
// The trigger here is the OR-217 shape, chosen because it is mechanical: no
// model runs, so this asserts the wiring rather than a model's judgement.
func TestANotDoneVerdictHandsTheTicketBackAndAsksNobody(t *testing.T) {
	home, ws, branch := boundWithAJobWorktree(t)
	wt := jobTree(ws, branch)
	write(t, wt, "internal/x/offbyone_test.go", "package x\n")

	jira := newTracker()
	slack := &slackSpy{}
	out := collectOnce(t, home, jira, slack, nil,
		PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"})

	if len(slack.posted) != 0 {
		t.Fatalf("a change that is not done was offered for approval:\n%v", slack.posted)
	}
	// Handed back, not held: out of ci-wait so nothing polls it forever, and
	// visible so the queue manager can decide what happens next.
	if !has(jira.removed["FCIA-6"], tracker.LabelCIWait) {
		t.Errorf("the ticket kept ci-wait, so it blocks rather than hands back: %v",
			jira.removed["FCIA-6"])
	}
	if !has(jira.added["FCIA-6"], tracker.LabelFailed) {
		t.Errorf("the ticket was not made visible again: %v", jira.added["FCIA-6"])
	}
	// The evidence has to reach the ticket, or the verdict is unactionable.
	comments := strings.Join(jira.comments["FCIA-6"], "\n")
	if !strings.Contains(comments, "offbyone_test.go") {
		t.Errorf("the ticket comment does not name what was found:\n%s", comments)
	}
	if !strings.Contains(out, "not done") {
		t.Errorf("the console does not say the change is not done:\n%s", out)
	}
	// It reports. It never merges, approves or edits.
	if jira.transitions["FCIA-6"] != "" {
		t.Errorf("the triage transitioned the ticket to %q", jira.transitions["FCIA-6"])
	}
}

// A run that IS done goes on to be offered for approval, exactly as before.
// The pass is a gate on a specific defect, not a second approval step.
func TestADoneVerdictGoesOnToAskForApproval(t *testing.T) {
	home, ws, branch := boundWithAJobWorktree(t)
	_ = jobTree(ws, branch) // clean: nothing stranded

	jira := newTracker()
	slack := &slackSpy{}
	collectOnce(t, home, jira, slack, nil,
		PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"})

	if len(slack.posted) != 1 {
		t.Fatalf("a change that is done was not offered for approval: %v", slack.posted)
	}
}

// The model is asked once per commit, not once per poll. `orion collect` is a
// poll, and this pass runs a suite and may run a model -- so a ticket waiting
// a day for an approval would otherwise pay for both on every tick.
func TestATicketIsTriagedOncePerHeadCommit(t *testing.T) {
	home, _, _ := boundWithAJobWorktree(t)
	jira := newTracker()
	slack := &slackSpy{}
	asked := 0
	judge := func(*workspace.Workspace, string, string) (string, error) {
		asked++
		return done.ReplyDone, nil
	}
	pr := PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"}

	collectOnce(t, home, jira, slack, judge, pr)
	collectOnce(t, home, jira, slack, judge, pr)
	collectOnce(t, home, jira, slack, judge, pr)

	if asked != 1 {
		t.Errorf("the model was asked %d times for one unchanged commit", asked)
	}
}

// A branch somebody has pushed to is a different change, and the previous
// verdict says nothing about it.
func TestAPushedCommitIsTriagedAfresh(t *testing.T) {
	home, _, _ := boundWithAJobWorktree(t)
	jira := newTracker()
	slack := &slackSpy{}
	asked := 0
	judge := func(*workspace.Workspace, string, string) (string, error) {
		asked++
		return done.ReplyDone, nil
	}

	collectOnce(t, home, jira, slack, judge,
		PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"})
	collectOnce(t, home, jira, slack, judge,
		PR{Verdict: VerdictPassing, Head: "def456", URL: "https://example/pr/1"})

	if asked != 2 {
		t.Errorf("the model was asked %d times across two different commits, want 2", asked)
	}
}

// A merged ticket's triage record is a statement about a commit nobody would
// be approving any more, and merged() is supposed to forget it (OR-244,
// alongside clearFixes and clearRebases). Without this, a ticket reopened
// later and pushed to the SAME commit hash by coincidence -- or one whose
// workspace state was never cleaned up -- would read as already triaged and
// skip straight past the gate.
func TestAMergedTicketForgetsItsTriageRecord(t *testing.T) {
	home, ws, _ := boundWithAJobWorktree(t)
	if err := markTriaged(ws.Dir, "FCIA-6", "abc123"); err != nil {
		t.Fatal(err)
	}
	if got := loadRequests(ws.Dir).Triaged["FCIA-6"]; got != "abc123" {
		t.Fatalf("fixture did not record the triage: %q", got)
	}

	jira := newTracker()
	slack := &slackSpy{}
	collectOnce(t, home, jira, slack, nil,
		PR{Verdict: VerdictMerged, Head: "abc123", URL: "https://example/pr/1"})

	if got, ok := loadRequests(ws.Dir).Triaged["FCIA-6"]; ok {
		t.Errorf("a merged ticket kept its triage record: %q", got)
	}
}

// The gate sits between a PASSING verdict and the approval request, and
// nowhere else. A red build, a still-running check, a closed pull request or
// a fresh merge each end the pass through a different branch of the verdict
// switch -- none of them should pay for a test rerun or a model call that
// exists to answer a question none of them are asking.
func TestTheGateDoesNotRunOnNonGreenVerdicts(t *testing.T) {
	for _, v := range []Verdict{VerdictPending, VerdictFailing, VerdictClosed, VerdictMerged} {
		t.Run(string(v), func(t *testing.T) {
			home, _, _ := boundWithAJobWorktree(t)
			jira := newTracker()
			slack := &slackSpy{}
			asked := 0
			judge := func(*workspace.Workspace, string, string) (string, error) {
				asked++
				return done.ReplyDone, nil
			}

			collectOnce(t, home, jira, slack, judge,
				PR{Verdict: v, Head: "abc123", URL: "https://example/pr/1"})

			if asked != 0 {
				t.Errorf("verdict %s asked the intent question %d time(s); the gate only "+
					"runs on VerdictPassing", v, asked)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// supervisorDonePrompt: what the model is actually handed.
// ---------------------------------------------------------------------------

// Absent input is stated as absent, not left blank. A model handed an empty
// "WHAT IT ASKED FOR" section invents a plausible requirement and judges the
// diff against that -- which is a hand-back nobody can trace to anything.
func TestSupervisorDonePromptStatesAbsentCriteriaAsAbsent(t *testing.T) {
	p := supervisorDonePrompt(done.Evidence{Key: "OR-1"})

	if !strings.Contains(p, "could not be read") {
		t.Errorf("absent criteria were not declared as absent:\n%s", p)
	}
	if !strings.Contains(p, done.ReplyDone) {
		t.Errorf("the fallback for absent criteria does not point the model at %q:\n%s",
			done.ReplyDone, p)
	}
}

// Same reasoning for an unreadable diff: the model has to be told it is
// looking at a hole, not at a ticket with no changes.
func TestSupervisorDonePromptStatesAbsentDiffAsAbsent(t *testing.T) {
	p := supervisorDonePrompt(done.Evidence{Key: "OR-1", Criteria: "does a thing"})

	if !strings.Contains(p, "could not be read") {
		t.Errorf("an absent diff was not declared as absent:\n%s", p)
	}
}

// The model needs the ticket's key, its summary, its criteria, the file
// summary, and the patch itself -- an intent check with any of these missing
// is judging the diff against nothing.
func TestSupervisorDonePromptCarriesEveryField(t *testing.T) {
	ev := done.Evidence{
		Key: "OR-1", Summary: "add the --json flag",
		Criteria: "the CLI accepts --json and emits JSON",
		Diff:     done.Diff{Stat: "1 file changed", Patch: "diff --git a/x b/x"},
	}
	p := supervisorDonePrompt(ev)

	for _, want := range []string{"OR-1", "add the --json flag",
		"the CLI accepts --json", "1 file changed", "diff --git a/x b/x"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt does not carry %q:\n%s", want, p)
		}
	}
}

// ---------------------------------------------------------------------------
// Fixtures.
// ---------------------------------------------------------------------------

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// branchWithATest builds an origin holding a tiny Go module on develop, plus a
// branch that adds one test file, and returns a clone to inspect it from.
func branchWithATest(t *testing.T, testFile string) (origin, clone string, files []string) {
	t.Helper()
	origin = t.TempDir()
	gitRun(t, origin, "init", "--quiet", "--bare", "--initial-branch=develop")

	seed := t.TempDir()
	gitRun(t, seed, "init", "--quiet", "--initial-branch=develop")
	write(t, seed, "go.mod", "module example.test/x\n\ngo 1.21\n")
	write(t, seed, "internal/x/x.go", "package x\n\nfunc Add(a, b int) int { return a + b }\n")
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "--quiet", "-m", "base")
	gitRun(t, seed, "remote", "add", "origin", origin)
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "develop")

	gitRun(t, seed, "checkout", "--quiet", "-b", "orion/x-2")
	write(t, seed, "internal/x/x_test.go", testFile)
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "--quiet", "-m", "the ticket's test")
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "orion/x-2")

	clone = filepath.Join(t.TempDir(), "repo")
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, clone)
	return origin, clone, []string{"internal/x/x_test.go"}
}

// branchWithAFlakyTest is OR-229's shape: a test that passes on its first run
// in a process and fails on its second, because it depends on what the first
// one left behind.
func branchWithAFlakyTest(t *testing.T) (origin, clone string, files []string) {
	t.Helper()
	return branchWithATest(t, `package x

import "testing"

var runs int

func TestOnlyPassesOnce(t *testing.T) {
	runs++
	if runs > 1 {
		t.Fatal("this assertion only holds the first time it runs")
	}
}
`)
}

func goTestPasses(t *testing.T, origin, count string) bool {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "at-count-1")
	gitRun(t, t.TempDir(), "clone", "--quiet", "--branch", "orion/x-2", origin, dir)
	cmd := testproc.Command(t, "go", "test", count, "./internal/x")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// workspaceWithAJobWorktree builds FCIA-6's own checkout, where the code that
// looks for stranded tests looks for it.
func workspaceWithAJobWorktree(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	_, ws, branch := boundWithAJobWorktree(t)
	return ws, branch
}

// boundWithAJobWorktree is approvalRepo -- a project that requires approval,
// so "was anybody asked?" is a question this fixture can answer -- plus
// FCIA-6's own checkout, where the stranded-test check looks.
func boundWithAJobWorktree(t *testing.T) (home string, ws *workspace.Workspace, branch string) {
	t.Helper()
	home, _ = approvalRepo(t, `"navjyot"`)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err = workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	branch = "orion/fcia-6"
	wt := filepath.Join(ws.Dir, "worktrees", strings.ReplaceAll(branch, "/", "-"))
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "init", "--quiet", "--initial-branch=develop")
	gitRun(t, wt, "commit", "--quiet", "--allow-empty", "-m", "base")
	gitRun(t, wt, "checkout", "--quiet", "-b", branch)
	return home, ws, branch
}

func collectOnce(t *testing.T, home string, jira *fakeTracker, slack SlackAPI,
	judge func(*workspace.Workspace, string, string) (string, error), pr PR) string {

	t.Helper()
	var buf bytes.Buffer
	Run(Options{Keys: []string{"FCIA-6"}, Home: home, Out: &buf}, Deps{
		Jira:    jira,
		Status:  func(string, string) (PR, error) { return pr, nil },
		Refresh: func(string, string) (string, error) { return "", nil },
		Prune:   func(*workspace.Workspace, string) error { return nil },
		Judge:   judge,
		Slack:   slack,
	})
	return buf.String()
}
