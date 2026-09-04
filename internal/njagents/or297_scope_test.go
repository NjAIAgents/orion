package njagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OR-297: cases assigned to this writer. writeToolkit and skillsOf are
// declared in required_test.go, in this same package.

// -- install.sh execution path unchanged for default toolkit -------------

func TestInstallIntoStillRunsInstallShForDefaultToolkit(t *testing.T) {
	root := writeToolkit(t, "pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project")
	if err := os.WriteFile(filepath.Join(root, "CONVENTIONS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho ran-with-args \"$@\"\n"
	if err := os.WriteFile(filepath.Join(root, "install.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	inst := Validate(root, Toolkit{})
	if inst == nil || !inst.OK() {
		t.Fatalf("Validate = %+v, want a healthy default toolkit", inst)
	}

	out, err := InstallInto(inst, "/tmp/some-project")
	if err != nil {
		t.Fatalf("InstallInto returned an error, want the execution path unchanged: %v", err)
	}
	if !strings.Contains(out, "ran-with-args") || !strings.Contains(out, "--project /tmp/some-project") {
		t.Errorf("InstallInto output = %q, want install.sh invoked with --project /tmp/some-project as before", out)
	}
}

// -- skillName ------------------------------------------------------------

func TestSkillNameLeadingSlashStripped(t *testing.T) {
	if got := skillName("/their-review"); got != "their-review" {
		t.Errorf("skillName(%q) = %q, want %q", "/their-review", got, "their-review")
	}
}

func TestSkillNameWithoutLeadingSlash(t *testing.T) {
	if got := skillName("their-review"); got != "their-review" {
		t.Errorf("skillName(%q) = %q, want %q", "their-review", got, "their-review")
	}
}

func TestSkillNameDropsArguments(t *testing.T) {
	if got := skillName("/their-review --full"); got != "their-review" {
		t.Errorf("skillName(%q) = %q, want %q", "/their-review --full", got, "their-review")
	}
}

func TestSkillNameEmptyAndWhitespaceYieldEmpty(t *testing.T) {
	if got := skillName(""); got != "" {
		t.Errorf("skillName(\"\") = %q, want empty", got)
	}
	if got := skillName("   "); got != "" {
		t.Errorf("skillName(whitespace) = %q, want empty", got)
	}
}

// -- Requirement.describe() ------------------------------------------------

func TestRequirementDescribeWithNoStage(t *testing.T) {
	r := Requirement{Skill: "their-review", Stage: ""}
	if got := r.describe(); got != "skills/their-review" {
		t.Errorf("describe() = %q, want %q", got, "skills/their-review")
	}
}

func TestRequirementDescribeWithStage(t *testing.T) {
	r := Requirement{Skill: "their-review", Stage: "review"}
	want := "skills/their-review (required by the review stage)"
	if got := r.describe(); got != want {
		t.Errorf("describe() = %q, want %q", got, want)
	}
}

// -- Testing skills: degradation preserved ---------------------------------

func TestTestSuiteAuthorNotRequiredForDefaultToolkit(t *testing.T) {
	for _, r := range RequiredSkills(Toolkit{}) {
		if r.Skill == "test-suite-author" {
			t.Fatal("test-suite-author is in RequiredSkills for the default toolkit; it must degrade, not fail")
		}
	}
}

func TestE2eSuiteNotRequiredForDefaultToolkit(t *testing.T) {
	for _, r := range RequiredSkills(Toolkit{}) {
		if r.Skill == "e2e-suite" {
			t.Fatal("e2e-suite is in RequiredSkills for the default toolkit; it must degrade, not fail")
		}
	}
}

func TestHasSkillFalseWhenTestingSkillNotPresentInToolkit(t *testing.T) {
	root := writeToolkit(t, "pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project")
	if err := os.WriteFile(filepath.Join(root, "CONVENTIONS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := Validate(root, Toolkit{})
	if inst == nil || !inst.OK() {
		t.Fatalf("Validate = %+v, want a healthy default toolkit", inst)
	}
	for _, s := range TestingSkills {
		if HasSkill(inst, s) {
			t.Errorf("HasSkill(%s) = true, want false: this toolkit ships no such skill", s)
		}
	}
}

// -- Backwards compatibility ------------------------------------------------

func TestUnconfiguredMachineRequiredSetAndDescribeUnchanged(t *testing.T) {
	want := []string{"pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project"}
	reqs := RequiredSkills(Toolkit{})
	if strings.Join(skillsOf(reqs), ",") != strings.Join(want, ",") {
		t.Fatalf("RequiredSkills(Toolkit{}) = %v, want the unchanged default six %v", skillsOf(reqs), want)
	}
	for i, r := range reqs {
		if r.Stage != "" {
			t.Errorf("reqs[%d].Stage = %q, want empty on an unconfigured machine", i, r.Stage)
		}
		if r.describe() != "skills/"+r.Skill {
			t.Errorf("describe() = %q, want the unannotated %q", r.describe(), "skills/"+r.Skill)
		}
	}
}

func TestDefaultToolkitValidationUnchangedRequiresConventionsAndInstallSh(t *testing.T) {
	root := writeToolkit(t, "pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project")

	inst := Validate(root, Toolkit{})
	if inst == nil {
		t.Fatal("Validate returned nil for a checkout shipping every default skill")
	}
	if strings.Join(inst.Missing, ",") != "CONVENTIONS.md" {
		t.Errorf("Missing = %v, want [CONVENTIONS.md]", inst.Missing)
	}
	if len(inst.Warnings) != 1 || !strings.Contains(inst.Warnings[0], "install.sh") {
		t.Errorf("Warnings = %v, want the install.sh warning for the default toolkit", inst.Warnings)
	}
}

func TestForeignToolkitHealthIndependentOfConventionsAndInstallShWhenNotConfigured(t *testing.T) {
	// Not configured: no Stages named, so RequiredSkills falls back to the
	// default six, which this foreign toolkit does not ship -- it must be
	// reported incomplete for those skills, but never for CONVENTIONS.md or
	// install.sh, which are nj-agents' own conventions.
	tk := Toolkit{Repo: "https://github.com/acme/house-skills.git"}
	root := writeToolkit(t, "their-own-skill")

	inst := Validate(root, tk)
	if inst == nil {
		t.Fatal("Validate returned nil for a directory with a skills/ dir")
	}
	for _, m := range inst.Missing {
		if m == "CONVENTIONS.md" || strings.Contains(m, "install.sh") {
			t.Errorf("Missing = %v, want no CONVENTIONS.md or install.sh entries for a foreign, unconfigured toolkit", inst.Missing)
		}
	}
	if len(inst.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none: install.sh is nj-agents' own convention", inst.Warnings)
	}
}
