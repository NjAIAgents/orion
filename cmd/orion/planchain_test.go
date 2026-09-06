package main

import (
	"bufio"
	"bytes"
	"fmt"
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
