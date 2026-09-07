package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// A configured command must reach the prompt, or configuring it does nothing.
//
// intent, scaffold and decompose named their command inline because each had
// a built-in one to name. spec and plan had none, so their prompts mentioned
// no command at all -- and a project declaring "spec": "/speckit.specify"
// got a prompt that never said so. The config loaded, `orion doctor` reported
// the toolkit healthy, and the stage ran Orion's own prompt regardless: three
// green signals and the configuration having no effect.
func TestSpecAndPlanUseTheirConfiguredCommand(t *testing.T) {
	w := ws(t, `{}`)
	w.Task.Slug = "thing"
	tk := config.Toolkit{Stages: map[string]string{
		"spec": "/speckit.specify",
		"plan": "/speckit.plan",
	}}

	for stage, want := range map[string]string{
		"spec": "/speckit.specify",
		"plan": "/speckit.plan",
	} {
		p, err := stagePrompt(w, stage, tk)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if !strings.Contains(p, want) {
			t.Errorf("the %s prompt never names %s:\n%s", stage, want, p)
		}
	}
}

// With no toolkit configured, both prompts stay exactly as they were: these
// two stages have no built-in skill to fall back to, so the absence of a
// command is the normal state rather than a gap.
func TestWithNoToolkitTheseStagesNameNoCommand(t *testing.T) {
	w := ws(t, `{}`)
	w.Task.Slug = "thing"

	for _, stage := range []string{"spec", "plan"} {
		p, err := stagePrompt(w, stage, config.Toolkit{})
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if strings.Contains(p, "Use  for this stage") || strings.Contains(p, "Use /") {
			t.Errorf("the unconfigured %s prompt names a command:\n%s", stage, p)
		}
	}
}

// /speckit.plan writes plan.md, research.md, data-model.md and contracts --
// NOT tasks.md, which is /speckit.tasks' job and which `orion decompose`
// reads from specs/<nnn>/tasks.md.
//
// Configuring the plan stage without asking for that handoff leaves the chain
// with a plan and nothing to decompose from, which surfaces two stages later
// as "no specs/*/tasks.md here".
func TestAConfiguredPlanStageIsAskedForTheTaskList(t *testing.T) {
	w := ws(t, `{}`)
	w.Task.Slug = "thing"

	p, err := stagePrompt(w, "plan", config.Toolkit{
		Stages: map[string]string{"plan": "/speckit.plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tasks.md", "/speckit.tasks", "decompose"} {
		if !strings.Contains(p, want) {
			t.Errorf("the configured plan prompt never mentions %q:\n%s", want, p)
		}
	}
}

// And an unconfigured plan stage is not asked for one: Orion's own decompose
// reads plans/<slug>.plan.md, so a task list would be an artifact nothing
// consumes.
func TestAnUnconfiguredPlanStageIsNotAskedForATaskList(t *testing.T) {
	w := ws(t, `{}`)
	w.Task.Slug = "thing"

	p, err := stagePrompt(w, "plan", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "tasks.md") {
		t.Errorf("an unconfigured plan stage was asked for a task list:\n%s", p)
	}
}
