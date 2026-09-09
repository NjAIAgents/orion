package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/provision"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// chainWS is a workspace the chain can write to: the remote step records
// the URL it made in task.json, so the directory has to exist.
func chainWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	w := &workspace.Workspace{ID: "cloudlens", Dir: t.TempDir()}
	w.Task.Slug = "cloudlens"
	if err := os.MkdirAll(w.MetaDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	return w
}

// fakeRemote stands in for provision.Remote so no test creates a repository
// on GitHub. It honours Confirm the way the real one does -- a declined
// confirmation is an error -- so the chain's handling of a refusal is what
// is under test, not a stub that always says yes.
func fakeRemote(t *testing.T) *int {
	t.Helper()
	calls := 0
	orig := remoteFn
	remoteFn = func(opts provision.Options) (*provision.Result, error) {
		calls++
		if opts.Confirm != nil && !opts.Confirm("create?") {
			return nil, fmt.Errorf("cancelled: no remote created")
		}
		return &provision.Result{RemoteURL: "git@github.com:test/" + opts.Name + ".git"}, nil
	}
	t.Cleanup(func() { remoteFn = orig })
	return &calls
}

// okRun is a stage that succeeds, recording the order it was asked for.
func okRun(order *[]string) stageRunner {
	return func(_ *workspace.Workspace, stage string) (*supervisor.Result, error) {
		*order = append(*order, stage)
		return &supervisor.Result{ExitCode: 0, Duration: time.Second, LogPath: "/tmp/x.log"}, nil
	}
}

func yes(string) bool { return true }

func TestTheChainRunsEveryStageInRosterOrder(t *testing.T) {
	fakeRemote(t)
	var order []string
	var out bytes.Buffer

	done := runPlanChain(&out, chainWS(t), okRun(&order), yes)

	if done != len(planStages) {
		t.Fatalf("completed %d of %d stages:\n%s", done, len(planStages), out.String())
	}
	want := make([]string, 0, len(planStages))
	for _, s := range planStages {
		// A fallback step with nothing to work from runs supervised.
		if s.Frame == nil || s.Fallback {
			want = append(want, s.Stage)
		}
	}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("ran %v, want the supervised stages in roster order %v", order, want)
	}
}

// The reason the chain pauses at all: a stage's artifact is what the next
// stage designs from, so the operator reads it before paying for the one that
// consumes it.
func TestDecliningTheNextStageStopsTheChain(t *testing.T) {
	fakeRemote(t)
	var order []string
	var out bytes.Buffer

	// Yes to the first continue, no to the second.
	asked := 0
	ask := func(string) bool { asked++; return asked < 2 }

	runPlanChain(&out, chainWS(t), okRun(&order), ask)

	// intent runs unasked (first to run), constitution on the first yes,
	// spec is declined. Named rather than counted, because done frame steps ahead
	// of intent are skipped and would shift any index.
	if strings.Join(order, ",") != "intent,constitution" {
		t.Errorf("ran %v; want intent then constitution, and the declined spec must not run", order)
	}
	if !strings.Contains(out.String(), "at your request") {
		t.Errorf("a declined chain must say it stopped deliberately:\n%s", out.String())
	}
	// Naming the stage it stopped BEFORE is what makes it resumable.
	if !strings.Contains(out.String(), "--stage spec") {
		t.Errorf("output does not name the resume command:\n%s", out.String())
	}
}

