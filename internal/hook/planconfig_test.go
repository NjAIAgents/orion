package hook

import (
	"os"
	"path/filepath"
	"testing"
)

// With the plan gate off, an edit must succeed no matter where -- or
// whether -- a plan file exists, custom paths.plans included. The gate is
// the only thing that ever looks at plan location; disabled, that location
// stops mattering.
func TestShieldPlanGateDisabledIgnoresPlanLocationEvenWithCustomPath(t *testing.T) {
	cfg := shieldCfg(t)
	cfg.Gates.RequirePlanBeforeEdit = false
	cfg.Paths.Plans = "docs/plans"

	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); d.Blocked() {
		t.Errorf("gate disabled, no plan anywhere: edit must succeed, got: %s", d.Msg)
	}

	// Even with a configured plans directory present but empty.
	if err := os.MkdirAll(filepath.Join(cfg.Root, "docs", "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); d.Blocked() {
		t.Errorf("gate disabled, empty configured plans dir: edit must succeed, got: %s", d.Msg)
	}
}

// With the gate on and a custom paths.plans, edits stay blocked until a plan
// exists at THAT configured path -- not the default plans/, and not merely
// any directory existing.
func TestShieldPlanGateEnabledCustomPathBlocksUntilPlanExists(t *testing.T) {
	cfg := shieldCfg(t)
	cfg.Gates.RequirePlanBeforeEdit = true
	cfg.Paths.Plans = "docs/plans"

	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); !d.Blocked() {
		t.Fatal("gate enabled, no plan written yet: edit must be refused")
	}

	if err := os.MkdirAll(filepath.Join(cfg.Root, "docs", "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); !d.Blocked() {
		t.Fatal("gate enabled, configured dir exists but empty: edit must still be refused")
	}

	if err := os.WriteFile(filepath.Join(cfg.Root, "docs", "plans", "x.plan.md"),
		[]byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); d.Blocked() {
		t.Errorf("gate enabled, plan written at configured path: edit must succeed, got: %s", d.Msg)
	}
}

// Changing paths.plans after a plan was already written must not move, copy
// or otherwise disturb that file -- config is read at gate-check time, it
// does not reach back and relocate anything already on disk.
func TestConfigChangeDoesNotMoveExistingPlan(t *testing.T) {
	cfg := shieldCfg(t)
	cfg.Gates.RequirePlanBeforeEdit = true

	if err := os.MkdirAll(filepath.Join(cfg.Root, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(cfg.Root, "plans", "x.plan.md")
	if err := os.WriteFile(original, []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The plan opens the gate under the config it was written with.
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); d.Blocked() {
		t.Fatalf("plan at the configured default location must open the gate, got: %s", d.Msg)
	}

	// Reconfiguring paths.plans must leave the file exactly where it was.
	cfg.Paths.Plans = "docs/plans"
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("the original plan file must still exist untouched, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, "docs", "plans", "x.plan.md")); err == nil {
		t.Fatal("the plan must not have been copied or moved to the newly configured path")
	}

	// And the gate now looks where the config points, finding nothing there.
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); !d.Blocked() {
		t.Fatal("after reconfiguring paths.plans, the gate must look at the new path and find no plan")
	}
}
