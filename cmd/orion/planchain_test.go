package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func chainWS() *workspace.Workspace { return &workspace.Workspace{ID: "cloudlens"} }

// okRun is a stage that succeeds, recording the order it was asked for.
func okRun(order *[]string) stageRunner {
	return func(_ *workspace.Workspace, stage string) (*supervisor.Result, error) {
		*order = append(*order, stage)
		return &supervisor.Result{ExitCode: 0, Duration: time.Second, LogPath: "/tmp/x.log"}, nil
	}
}

func yes(string) bool { return true }

func TestTheChainRunsEveryStageInRosterOrder(t *testing.T) {
	var order []string
	var out bytes.Buffer

	done := runPlanChain(&out, chainWS(), okRun(&order), yes)

	if done != len(planStages) {
		t.Fatalf("completed %d of %d stages:\n%s", done, len(planStages), out.String())
	}
	want := make([]string, 0, len(planStages))
	for _, s := range planStages {
		want = append(want, s.Stage)
	}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("ran %v, want the roster order %v", order, want)
	}
}

// The reason the chain pauses at all: a stage's artifact is what the next
// stage designs from, so the operator reads it before paying for the one that
// consumes it.
func TestDecliningTheNextStageStopsTheChain(t *testing.T) {
	var order []string
	var out bytes.Buffer

	// Yes to the first continue, no to the second.
	asked := 0
	ask := func(string) bool { asked++; return asked < 2 }

	done := runPlanChain(&out, chainWS(), okRun(&order), ask)

	if done != 2 {
		t.Fatalf("completed %d stages, want 2:\n%s", done, out.String())
	}
	if len(order) != 2 {
		t.Errorf("ran %v; a declined stage must not run", order)
	}
	if !strings.Contains(out.String(), "at your request") {
		t.Errorf("a declined chain must say it stopped deliberately:\n%s", out.String())
	}
	// Naming the stage it stopped BEFORE is what makes it resumable.
	if !strings.Contains(out.String(), "--stage "+planStages[2].Stage) {
		t.Errorf("output does not name the resume command:\n%s", out.String())
	}
}

// CloudLens. The spec stage could not find the intent it was to design from,
// refused, and said so in its artifact. Every stage after it would have
// designed from a document that says it is not a design.
func TestAStageThatFailsStopsTheChainAndNamesTheFix(t *testing.T) {
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

	done := runPlanChain(&out, chainWS(), run, yes)

	// intent runs and succeeds; spec blocks. So exactly one stage completed,
	// and nothing after spec ran at all.
	if done != 1 {
		t.Fatalf("completed %d stages, want 1 (intent) before spec blocked:\n%s", done, out.String())
	}
	if strings.Join(ran, ",") != "intent,spec" {
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
	var order []string
	var out bytes.Buffer
	asked := 0

	runPlanChain(&out, chainWS(), okRun(&order), func(string) bool { asked++; return true })

	if want := len(planStages) - 1; asked != want {
		t.Errorf("asked %d times, want %d -- one per stage AFTER the first", asked, want)
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
	if planStages[0].Stage != "intent" {
		t.Errorf("the chain starts with %q; every later stage reads what intent writes",
			planStages[0].Stage)
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
			Frame: func(io.Writer, *workspace.Workspace, confirmer) error { framed++; return nil }},
		{Stage: "spec", Actor: "architect", What: "spec"},
	})

	done := runPlanChain(&out, chainWS(), okRun(&ran), yes)

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

	done := runPlanChain(&out, chainWS(), okRun(&ran), ask)

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
			Frame: func(io.Writer, *workspace.Workspace, confirmer) error {
				return fmt.Errorf("gh repo create failed: not logged in")
			}},
		{Stage: "spec", Actor: "architect", What: "spec"},
	})

	done := runPlanChain(&out, chainWS(), okRun(&ran), yes)

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
		{Stage: "remote", Actor: "orion", Frame: func(io.Writer, *workspace.Workspace, confirmer) error { return nil }},
		{Stage: "decompose", Actor: "pm"},
		{Stage: "clone", Actor: "orion", Frame: func(io.Writer, *workspace.Workspace, confirmer) error { return nil }},
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
			Frame: func(io.Writer, *workspace.Workspace, confirmer) error { return nil }},
	})
	no := func(string) bool { return false }

	done := runPlanChain(&out, chainWS(), okRun(&ran), no)

	if done != 1 {
		t.Fatalf("completed %d steps, want 1:\n%s", done, out.String())
	}
	if !strings.Contains(out.String(), "orion plan") || strings.Contains(out.String(), "--stage remote") {
		t.Errorf("the resume line must be `orion plan`, never `orion run --stage remote`:\n%s", out.String())
	}
}