// CloudLens. The spec stage could not find the intent it was to design from,
// refused, and said so in its artifact. Every stage after it would have
// designed from a document that says it is not a design.
func TestAStageThatFailsStopsTheChainAndNamesTheFix(t *testing.T) {
	fakeRemote(t)
	var ran []string
	var out bytes.Buffer

	run := func(_ *workspace.Workspace, stage string) (*supervisor.Result, error) {
		ran = append(ran, stage)
		if stage == "spec" {
			return &supervisor.Result{ExitCode: 0, Duration: time.Second},
				fmt.Errorf("the spec stage declared itself BLOCKED in its own artifact")
		}
		return &supervisor.Result{ExitCode: 0, Duration: time.Second}, nil
	}

	done := runPlanChain(&out, chainWS(t), run, yes)

	// intent and constitution run and succeed; spec blocks. So everything
	// before spec completed, plus any done frame step ahead of it, and
	// nothing after spec ran at all.
	specAt := -1
	for i, s := range planStages {
		if s.Stage == "spec" {
			specAt = i
		}
	}
	if done != specAt {
		t.Fatalf("completed %d steps, want %d (everything before spec) before spec blocked:\n%s", done, specAt, out.String())
	}
	if strings.Join(ran, ",") != "intent,constitution,spec" {
		t.Errorf("ran %v; nothing may run after a blocked stage", ran)
	}
	// The underlying error names the fix; the chain must not bury it.
	if !strings.Contains(out.String(), "BLOCKED") {
		t.Errorf("the block reason was swallowed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "--stage spec") {
		t.Errorf("output does not name the stage to re-run:\n%s", out.String())
	}
}

// The first stage is not asked about: the operator confirmed the chain and
// its cost immediately before, and nothing has happened since.
func TestTheFirstStageIsNotAskedAboutTwice(t *testing.T) {
	fakeRemote(t)
	var order []string
	var out bytes.Buffer
	asked := 0

	// Only the chain's own "Continue to ...?" prompts: a frame step may ask
	// its own question before an outward action (the remote confirms before
	// creating a repository), and that is a second, deliberate confirmation,
	// not the chain asking twice.
	runPlanChain(&out, chainWS(t), okRun(&order), func(q string) bool {
		if strings.HasPrefix(q, "Continue to") {
			asked++
		}
		return true
	})

	// One per step that runs, except the first that runs: a done step is
	// skipped, not asked about (the toolkit is done when nothing delegates
	// to spec-kit; the clone when no copy was asked for), wherever it sits.
	w := chainWS(t)
	runs := 0
	for _, s := range planStages {
		if s.Done == nil || !s.Done(w) {
			runs++
		}
	}
	if want := runs - 1; asked != want {
		t.Errorf("asked %d times, want %d -- one per step that runs, after the first", asked, want)
	}
}

// Defaulting to yes would make an unattended chain spend the whole estimate,
// which is the failure the pause exists to prevent.
func TestAnEmptyAnswerIsNo(t *testing.T) {
	var out bytes.Buffer
	for _, in := range []string{"\n", "", "n\n", "no\n", "maybe\n"} {
		if askYesNo(bufio.NewReader(strings.NewReader(in)), &out, "go?") {
			t.Errorf("%q was read as yes", in)
		}
	}
	for _, in := range []string{"y\n", "Y\n", "yes\n", " YES \n"} {
		if !askYesNo(bufio.NewReader(strings.NewReader(in)), &out, "go?") {
			t.Errorf("%q was not read as yes", in)
		}
	}
}

// The chain must BEGIN with intent, because every stage after it reads the
// file intent writes.
//
// It did not, and the spec stage opened by reading docs/intent/<slug>.md --
// a file nothing in Orion had ever created. The stage refused rather than
// inventing a product, which was right, but it cost a run to find out. This
// test fails if intent is ever dropped or reordered.
func TestTheChainStartsWithIntent(t *testing.T) {
	if len(planStages) == 0 {
		t.Fatal("the planning chain is empty")
	}
	first := ""
	for _, s := range planStages {
		if s.Frame == nil {
			first = s.Stage
			break
		}
	}
	if first != "intent" {
		t.Errorf("the first supervised stage is %q; every later stage reads what intent writes", first)
	}
	// And spec must come after it, not before.
	intentAt, specAt := -1, -1
	for i, s := range planStages {
		switch s.Stage {
		case "intent":
			intentAt = i
		case "spec":
			specAt = i
		}
	}
	if intentAt < 0 || specAt < 0 {
		t.Fatalf("chain is missing intent or spec: %+v", planStages)
	}
	if intentAt > specAt {
		t.Error("spec runs before intent, so it would read a file that does not exist yet")
	}
}

// withPlanStages swaps the chain for a test and restores it after. The chain
// is a package variable on purpose -- one list that the roster, the cost
// shape and the dispatch all read -- so a test of the dispatch has to be
// able to hand it a chain with the shape under test.
func withPlanStages(t *testing.T, stages []planStage) {
	t.Helper()
	orig := planStages
	planStages = stages
	t.Cleanup(func() { planStages = orig })
}

// A frame step runs in Orion's own process. The stage runner -- the thing
// that spawns a model -- must never be asked for it.
func TestAFrameStepRunsWithoutAStageRunner(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	framed := 0
	withPlanStages(t, []planStage{
		{Stage: "intent", Actor: "pm", What: "intent"},
		{Stage: "remote", Actor: "orion", What: "the remote",
			Frame: func(*stepIO, *workspace.Workspace) error { framed++; return nil }},
		{Stage: "spec", Actor: "architect", What: "spec"},
	})

	done := runPlanChain(&out, chainWS(t), okRun(&ran), yes)

	if done != 3 {
		t.Fatalf("completed %d of 3 steps:\n%s", done, out.String())
	}
	if framed != 1 {
		t.Errorf("the frame step ran %d times, want 1", framed)
	}
	if strings.Join(ran, ",") != "intent,spec" {
		t.Errorf("the stage runner was asked for %v; a frame step must never reach it", ran)
	}
}

// A step whose work is already there is reported as done and skipped -- and
// NOT asked about, because "continue to spec?" has no answer when the spec is
// committed. This is the whole of resume.
func TestADoneStepIsSkippedAndNotAskedAbout(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	asked := []string{}
	withPlanStages(t, []planStage{
		{Stage: "intent", Actor: "pm", What: "intent",
			Done: func(*workspace.Workspace) bool { return true }},
		{Stage: "spec", Actor: "architect", What: "spec",
			Done: func(*workspace.Workspace) bool { return true }},
		{Stage: "plan", Actor: "architect", What: "plan"},
	})
	ask := func(q string) bool { asked = append(asked, q); return true }

	done := runPlanChain(&out, chainWS(t), okRun(&ran), ask)

	if done != 3 {
		t.Fatalf("completed %d of 3 steps; done steps count so a resumed chain can finish:\n%s", done, out.String())
	}
	if strings.Join(ran, ",") != "plan" {
		t.Errorf("ran %v; a done step must not run again", ran)
	}
	for _, q := range asked {
		if strings.Contains(q, "spec") {
			t.Errorf("asked %q about a step that was already done", q)
		}
	}
	if !strings.Contains(out.String(), "= done") || !strings.Contains(out.String(), "spec") {
		t.Errorf("a skipped step must be printed as done, by name:\n%s", out.String())
	}
}

// A frame step that fails stops the chain naming itself and the command that
// resumes -- `orion plan`, not `orion run`, which cannot run a frame step.
func TestAFailingFrameStepStopsTheChainAndNamesTheResume(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	withPlanStages(t, []planStage{
		{Stage: "intent", Actor: "pm", What: "intent"},
		{Stage: "remote", Actor: "orion", What: "the remote",
			Frame: func(*stepIO, *workspace.Workspace) error {
				return fmt.Errorf("gh repo create failed: not logged in")
			}},
		{Stage: "spec", Actor: "architect", What: "spec"},
	})

	done := runPlanChain(&out, chainWS(t), okRun(&ran), yes)

	if done != 1 {
		t.Fatalf("completed %d steps, want 1 before the frame step failed:\n%s", done, out.String())
	}
	if strings.Join(ran, ",") != "intent" {
		t.Errorf("ran %v; nothing may run after a failed frame step", ran)
	}
	for _, want := range []string{"not logged in", "remote", "orion plan"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "--stage remote") {
		t.Errorf("a frame step must not be offered to `orion run --stage`:\n%s", out.String())
	}
}

// `orion run --stage X` suggests what to run next. A frame step is not
// something it can run, so the suggestion skips over one to the next stage
// that spends.
func TestNextPlanStageSkipsFrameSteps(t *testing.T) {
	withPlanStages(t, []planStage{
		{Stage: "scaffold", Actor: "devops"},
		{Stage: "remote", Actor: "orion", Frame: func(*stepIO, *workspace.Workspace) error { return nil }},
		{Stage: "decompose", Actor: "pm"},
		{Stage: "clone", Actor: "orion", Frame: func(*stepIO, *workspace.Workspace) error { return nil }},
	})
	if next, ok := nextPlanStage("scaffold"); !ok || next.Stage != "decompose" {
		t.Errorf("after scaffold got %q/%v, want decompose (skipping the remote frame step)", next.Stage, ok)
	}
	if next, ok := nextPlanStage("decompose"); ok {
		t.Errorf("after decompose got %q; only a frame step follows, so there is nothing for orion run", next.Stage)
	}
}

// Declining to continue INTO a frame step names `orion plan` as the resume,
// not `orion run --stage`, which cannot run one.
func TestDecliningBeforeAFrameStepNamesOrionPlanAsTheResume(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	withPlanStages(t, []planStage{
		{Stage: "scaffold", Actor: "devops", What: "scaffold"},
		{Stage: "remote", Actor: "orion", What: "the remote",
			Frame: func(*stepIO, *workspace.Workspace) error { return nil }},
	})
	no := func(string) bool { return false }

	done := runPlanChain(&out, chainWS(t), okRun(&ran), no)

	if done != 1 {
		t.Fatalf("completed %d steps, want 1:\n%s", done, out.String())
	}
	if !strings.Contains(out.String(), "orion plan") || strings.Contains(out.String(), "--stage remote") {
		t.Errorf("the resume line must be `orion plan`, never `orion run --stage remote`:\n%s", out.String())
	}
}

// Every supervised stage knows when it is done, so a resumed chain never
// re-runs one whose artifact is already there. An entry without Done would
// be re-run on every resume -- silently, and at a stage's cost.
func TestEverySupervisedStageHasADonePredicate(t *testing.T) {
	for _, s := range planStages {
		if s.Frame == nil && s.Done == nil {
			t.Errorf("the %s stage has no Done predicate, so a resume would always re-run it", s.Stage)
		}
	}
}

// Test-time proof the wiring reaches StageDone: a workspace with no runs and
// no artifacts is done at no SUPERVISED stage, so a fresh chain runs every
// one of them. (A frame step may be done on a fresh workspace -- the clone
// is, when no copy was asked for -- which is the step saying it has nothing
// to do, not a stage skipping work.)
func TestAFreshWorkspaceIsDoneAtNoSupervisedStage(t *testing.T) {
	w := &workspace.Workspace{ID: "fresh", Dir: t.TempDir()}
	w.Task.Slug = "thing"
	for _, s := range planStages {
		if s.Frame == nil && s.Done != nil && s.Done(w) {
			t.Errorf("the %s stage reports done on a fresh workspace", s.Stage)
		}
	}
}

// The remote step is done when the task records a remote, so a resumed
// chain never tries to create the repository twice.
func TestTheRemoteStepIsDoneOnceTheTaskRecordsARemote(t *testing.T) {
	calls := fakeRemote(t)
	w := chainWS(t)
	w.Task.Remote = "git@github.com:test/cloudlens.git"
	var out bytes.Buffer
	var ran []string

	runPlanChain(&out, w, okRun(&ran), yes)

	if *calls != 0 {
		t.Errorf("the remote was created %d times although the task already records one", *calls)
	}
	if !strings.Contains(out.String(), "= done") || !strings.Contains(out.String(), "remote") {
		t.Errorf("the remote step is not reported as done:\n%s", out.String())
	}
}

// Declining to create the repository stops the chain before decompose --
// tickets that name a repository nobody agreed to create are worse than no
// tickets -- and the refusal is the operator's own answer, not a failure.
func TestDecliningTheRemoteStopsTheChainBeforeDecompose(t *testing.T) {
	fakeRemote(t)
	w := chainWS(t)
	var out bytes.Buffer
	var ran []string
	ask := func(q string) bool { return !strings.Contains(q, "create?") }

	done := runPlanChain(&out, w, okRun(&ran), ask)

	for _, stage := range ran {
		if stage == "decompose" {
			t.Fatalf("decompose ran after the remote was declined: %v", ran)
		}
	}
	if w.Task.Remote != "" {
		t.Errorf("a declined remote was recorded: %q", w.Task.Remote)
	}
	if !strings.Contains(out.String(), "cancelled") || !strings.Contains(out.String(), "orion plan") {
		t.Errorf("the stop must show the refusal and name the resume:\n%s", out.String())
	}
	if done == len(planStages) {
		t.Error("the chain reported itself complete after a declined step")
	}
}

// On success the URL is recorded in task.json, which is what the next
// resume's Done reads and what `orion watch` pushes to.
func TestTheRemoteStepRecordsTheURLItMade(t *testing.T) {
	calls := fakeRemote(t)
	w := chainWS(t)
	var out bytes.Buffer
	var ran []string

	done := runPlanChain(&out, w, okRun(&ran), yes)

	if done != len(planStages) {
		t.Fatalf("completed %d of %d steps:\n%s", done, len(planStages), out.String())
	}
	if *calls != 1 {
		t.Errorf("the remote was created %d times, want exactly once", *calls)
	}
	if !strings.Contains(w.Task.Remote, "cloudlens") {
		t.Errorf("the remote URL was not recorded on the task: %q", w.Task.Remote)
	}
	if _, err := os.Stat(w.TaskPath()); err != nil {
		t.Errorf("task.json was not written after the remote step: %v", err)
	}
	if !remoteDone(w) {
		t.Error("remoteDone is false right after the remote was recorded")
	}
}

// No copy asked for is a complete answer, so the clone step is done before
// it starts and the chain still ends.
func TestTheCloneStepIsDoneWhenNoCopyWasAskedFor(t *testing.T) {
	fakeRemote(t)
	w := chainWS(t)
	var out bytes.Buffer
	var ran []string

	done := runPlanChain(&out, w, okRun(&ran), yes)

	if done != len(planStages) {
		t.Fatalf("completed %d of %d steps:\n%s", done, len(planStages), out.String())
	}
	if !strings.Contains(out.String(), "= done") || !strings.Contains(out.String(), "clone") {
		t.Errorf("the clone step is not reported as done:\n%s", out.String())
	}
}

// A copy that already exists is done: cloneWorkspace refuses to clone onto
// an existing directory, so a resume must not try.
func TestTheCloneStepIsDoneWhenTheCopyAlreadyExists(t *testing.T) {
	w := chainWS(t)
	dest := filepath.Join(t.TempDir(), "mine")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.Task.CheckoutPath = dest
	if !cloneDone(w) {
		t.Error("a destination that is already a repository is not reported done")
	}
	w.Task.CheckoutPath = filepath.Join(t.TempDir(), "not-yet")
	if cloneDone(w) {
		t.Error("a destination that does not exist is reported done")
	}
}

// The clone is best effort: a copy that cannot be made costs a convenience,
// not the run. The chain still ends, and the retry is named.
func TestAFailedCloneDoesNotFailTheChain(t *testing.T) {
	fakeRemote(t)
	w := chainWS(t) // its repo dir has no .git, so the clone refuses
	w.Task.CheckoutPath = filepath.Join(t.TempDir(), "mine")
	var out bytes.Buffer
	var ran []string

	done := runPlanChain(&out, w, okRun(&ran), yes)

	if done != len(planStages) {
		t.Fatalf("a failed clone stopped the chain at %d of %d:\n%s", done, len(planStages), out.String())
	}
	if !strings.Contains(out.String(), "orion clone") {
		t.Errorf("the retry command is not named:\n%s", out.String())
	}
}

// --from re-runs the named step and everything after it, done or not;
// steps before it keep the normal rule.
func TestFromRerunsTheNamedStepAndEverythingAfterIt(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	always := func(*workspace.Workspace) bool { return true }
	withPlanStages(t, []planStage{
		{Stage: "intent", Actor: "pm", What: "intent", Done: always},
		{Stage: "spec", Actor: "architect", What: "spec", Done: always},
		{Stage: "plan", Actor: "architect", What: "plan", Done: always},
	})

	done := runPlanChainFrom(&out, chainWS(t), okRun(&ran), yes, "spec")

	if done != 3 {
		t.Fatalf("completed %d of 3:\n%s", done, out.String())
	}
	if strings.Join(ran, ",") != "spec,plan" {
		t.Errorf("ran %v; --from spec must re-run spec and plan and skip the done intent", ran)
	}
}

// An unknown --from name is an error that lists the steps, never "from the
// start" -- a typo must not re-run the whole chain at full cost.
func TestPlanFromIndexRejectsAnUnknownStepAndListsThem(t *testing.T) {
	if i, err := planFromIndex(""); err != nil || i != -1 {
		t.Errorf("empty --from = %d, %v; want -1, nil", i, err)
	}
	if i, err := planFromIndex(" Spec "); err != nil || i < 0 || planStages[i].Stage != "spec" {
		t.Errorf("--from ' Spec ' = %d, %v; want the spec index", i, err)
	}
	_, err := planFromIndex("bogus")
	if err == nil {
		t.Fatal("bogus was accepted")
	}
	for _, s := range planStages {
		if !strings.Contains(err.Error(), s.Stage) {
			t.Errorf("the error does not list %q: %v", s.Stage, err)
		}
	}
}

// The toolkit step has nothing to do for a project that delegates nothing to
// spec-kit -- the chain tests' workspaces are such projects -- and is done
// for a spec-kit project once .specify/ is there and the preset is composed.
func TestTheToolkitStepIsDoneWhenNothingDelegatesToSpecKitOrItIsInstalled(t *testing.T) {
	w := chainWS(t)
	if !toolkitDone(w) {
		t.Error("a project with no spec-kit stages is not reported done")
	}
	if err := os.MkdirAll(w.RepoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.RepoDir(), "orion.json"),
		[]byte(`{"toolkit":{"stages":{"spec":"/speckit-specify"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if toolkitDone(w) {
		t.Error("a spec-kit project with no .specify/ is reported done")
	}
	if err := os.MkdirAll(filepath.Join(w.RepoDir(), ".specify"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Installed but the specify skill was not composed with the orion
	// preset (or an upgrade reinstalled it): not done, so the step re-applies.
	if toolkitDone(w) {
		t.Error("a spec-kit project whose specify skill lacks the orion wrap is reported done")
	}
	// Every skill the preset wraps, not just the first: a project with only
	// some of them composed is out of date, and the step re-applies.
	for _, name := range []string{"speckit-specify", "speckit-tasks"} {
		skill := filepath.Join(w.RepoDir(), ".claude", "skills", name)
		if err := os.MkdirAll(skill, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("## Orion runs this headless\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !toolkitDone(w) {
		t.Error("a spec-kit project with .specify/ and the composed skill is not reported done")
	}
}

// The constitution sits between intent and spec: seeded from the intent's
// constraints, read by every spec-kit command from spec onward.
func TestTheConstitutionSitsBetweenIntentAndSpec(t *testing.T) {
	at := map[string]int{}
	for i, s := range planStages {
		at[s.Stage] = i
	}
	c, ok := at["constitution"]
	if !ok {
		t.Fatal("the chain has no constitution stage")
	}
	if !(at["intent"] < c && c < at["spec"]) {
		t.Errorf("order intent=%d constitution=%d spec=%d; want intent < constitution < spec", at["intent"], c, at["spec"])
	}
	if planStages[c].Done == nil || planStages[c].Frame != nil {
		t.Error("constitution must be a supervised stage with a Done predicate")
	}
}

// analyze sits between plan and scaffold: it checks what plan wrote before
// anything is built from it, and owes no file of its own.
func TestAnalyzeSitsBetweenPlanAndScaffold(t *testing.T) {
	at := map[string]int{}
	for i, s := range planStages {
		at[s.Stage] = i
	}
	a, ok := at["analyze"]
	if !ok {
		t.Fatal("the chain has no analyze stage")
	}
	if !(at["plan"] < a && a < at["scaffold"]) {
		t.Errorf("order plan=%d analyze=%d scaffold=%d; want plan < analyze < scaffold", at["plan"], a, at["scaffold"])
	}
	if planStages[a].Done == nil || planStages[a].Frame != nil {
		t.Error("analyze must be a supervised stage with a Done predicate")
	}
}

// Every line that ends carries the icon column: ✓ on a done or skipped
// step, ✗ on a failed one, ○ when the operator stopped it.
func TestChainLinesCarryTheOutcomeIcon(t *testing.T) {
	fakeRemote(t)
	var out bytes.Buffer
	run := func(_ *workspace.Workspace, stage string) (*supervisor.Result, error) {
		if stage == "spec" {
			return &supervisor.Result{}, fmt.Errorf("boom")
		}
		return &supervisor.Result{ExitCode: 0, Duration: time.Second}, nil
	}
	runPlanChain(&out, chainWS(t), run, yes)
	got := out.String()
	ok, fail := ui.Icon(&out, ui.VerbOK), ui.Icon(&out, ui.VerbFail)
	if !strings.Contains(got, ok+"= done") {
		t.Errorf("a skipped step lacks the ok icon:\n%s", got)
	}
	if !strings.Contains(got, ok+"done") {
		t.Errorf("a done step lacks the ok icon:\n%s", got)
	}
	if !strings.Contains(got, fail+"failed") || !strings.Contains(got, "boom") {
		t.Errorf("the failed step lacks the fail icon:\n%s", got)
	}

	out.Reset()
	asked := 0
	runPlanChain(&out, chainWS(t), okRun(new([]string)), func(string) bool { asked++; return asked < 2 })
	if !strings.Contains(out.String(), ui.Icon(&out, "pending")+"stopped") {
		t.Errorf("a stop at the operator's request lacks the pending icon:\n%s", out.String())
	}
}

// A frame step that did not do what it exists to do -- an unprotected
// branch, a copy that was never made -- must not be reported as done. It
// says so, says how to get it, and asks before the chain goes on (OR-408).
func TestADegradedFrameStepIsNotReportedAsDone(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	withPlanStages(t, []planStage{
		{Stage: "remote", Actor: "orion", What: "the remote",
			Frame: func(*stepIO, *workspace.Workspace) error {
				return degraded("not protected: main (no admin rights)", "orion provision w1")
			}},
		{Stage: "spec", Actor: "architect", What: "the spec"},
	})

	done := runPlanChain(&out, chainWS(t), okRun(&ran), func(string) bool { return true })

	got := out.String()
	if strings.Contains(got, "done  1/2") {
		t.Errorf("a degraded step reported itself done:\n%s", got)
	}
	for _, want := range []string{"not protected", "orion provision w1"} {
		if !strings.Contains(got, want) {
			t.Errorf("the degraded line does not say %q:\n%s", want, got)
		}
	}
	if done != 2 || len(ran) != 1 {
		t.Errorf("the chain did not continue past the degraded step: done=%d ran=%v", done, ran)
	}
}

// Saying no at that question stops the chain, and names `orion plan` as the
// resume -- a frame step is not something `orion run --stage` can run.
func TestDecliningAfterADegradedStepStopsTheChain(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	withPlanStages(t, []planStage{
		{Stage: "clone", Actor: "orion", What: "your copy",
			Frame: func(*stepIO, *workspace.Workspace) error {
				return degraded("no copy was made at /tmp/x", "orion clone w1 <path>")
			}},
		{Stage: "spec", Actor: "architect", What: "the spec"},
	})

	runPlanChain(&out, chainWS(t), okRun(&ran), func(q string) bool {
		return !strings.Contains(q, "did not finish")
	})

	if len(ran) != 0 {
		t.Errorf("the chain ran on after being told to stop: %v", ran)
	}
	if !strings.Contains(out.String(), "orion plan") {
		t.Errorf("the resume line must name `orion plan`:\n%s", out.String())
	}
}

// A clone onto a path that cannot be used used to warn and report the
// step done. It now offers another path, and takes it (OR-408).
func TestCloneStepOffersAnotherPathWhenTheFirstIsTaken(t *testing.T) {
	ws := chainWS(t)
	// A FILE, not a directory: a directory that exists is now a parent to
	// clone into (OR-418), so it is no longer a refusal at all.
	taken := filepath.Join(t.TempDir(), "in-the-way")
	if err := os.WriteFile(taken, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws.Task.CheckoutPath = taken

	// Point the second answer at a git repo we can actually clone.
	src := ws.RepoDir()
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"commit", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", append([]string{"-C", src}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@l",
			"GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@l")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v\n%s", err, b)
		}
	}
	want := filepath.Join(t.TempDir(), "copy")

	var out bytes.Buffer
	err := cloneStep(&stepIO{Out: &out, Ask: func(string) string { return want }}, ws)

	if err != nil {
		t.Fatalf("the retry did not take: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(want, ".git")); err != nil {
		t.Errorf("nothing was cloned to the second path: %v", err)
	}
	if ws.Task.CheckoutPath != want {
		t.Errorf("the new path was not recorded: %q", ws.Task.CheckoutPath)
	}
}

// With nowhere to ask -- a non-interactive run -- it reports degraded
// rather than claiming the copy was made.
func TestCloneStepReportsDegradedWithNoOneToAsk(t *testing.T) {
	ws := chainWS(t)
	taken := filepath.Join(t.TempDir(), "in-the-way")
	if err := os.WriteFile(taken, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws.Task.CheckoutPath = taken

	var out bytes.Buffer
	err := cloneStep(&stepIO{Out: &out}, ws)

	var deg *Degraded
	if !errors.As(err, &deg) {
		t.Fatalf("a failed clone reported %v, want a degraded outcome", err)
	}
	if !strings.Contains(deg.Fix, "orion clone") {
		t.Errorf("the fix does not name the command: %q", deg.Fix)
	}
}

// A stage's model can check out a branch of its own, and nothing stops it.
// The chain says so and records where the stage left the sandbox, rather
// than letting every later stage commit somewhere nobody chose (OR-405).
func TestAStageThatChangesTheBranchIsReported(t *testing.T) {
	ws := chainWS(t)
	repo := ws.RepoDir()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@l",
			"GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@l")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v\n%s", err, b)
		}
	}
	git("init", "-b", "develop")
	git("commit", "--allow-empty", "-m", "x")

	withPlanStages(t, []planStage{{Stage: "spec", Actor: "architect", What: "the spec"}})
	var out bytes.Buffer
	wandered := func(_ *workspace.Workspace, _ string) (*supervisor.Result, error) {
		git("checkout", "-q", "-b", "somewhere-else")
		return &supervisor.Result{}, nil
	}

	runPlanChain(&out, ws, wandered, func(string) bool { return true })

	got := out.String()
	for _, want := range []string{"somewhere-else", "develop"} {
		if !strings.Contains(got, want) {
			t.Errorf("the warning does not name %q:\n%s", want, got)
		}
	}
	if ws.Task.PlanBranch != "somewhere-else" {
		t.Errorf("the branch the stage left behind was not recorded: %q", ws.Task.PlanBranch)
	}
}

// The remote step pushes main and develop and returns; every stage after
// it commits to the sandbox only. So the clone step sends the branch the
// chain has been committing to before making the copy -- otherwise the
// copy has none of the work in it (OR-418).
func TestCloneStepSendsTheChainsWorkBeforeCopyingIt(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "origin.git")
	mustGit(t, "", "init", "--bare", "-b", "main", bare)

	ws := chainWS(t)
	repo := ws.RepoDir()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-b", "main")
	mustGit(t, repo, "remote", "add", "origin", bare)
	mustGit(t, repo, "commit", "--allow-empty", "-m", "what the stages committed")
	ws.Task.Remote = bare
	ws.Task.CheckoutPath = filepath.Join(t.TempDir(), "copy")

	var out bytes.Buffer
	if err := cloneStep(&stepIO{Out: &out}, ws); err != nil {
		t.Fatalf("clone step failed: %v\n%s", err, out.String())
	}

	got := gitLineIn(t, bare, "log", "--oneline", "-1", "main")
	if !strings.Contains(got, "what the stages committed") {
		t.Errorf("the chain's commit never reached the remote: %q", got)
	}
	if _, err := os.Stat(filepath.Join(ws.Task.CheckoutPath, ".git")); err != nil {
		t.Errorf("no copy was made: %v", err)
	}
}

// mustGit runs one git command, skipping the test when git is unusable.
func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@l",
		"GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@l")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v\n%s", err, b)
	}
}
