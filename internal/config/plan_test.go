package config

import "testing"

// PlanPath is the single definition of where a plan lives. Both the prompts
// that ask an agent to write a plan and the shield's gate that recognises one
// resolve through it, so this pins the join it performs directly rather than
// through either caller.
func TestPlanPathJoinsSlugWithConfiguredPlansDir(t *testing.T) {
	cfg := Config{Paths: Paths{Plans: "docs/plans"}}
	got := cfg.PlanPath("thing")
	want := "docs/plans/thing.plan.md"
	if got != want {
		t.Errorf("PlanPath(%q) = %q, want %q", "thing", got, want)
	}
}

// The result is repository spelling, not filesystem spelling: it is rendered
// into prompt text an agent reads and a path an agent types into the repo,
// where the separator is always a forward slash -- regardless of the OS this
// runs on.
func TestPlanPathUsesForwardSlashes(t *testing.T) {
	cfg := Config{Paths: Paths{Plans: "docs/plans"}}
	got := cfg.PlanPath("my-task")
	want := "docs/plans/my-task.plan.md"
	if got != want {
		t.Errorf("PlanPath returned %q, want the forward-slash form %q", got, want)
	}
}

// Every plan file PlanPath names must satisfy the suffix the gate looks for
// (config.PlanExt, internal/hook.planExists) -- the invariant that lets the
// gate recognise what the prompt just told the agent to write.
func TestPlanPathEndsInPlanExt(t *testing.T) {
	cfg := Config{Paths: Paths{Plans: "plans"}}
	got := cfg.PlanPath("thing")
	if got != "plans/thing"+PlanExt {
		t.Errorf("PlanPath(%q) = %q, want suffix %q", "thing", got, PlanExt)
	}
}
