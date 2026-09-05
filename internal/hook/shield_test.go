package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

func edit(path string) Input {
	b, _ := json.Marshal(map[string]string{"file_path": path})
	return Input{HookEventName: "PreToolUse", ToolName: "Edit", ToolInput: b}
}

func shieldCfg(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	c := config.Defaults()
	c.Root = root
	c.Paths.Protected = []string{".github/workflows/**", "orion.json"}
	c.Paths.State = filepath.Join(root, ".orion", "state")
	return c
}

func TestShieldProtectedPaths(t *testing.T) {
	cfg := shieldCfg(t)

	for _, p := range []string{".github/workflows/ci.yml", "orion.json"} {
		d := Shield(edit(filepath.Join(cfg.Root, p)), cfg)
		if !d.Blocked() {
			t.Errorf("%s must be protected: an agent that can edit its own controls has none", p)
		}
	}
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); d.Blocked() {
		t.Errorf("ordinary source must be editable, got: %s", d.Msg)
	}
}

func TestShieldTestProtectionOnlyDuringFix(t *testing.T) {
	cfg := shieldCfg(t)
	testFile := edit(filepath.Join(cfg.Root, "src/test_thing.py"))

	// Outside a fix, editing a test is ordinary work.
	if d := Shield(testFile, cfg); d.Blocked() {
		t.Fatalf("tests must be editable outside a fix, got: %s", d.Msg)
	}

	if err := os.MkdirAll(cfg.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StateDir(), FixModeMarker), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := Shield(testFile, cfg)
	if !d.Blocked() {
		t.Fatal("during a fix the failing test defines done and must be read-only")
	}
	if !strings.Contains(d.Msg, "goalposts") {
		t.Errorf("block should explain why, got: %s", d.Msg)
	}
	// Source stays editable during a fix, otherwise nothing could be fixed.
	if d := Shield(edit(filepath.Join(cfg.Root, "src/thing.py")), cfg); d.Blocked() {
		t.Errorf("source must stay editable during a fix, got: %s", d.Msg)
	}
}

func TestShieldPlanGate(t *testing.T) {
	cfg := shieldCfg(t)
	cfg.Gates.RequirePlanBeforeEdit = true

	d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg)
	if !d.Blocked() {
		t.Fatal("with the plan gate on, implementation before a plan must be refused")
	}

	// The artifacts themselves must stay writable or the gate would make
	// producing the required plan impossible.
	for _, p := range []string{"plans/x.plan.md", "docs/intent/x.md", "specs/x.spec.md", "CLAUDE.md"} {
		if d := Shield(edit(filepath.Join(cfg.Root, p)), cfg); d.Blocked() {
			t.Errorf("%s must remain writable under the plan gate, got: %s", p, d.Msg)
		}
	}

	if err := os.MkdirAll(filepath.Join(cfg.Root, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "plans", "x.plan.md"), []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); d.Blocked() {
		t.Errorf("with a plan committed, implementation must proceed, got: %s", d.Msg)
	}
}

// planExists must say no both when the plans directory was never created and
// when it exists but holds nothing recognisable as a plan -- two distinct
// ways to have "no plan yet" that must not be conflated with "has a plan".
func TestPlanExistsFalseWhenDirectoryMissingOrEmpty(t *testing.T) {
	cfg := shieldCfg(t)

	if planExists(cfg) {
		t.Error("no plans directory at all must not count as a plan existing")
	}

	if err := os.MkdirAll(filepath.Join(cfg.Root, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if planExists(cfg) {
		t.Error("an empty plans directory must not count as a plan existing")
	}
}

// Any file ending in the configured plan extension, sitting in the plans
// directory, must be enough -- planExists answers "has this project produced
// a plan", not "does a specific one exist".
func TestPlanExistsTrueForAnyPlanFile(t *testing.T) {
	cfg := shieldCfg(t)

	if err := os.MkdirAll(filepath.Join(cfg.Root, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "plans", "whatever.plan.md"),
		[]byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !planExists(cfg) {
		t.Error("a *.plan.md file in the plans directory must make planExists true")
	}
}

// The gate opens on ANY plan file, not one named after the current task: it
// sees a directory, not a slug, so a plan written under a different name must
// still satisfy it.
func TestShieldPlanGateRecognisesByExtensionRegardlessOfSlug(t *testing.T) {
	cfg := shieldCfg(t)
	cfg.Gates.RequirePlanBeforeEdit = true

	if err := os.MkdirAll(filepath.Join(cfg.Root, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "plans", "completely-unrelated-name.plan.md"),
		[]byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); d.Blocked() {
		t.Errorf("a .plan.md file present under any name must open the gate, got: %s", d.Msg)
	}
}

// A project that moves paths.plans off the default must have the gate follow
// it there rather than continuing to look in plans/.
func TestShieldPlanGateUsesConfiguredPlansDirectory(t *testing.T) {
	cfg := shieldCfg(t)
	cfg.Gates.RequirePlanBeforeEdit = true
	cfg.Paths.Plans = "docs/plans"

	// A plan sitting in the OLD default location must not satisfy a gate
	// configured to look elsewhere.
	if err := os.MkdirAll(filepath.Join(cfg.Root, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "plans", "x.plan.md"), []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); !d.Blocked() {
		t.Fatal("a plan in the unconfigured default plans/ must not open a gate configured for docs/plans")
	}

	// Writing it where the config actually points must open the gate.
	if err := os.MkdirAll(filepath.Join(cfg.Root, "docs", "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "docs", "plans", "x.plan.md"),
		[]byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := Shield(edit(filepath.Join(cfg.Root, "src/main.go")), cfg); d.Blocked() {
		t.Errorf("a plan written at the configured docs/plans must open the gate, got: %s", d.Msg)
	}
}

func TestShieldIgnoresEmptyPath(t *testing.T) {
	cfg := shieldCfg(t)
	in := Input{HookEventName: "PreToolUse", ToolName: "Edit", ToolInput: json.RawMessage(`{}`)}
	if d := Shield(in, cfg); d.Blocked() {
		t.Error("a tool call with no file path is not something shield can judge")
	}
}
