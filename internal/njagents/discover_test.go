package njagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OR-297: Validate/Discover coverage for the derived-requirements change.
// writeToolkit and skillsOf are shared with required_test.go in this package.

// writeToolkitAt is writeToolkit but at a caller-chosen path, for cases that
// need to control WHERE the checkout lands (vendor dir naming, Discover
// candidate ordering) rather than accepting a fresh t.TempDir() each time.
func writeToolkitAt(t *testing.T, root string, skills ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range skills {
		dir := filepath.Join(root, "skills", s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidateRandomEmptyDirectoryReturnsNilNotEmptyInstall(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/only"}}
	if inst := Validate(t.TempDir(), tk); inst != nil {
		t.Fatalf("Validate(empty dir) = %+v, want nil", inst)
	}
}

func TestValidateDirWithSkillsButMissingConfiguredSkillIsIncompleteNotNil(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/needed"}}
	root := writeToolkit(t) // skills/ exists, empty

	inst := Validate(root, tk)
	if inst == nil {
		t.Fatal("Validate(skills/ present, skill missing) = nil, want an incomplete Install")
	}
	if len(inst.Missing) != 1 || !strings.Contains(inst.Missing[0], "needed") ||
		!strings.Contains(inst.Missing[0], "review stage") {
		t.Errorf("Missing = %v, want the missing skill annotated with its stage", inst.Missing)
	}
}

func TestValidateMissingSkillForDefaultToolkitHasNoStageAnnotation(t *testing.T) {
	root := writeToolkit(t) // no skills at all

	inst := Validate(root, Toolkit{})
	if inst == nil {
		t.Fatal("Validate returned nil for an incomplete default toolkit checkout")
	}
	for _, m := range inst.Missing {
		if m == "CONVENTIONS.md" {
			continue
		}
		if !strings.HasPrefix(m, "skills/") || strings.Contains(m, "required by") {
			t.Errorf("Missing entry %q, want a bare skills/<name> with no stage annotation", m)
		}
	}
}

func TestValidateMissingSkillForConfiguredStageIsAnnotated(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"pr": "/their-pr"}}
	root := writeToolkit(t)

	inst := Validate(root, tk)
	if inst == nil {
		t.Fatal("Validate returned nil")
	}
	want := "skills/their-pr (required by the pr stage)"
	if len(inst.Missing) != 1 || inst.Missing[0] != want {
		t.Errorf("Missing = %v, want [%q]", inst.Missing, want)
	}
}

func TestValidateInstallShMissingIsWarningOnlyForDefaultToolkit(t *testing.T) {
	root := writeToolkit(t, "pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project")
	if err := os.WriteFile(filepath.Join(root, "CONVENTIONS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := Validate(root, Toolkit{})
	if inst == nil {
		t.Fatal("Validate returned nil for an otherwise complete default checkout")
	}
	if len(inst.Missing) != 0 {
		t.Errorf("Missing = %v, want empty: install.sh must not fail validation", inst.Missing)
	}
	if len(inst.Warnings) != 1 || !strings.Contains(inst.Warnings[0], "install.sh") {
		t.Errorf("Warnings = %v, want a single install.sh warning", inst.Warnings)
	}
}

func TestValidateInstallShMissingDoesNotWarnForForeignToolkit(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/only"}}
	root := writeToolkit(t, "only")

	inst := Validate(root, tk)
	if inst == nil || !inst.OK() {
		t.Fatalf("Validate = %+v, want a healthy foreign toolkit with no install.sh", inst)
	}
	if len(inst.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none for a foreign toolkit missing install.sh", inst.Warnings)
	}
}

func TestValidateInstallShPresentDoesNotWarnForEitherToolkit(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/only"}}
	foreignRoot := writeToolkit(t, "only")
	if err := os.WriteFile(filepath.Join(foreignRoot, "install.sh"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if inst := Validate(foreignRoot, tk); inst == nil || len(inst.Warnings) != 0 {
		t.Errorf("foreign toolkit with install.sh present: Warnings = %+v, want none", inst)
	}

	defaultRoot := writeToolkit(t, "pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project")
	if err := os.WriteFile(filepath.Join(defaultRoot, "CONVENTIONS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultRoot, "install.sh"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if inst := Validate(defaultRoot, Toolkit{}); inst == nil || len(inst.Warnings) != 0 {
		t.Errorf("default toolkit with install.sh present: Warnings = %+v, want none", inst)
	}
}

// -- isToolkitRoot and hasSkillsDir --------------------------------------

func TestIsToolkitRootDefaultToolkitFailsWithoutConventionsEvenWithSkills(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isToolkitRoot(root, Toolkit{}) {
		t.Error("isToolkitRoot(default, no CONVENTIONS.md) = true, want false")
	}
}

func TestIsToolkitRootForeignToolkitPassesWithSkillsRegardlessOfConventions(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git"}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isToolkitRoot(root, tk) {
		t.Error("isToolkitRoot(foreign, skills/ present, no CONVENTIONS.md) = false, want true")
	}
}

func TestIsToolkitRootBothFailWithoutSkillsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CONVENTIONS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isToolkitRoot(root, Toolkit{}) {
		t.Error("isToolkitRoot(default, no skills/) = true, want false")
	}
	if isToolkitRoot(root, Toolkit{Repo: "https://example.com/kit.git"}) {
		t.Error("isToolkitRoot(foreign, no skills/) = true, want false")
	}
}

func TestHasSkillsDirFalseForFileNamedSkills(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "skills"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hasSkillsDir(root) {
		t.Error("hasSkillsDir(file named skills) = true, want false")
	}
}

// -- Discover: locating toolkits ------------------------------------------

func TestDiscoverFindsAndValidatesToolkitAtConfiguredDir(t *testing.T) {
	toolkitRoot := t.TempDir()
	writeToolkitAt(t, toolkitRoot, "only")
	tk := Toolkit{Repo: "https://example.com/kit.git", Dir: toolkitRoot,
		Stages: map[string]string{"review": "/only"}}

	inst := Discover(t.TempDir(), tk)
	if inst == nil || inst.Root != toolkitRoot {
		t.Fatalf("Discover = %+v, want the configured Toolkit.Dir %q", inst, toolkitRoot)
	}
	if inst.Via != "configured" {
		t.Errorf("Via = %q, want %q", inst.Via, "configured")
	}
	if !inst.OK() {
		t.Errorf("Discover found an incomplete Install: %+v", inst)
	}
}

func TestDiscoverUsesConfiguredRepoForVendorDirNamingWithoutOverwritingDefault(t *testing.T) {
	orionHome := t.TempDir()
	foreign := Toolkit{Repo: "https://github.com/acme/house-skills.git",
		Stages: map[string]string{"review": "/only"}}
	foreignVendor := VendorDirFor(orionHome, foreign.Repo)
	writeToolkitAt(t, foreignVendor, "only")

	inst := Discover(orionHome, foreign)
	if inst == nil || inst.Root != foreignVendor {
		t.Fatalf("Discover = %+v, want the foreign repo's own vendor leaf %q", inst, foreignVendor)
	}
	if defaultVendor := VendorDir(orionHome); inst.Root == defaultVendor {
		t.Errorf("foreign toolkit resolved to nj-agents' own vendor directory %q", defaultVendor)
	}
}

func TestFromRunnerSymlinkProbesOnlyConfiguredSkillNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configuredRoot := t.TempDir()
	writeToolkitAt(t, configuredRoot, "custom-skill")
	unconfiguredRoot := t.TempDir()
	writeToolkitAt(t, unconfiguredRoot, "pre-push-review")

	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(configuredRoot, "skills", "custom-skill"),
		filepath.Join(skillsDir, "custom-skill")); err != nil {
		t.Fatal(err)
	}
	// A default-set skill link exists too, pointing at a DIFFERENT, otherwise
	// valid toolkit. If the built-in names were probed alongside the
	// configured ones, this is the root that would wrongly come back.
	if err := os.Symlink(filepath.Join(unconfiguredRoot, "skills", "pre-push-review"),
		filepath.Join(skillsDir, "pre-push-review")); err != nil {
		t.Fatal(err)
	}

	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/custom-skill"}}
	got := fromRunnerSymlink(tk)
	if got != configuredRoot {
		t.Errorf("fromRunnerSymlink = %q, want the configured skill's root %q (not the unconfigured %q)",
			got, configuredRoot, unconfiguredRoot)
	}
}

func TestDiscoverReturnsFirstCompleteCandidateAcrossSearchPaths(t *testing.T) {
	orionHome := t.TempDir()
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/only"}}

	incompleteDir := t.TempDir()
	writeToolkitAt(t, incompleteDir) // skills/ exists but ships nothing
	tk.Dir = incompleteDir

	completeDir := t.TempDir()
	writeToolkitAt(t, completeDir, "only")
	t.Setenv("ORION_NJ_AGENTS_DIR", completeDir)

	inst := Discover(orionHome, tk)
	if inst == nil || inst.Root != completeDir {
		t.Fatalf("Discover = %+v, want the first COMPLETE candidate %q, not the incomplete configured Dir %q",
			inst, completeDir, incompleteDir)
	}
	if !inst.OK() {
		t.Errorf("Discover's chosen candidate is not OK: %+v", inst)
	}
}
