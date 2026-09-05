package toolkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OR-297: dedicated coverage for the RequiredSkills/RequiredDocs/Validate
// case list, including a few combinations required_test.go's tests don't
// pin down directly (first-stage-alphabetically on a shared skill, RequiredDocs
// as a standalone unit, and a fully-complete default toolkit).

func TestRequiredSkillsEmptyToolkitDefaultOrder(t *testing.T) {
	want := []string{"pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project"}
	got := skillsOf(RequiredSkills(Toolkit{}))
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("RequiredSkills(Toolkit{}) = %v, want the default six in order %v", got, want)
	}
}

func TestRequiredSkillsFromStagesMapSlashStrippedAndDeduped(t *testing.T) {
	tk := Toolkit{Stages: map[string]string{
		"review": "/their-review",
		"intent": "/their-capture",
	}}
	got := skillsOf(RequiredSkills(tk))
	want := []string{"their-capture", "their-review"} // sorted by stage name: intent, review
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("RequiredSkills = %v, want exactly %v", got, want)
	}
}

func TestMultipleStagesNamingSameSkillListedOnceWithFirstStageAlphabetically(t *testing.T) {
	tk := Toolkit{Stages: map[string]string{
		"review": "/shared",
		"verify": "/shared",
		"build":  "/shared",
	}}
	got := RequiredSkills(tk)
	if len(got) != 1 || got[0].Skill != "shared" {
		t.Fatalf("RequiredSkills = %+v, want a single 'shared' requirement", got)
	}
	// Stages are walked in sorted order and the first hit wins, so the stage
	// that survives is "build" -- alphabetically first among the three.
	if got[0].Stage != "build" {
		t.Errorf("Stage = %q, want %q (alphabetically first of the stages naming it)", got[0].Stage, "build")
	}
}

func TestStageConfiguredToEmptyStringIsNotARequirement(t *testing.T) {
	tk := Toolkit{Stages: map[string]string{
		"review": "/only",
		"verify": "",
	}}
	got := skillsOf(RequiredSkills(tk))
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("RequiredSkills = %v, want [only]: an empty command names nothing", got)
	}
}

func TestCommandWithArgumentsYieldsSkillNameOnly(t *testing.T) {
	tk := Toolkit{Stages: map[string]string{"review": "/their-review --full"}}
	got := skillsOf(RequiredSkills(tk))
	if len(got) != 1 || got[0] != "their-review" {
		t.Errorf("RequiredSkills = %v, want [their-review], not the arguments", got)
	}
}

func TestLeadingSlashIsOptionalInStageCommand(t *testing.T) {
	tk := Toolkit{Stages: map[string]string{"pr": "their-pr"}}
	got := skillsOf(RequiredSkills(tk))
	if len(got) != 1 || got[0] != "their-pr" {
		t.Errorf("RequiredSkills = %v, want [their-pr] whether or not the command starts with a slash", got)
	}
}

func TestRequirementCarriesTheStageThatNamedIt(t *testing.T) {
	tk := Toolkit{Stages: map[string]string{"review": "/their-review"}}
	got := RequiredSkills(tk)
	if len(got) != 1 || got[0].Stage != "review" {
		t.Errorf("RequiredSkills = %+v, want Stage %q", got, "review")
	}
}

func TestDefaultRequirementsHaveEmptyStageField(t *testing.T) {
	for _, r := range RequiredSkills(Toolkit{}) {
		if r.Stage != "" {
			t.Errorf("default requirement %+v has Stage %q, want empty: nothing configured named it", r, r.Stage)
		}
	}
}

func TestRequiredDocsDefaultToolkitRequiresConventions(t *testing.T) {
	if got := RequiredDocs(Toolkit{}); strings.Join(got, ",") != "CONVENTIONS.md" {
		t.Errorf("RequiredDocs(empty Repo) = %v, want [CONVENTIONS.md]", got)
	}
	if got := RequiredDocs(Toolkit{Repo: RepoURL}); strings.Join(got, ",") != "CONVENTIONS.md" {
		t.Errorf("RequiredDocs(RepoURL) = %v, want [CONVENTIONS.md]", got)
	}
}

func TestRequiredDocsForeignToolkitRequiresNoDocuments(t *testing.T) {
	tk := Toolkit{Repo: "https://github.com/acme/house-skills.git"}
	if got := RequiredDocs(tk); len(got) != 0 {
		t.Errorf("RequiredDocs(foreign) = %v, want none: CONVENTIONS.md is nj-agents' own contract", got)
	}
}

func TestRequiredDocsReturnsNilForForeignToolkitNotEmptySlice(t *testing.T) {
	tk := Toolkit{Repo: "https://github.com/acme/house-skills.git"}
	if got := RequiredDocs(tk); got != nil {
		t.Errorf("RequiredDocs(foreign) = %#v, want nil, not an empty non-nil slice", got)
	}
}

func TestValidateForeignToolkitWithoutConventionsButAllSkillsIsHealthy(t *testing.T) {
	tk := Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Stages: map[string]string{"review": "/their-review"},
	}
	inst := Validate(writeToolkit(t, "their-review"), tk)
	if inst == nil || !inst.OK() {
		t.Fatalf("Validate = %+v, want a healthy install: CONVENTIONS.md isn't required of a foreign toolkit", inst)
	}
}

func TestValidateDefaultToolkitWithoutConventionsIsIncomplete(t *testing.T) {
	root := writeToolkit(t, "pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project")
	inst := Validate(root, Toolkit{})
	if inst == nil {
		t.Fatal("Validate returned nil for a checkout shipping every default skill")
	}
	found := false
	for _, m := range inst.Missing {
		if m == "CONVENTIONS.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("Missing = %v, want it to contain CONVENTIONS.md", inst.Missing)
	}
}

func TestValidateDefaultToolkitWithAllSkillsAndConventionsIsHealthy(t *testing.T) {
	root := writeToolkit(t, "pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project")
	if err := os.WriteFile(filepath.Join(root, "CONVENTIONS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := Validate(root, Toolkit{})
	if inst == nil {
		t.Fatal("Validate returned nil for a complete default toolkit")
	}
	if !inst.OK() {
		t.Errorf("Install = %+v, want OK() true for a complete default checkout", inst)
	}
}
