package njagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OR-297: what a toolkit must ship comes from the stages a project
// configures. A fixed list validated every toolkit against nj-agents'
// catalogue, so a healthy foreign toolkit failed doctor for six skills the
// project never invokes.

func skillsOf(reqs []Requirement) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Skill)
	}
	return out
}

// writeToolkit lays out a checkout shipping exactly these skills, and
// nothing else: no CONVENTIONS.md, no install.sh.
func writeToolkit(t *testing.T, skills ...string) string {
	t.Helper()
	root := t.TempDir()
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
	return root
}

func TestRequiredSkillsComeFromTheConfiguredStages(t *testing.T) {
	tk := Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Stages: map[string]string{"intent": "/their-capture", "review": "/their-review"},
	}

	got := RequiredSkills(tk)
	want := []string{"their-capture", "their-review"} // sorted by stage: intent, review
	if strings.Join(skillsOf(got), ",") != strings.Join(want, ",") {
		t.Fatalf("RequiredSkills = %v, want exactly %v -- a stage nobody configured must not be required",
			skillsOf(got), want)
	}
	// The stage is carried so a failure points at the config line.
	if got[0].Stage != "intent" || got[1].Stage != "review" {
		t.Errorf("stages = %q/%q, want intent/review", got[0].Stage, got[1].Stage)
	}
}

func TestAnEmptyStagesMapKeepsTodaysSixSkills(t *testing.T) {
	got := skillsOf(RequiredSkills(Toolkit{}))
	want := []string{"pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("RequiredSkills(empty) = %v, want the built-in set %v", got, want)
	}
	// Nothing named them, so nothing is annotated: doctor's output for an
	// unconfigured machine must not move.
	for _, r := range RequiredSkills(Toolkit{}) {
		if r.describe() != "skills/"+r.Skill {
			t.Errorf("describe() = %q, want the unannotated %q", r.describe(), "skills/"+r.Skill)
		}
	}
}

func TestAStageCommandIsStrippedToTheSkillDirectoryName(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{
		"review": "/their-review --full", // arguments are not part of the directory
		"pr":     "their-pr",             // a slash is optional
		"verify": "",                     // configured to nothing is not a requirement
	}}
	got := skillsOf(RequiredSkills(tk))
	if strings.Join(got, ",") != "their-pr,their-review" { // sorted: pr, review
		t.Errorf("RequiredSkills = %v, want [their-pr their-review]", got)
	}
}

func TestTwoStagesNamingOneSkillRequireItOnce(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{
		"review": "/one", "verify": "/one",
	}}
	if got := skillsOf(RequiredSkills(tk)); len(got) != 1 || got[0] != "one" {
		t.Errorf("RequiredSkills = %v, want [one] deduped", got)
	}
}

func TestAForeignToolkitWithoutConventionsIsHealthy(t *testing.T) {
	tk := Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Stages: map[string]string{"intent": "/their-capture", "review": "/their-review"},
	}
	root := writeToolkit(t, "their-capture", "their-review")

	inst := Validate(root, tk)
	if inst == nil {
		t.Fatal("Validate returned nil for a toolkit shipping every configured skill")
	}
	if len(inst.Missing) != 0 {
		t.Errorf("Missing = %v, want empty: CONVENTIONS.md is nj-agents' contract, not every toolkit's",
			inst.Missing)
	}
	if !inst.OK() {
		t.Error("a foreign toolkit shipping what it was asked for is healthy")
	}
}

func TestTheDefaultToolkitStillRequiresConventions(t *testing.T) {
	root := writeToolkit(t, "pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project")

	inst := Validate(root, Toolkit{})
	if inst == nil {
		t.Fatal("Validate returned nil for a complete nj-agents checkout")
	}
	if strings.Join(inst.Missing, ",") != "CONVENTIONS.md" {
		t.Errorf("Missing = %v, want [CONVENTIONS.md]", inst.Missing)
	}
}

// The subtle one. Reporting a non-toolkit as healthy is worse than reporting
// a toolkit as absent, and the old arithmetic -- everything required is
// missing, so nothing is here -- held only while the required lists were
// fixed at seven. They now shrink with the config.
func TestARandomDirectoryIsNotAToolkitUnderAOneSkillZeroDocConfig(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/only"}}

	if inst := Validate(t.TempDir(), tk); inst != nil {
		t.Fatalf("an empty directory validated as a toolkit: %+v", inst)
	}
	// And a real toolkit that is merely incomplete stays reported as one,
	// rather than disappearing into "not installed".
	inst := Validate(writeToolkit(t), tk)
	if inst == nil {
		t.Fatal("a skills/ directory missing its skills is an incomplete toolkit, not a random directory")
	}
	if len(inst.Missing) != 1 || !strings.Contains(inst.Missing[0], "review stage") {
		t.Errorf("Missing = %v, want the missing skill named with the stage that required it", inst.Missing)
	}
}

