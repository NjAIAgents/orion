package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/hook"
)

// planNamed finds the plan file a prompt tells the agent to write or read.
//
// The prompt is parsed rather than the path recomputed from config.PlanPath,
// which is the point of the test: if both sides derived the answer from the
// helper, a prompt that stopped calling it would still agree with the gate
// here and the drift this test exists to catch would pass.
var planNamed = regexp.MustCompile(`[^\s]+\.plan\.md`)

func planFromPrompt(t *testing.T, prompt, stage string) string {
	t.Helper()
	m := planNamed.FindString(prompt)
	if m == "" {
		t.Fatalf("the %s prompt names no plan file:\n%s", stage, prompt)
	}
	return strings.TrimSuffix(m, ".")
}

// The two sides of the plan artifact held together: the file the plan stage's
// prompt asks for must be the file the shield's plan gate opens on.
//
// One test rather than two, deliberately. A test that mocks the gate to check
// the prompt, and another that mocks the prompt to check the gate, both pass
// while a project with a non-default paths.plans has a plan stage writing
// somewhere the gate never looks -- a run that did exactly what it was told
// and is then refused every edit it attempts.
//
// It fails if either side is changed alone: hardcode the directory back into
// the prompt and the plan lands outside the configured directory the gate
// reads; change the suffix on either side and the gate stops recognising the
// file the prompt named.
func TestPlanPromptWritesWhatThePlanGateOpensOn(t *testing.T) {
	w := ws(t, `{"paths":{"plans":"docs/plans"},
		"gates":{"require_plan_before_edit":true}}`)
	cfg := config.Load(w.RepoDir())
	if cfg.Paths.Plans != "docs/plans" || !cfg.Gates.RequirePlanBeforeEdit {
		t.Fatalf("test config did not load: plans=%q gate=%v",
			cfg.Paths.Plans, cfg.Gates.RequirePlanBeforeEdit)
	}

	source := edit(t, filepath.Join(w.RepoDir(), "src", "main.go"))

	// The gate must actually be shut first, or everything below passes on a
	// shield that permits every edit.
	if d := hook.Shield(source, cfg); !d.Blocked() {
		t.Fatal("with the plan gate on and no plan written, the edit must be refused")
	}

	// The empty Toolkit is today's default configuration: OR-298 gave
	// stagePrompt a toolkit block so a project can name its own commands,
	// and an unset one keeps the built-in prompt. This test is about the
	// PLAN PATH, so it takes the default.
	prompt, err := stagePrompt(w, "plan", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	named := planFromPrompt(t, prompt, "plan")
	if !strings.HasPrefix(named, "docs/plans/") {
		t.Fatalf("the plan prompt must name the configured directory, got %q", named)
	}

	// Write the plan exactly where the prompt said to, and nowhere else.
	at := filepath.Join(w.RepoDir(), filepath.FromSlash(named))
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at, []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if d := hook.Shield(source, cfg); d.Blocked() {
		t.Fatalf("a plan written where the prompt asked for it must open the gate, got: %s", d.Msg)
	}
}

// The three stages that READ the plan must name the same file the plan stage
// wrote. A decompose or build prompt pointing at a path nothing ever wrote is
// the same silent break as a gate looking in the wrong directory.
func TestEveryStageNamesTheSamePlanFile(t *testing.T) {
	w := ws(t, `{"paths":{"plans":"docs/plans"}}`)
	want := config.Load(w.RepoDir()).PlanPath(w.Task.Slug)

	for _, stage := range []string{"plan", "decompose", "build", "verify"} {
		p, err := stagePrompt(w, stage, config.Toolkit{})
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		// A hardcoded plans/ site fails here: it yields plans/thing.plan.md
		// against a configured docs/plans/thing.plan.md.
		if got := planFromPrompt(t, p, stage); got != want {
			t.Errorf("%s names %q, want %q", stage, got, want)
		}
	}
}

// The build and verify prompts must not merely NAME the same path the plan
// stage names -- they must name a file that actually exists once the plan
// stage has written it. Comparing two rendered strings (as
// TestEveryStageNamesTheSamePlanFile does) would still pass if both sides
// happened to agree on a path nothing ever wrote to; this checks the
// filesystem instead.
func TestVerifyAndBuildPointAtAFileThePlanStageWrote(t *testing.T) {
	w := ws(t, `{"paths":{"plans":"docs/plans"}}`)
	cfg := config.Load(w.RepoDir())

	planPrompt, err := stagePrompt(w, "plan", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	named := planFromPrompt(t, planPrompt, "plan")

	at := filepath.Join(w.RepoDir(), filepath.FromSlash(named))
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at, []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, stage := range []string{"build", "verify"} {
		p, err := stagePrompt(w, stage, config.Toolkit{})
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		got := planFromPrompt(t, p, stage)
		if got != named {
			t.Errorf("%s names %q, but the plan stage wrote %q", stage, got, named)
			continue
		}
		if _, err := os.Stat(filepath.Join(w.RepoDir(), filepath.FromSlash(got))); err != nil {
			t.Errorf("%s names %q, which the plan stage never wrote: %v", stage, got, err)
		}
	}

	// cfg is loaded again here only to confirm the test's own setup landed
	// the plan under the configured directory, not the default.
	if !strings.HasPrefix(named, cfg.Paths.Plans+"/") {
		t.Fatalf("plan written outside the configured directory: %q vs %q", named, cfg.Paths.Plans)
	}
}

// Default config keeps today's behaviour: plans/, unchanged.
func TestPlanPathDefaultsToPlans(t *testing.T) {
	w := ws(t, "")
	p, err := stagePrompt(w, "plan", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if got := planFromPrompt(t, p, "plan"); got != "plans/thing.plan.md" {
		t.Errorf("default plan path changed: got %q", got)
	}
}

func edit(t *testing.T, path string) hook.Input {
	t.Helper()
	b, err := json.Marshal(map[string]string{"file_path": path})
	if err != nil {
		t.Fatal(err)
	}
	return hook.Input{HookEventName: "PreToolUse", ToolName: "Edit", ToolInput: b}
}