func TestInstallShIsRequiredOnlyOfTheToolkitThatShipsOne(t *testing.T) {
	foreign := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/only"}}
	inst := Validate(writeToolkit(t, "only"), foreign)
	if inst == nil || !inst.OK() {
		t.Fatalf("a foreign toolkit with no install.sh must be healthy: %+v", inst)
	}
	if len(inst.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none: install.sh is nj-agents' convention", inst.Warnings)
	}

	// The default toolkit does ship one, so its absence still says so.
	def := Validate(writeToolkit(t, "pre-push-review"), Toolkit{})
	if def == nil || len(def.Warnings) != 1 || !strings.Contains(def.Warnings[0], "install.sh") {
		t.Errorf("Warnings = %+v, want the install.sh warning for the default toolkit", def)
	}
}

func TestInstallIntoNamesTheReasonWhenTheToolkitShipsNoInstaller(t *testing.T) {
	_, err := InstallInto(&Install{Root: writeToolkit(t, "only")}, "/tmp/project")
	if err == nil {
		t.Fatal("InstallInto succeeded with no install.sh")
	}
	if !strings.Contains(err.Error(), "ships no install.sh") {
		t.Errorf("error = %q, want a reason rather than a stat failure", err)
	}
}

// TestingSkills degrade rather than fail, and this story must not change
// that: QA falls back to the repository's own test runner.
func TestTestingSkillsAreStillOptional(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/pre-push-review"}}
	for _, s := range TestingSkills {
		for _, r := range RequiredSkills(tk) {
			if r.Skill == s {
				t.Errorf("%s became required; it must degrade the QA stage, not fail validation", s)
			}
		}
	}
	inst := Validate(writeToolkit(t, "pre-push-review"), tk)
	if inst == nil || !inst.OK() {
		t.Fatalf("a toolkit without the testing skills must stay healthy: %+v", inst)
	}
	for _, s := range TestingSkills {
		if HasSkill(inst, s) {
			t.Errorf("HasSkill(%s) is true for a toolkit that does not ship it", s)
		}
	}
}

// Cloning the vendor default is a decision Orion already made. Cloning
// whatever URL a checked-in config names is third-party code landing in
// ORION_HOME, which is the operator's call.
func TestCloningTheDefaultUrlDoesNotAskAndAForeignOneDoes(t *testing.T) {
	// An occupied, non-toolkit destination stops Clone after the consent
	// gate and before the network, so this asserts consent, not git.
	occupy := func(home, repo string) {
		t.Helper()
		if err := os.MkdirAll(VendorDirFor(home, repo), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	home := t.TempDir()
	occupy(home, RepoURL)
	asked := false
	if _, err := Clone(home, Toolkit{}, "", func(string) bool { asked = true; return true }); err == nil {
		t.Fatal("expected the incomplete-checkout error")
	}
	if asked {
		t.Error("the default toolkit was confirmed; that is a consent prompt for a decision already made")
	}

	home = t.TempDir()
	foreign := Toolkit{Repo: "https://github.com/acme/house-skills.git"}
	occupy(home, foreign.Repo)
	asked = false
	if _, err := Clone(home, foreign, "", func(url string) bool {
		asked = true
		if url != foreign.Repo {
			t.Errorf("confirmed %q, want the URL actually about to be fetched %q", url, foreign.Repo)
		}
		return true
	}); err == nil {
		t.Fatal("expected the incomplete-checkout error")
	}
	if !asked {
		t.Error("a foreign URL was fetched without asking")
	}
}

func TestDecliningAForeignCloneLeavesNothingBehind(t *testing.T) {
	home := t.TempDir()
	foreign := Toolkit{Repo: "https://github.com/acme/house-skills.git"}

	inst, err := Clone(home, foreign, "", func(string) bool { return false })
	if err == nil {
		t.Fatal("declining must not report success")
	}
	if inst != nil {
		t.Errorf("Install = %+v, want nil", inst)
	}
	if !strings.Contains(err.Error(), CloneCommand(home, foreign)) {
		t.Errorf("error = %q, want the manual git command so the operator can do it themselves", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "vendor")); statErr == nil {
		t.Error("declining created the vendor directory; the machine must be untouched")
	}
}

// nil is not "no opinion". A caller with no way to ask must not fetch a URL
// nobody approved.
func TestAForeignCloneWithNoWayToAskIsDeclined(t *testing.T) {
	home := t.TempDir()
	if _, err := Clone(home, Toolkit{Repo: "https://example.com/kit.git"}, "", nil); err == nil {
		t.Fatal("a foreign clone with no Confirm succeeded")
	}
}

func TestCloneCommandNamesTheConfiguredRepository(t *testing.T) {
	home := filepath.Join("home", "orion")
	foreign := Toolkit{Repo: "https://github.com/acme/house-skills.git"}
	got := CloneCommand(home, foreign)
	if !strings.Contains(got, foreign.Repo) || !strings.Contains(got, "house-skills") {
		t.Errorf("CloneCommand = %q, want it to name %q and its own vendor leaf", got, foreign.Repo)
	}
	if want := "git clone " + RepoURL + " " + VendorDir(home); CloneCommand(home, Toolkit{}) != want {
		t.Errorf("CloneCommand(default) = %q, want %q", CloneCommand(home, Toolkit{}), want)
	}
}
