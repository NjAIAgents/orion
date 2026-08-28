package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doctor is where somebody looks when they suspect something is wrong, so
// it is where a branch model with no integration branch has to show up --
// as FAIL, because every merge Orion performs under it is a release nobody
// authorised, reported as an ordinary merge.
func TestDoctorFailsWhenThereIsNoIntegrationBranch(t *testing.T) {
	dir := writeProject(t, `{"vcs":{"default_branch":"main","work_branch":"main"}}`)

	c := checkBranchModel(dir)
	if c.grade != fail {
		t.Fatalf("grade = %v, want FAIL", c.grade.label())
	}
	if !strings.Contains(c.detail, "main") {
		t.Errorf("the detail must name the branch: %s", c.detail)
	}
	if !strings.Contains(c.fix, "orion init") ||
		!strings.Contains(c.fix, "allow_release_branch_merges") {
		t.Errorf("the fix must give the remedy and the opt-in: %s", c.fix)
	}
}

func TestDoctorWarnsAndSaysWhatTheOverrideGaveUp(t *testing.T) {
	dir := writeProject(t,
		`{"vcs":{"default_branch":"main","work_branch":"main","allow_release_branch_merges":true}}`)

	c := checkBranchModel(dir)
	if c.grade != warn {
		t.Fatalf("grade = %v, want WARN: a deliberate single-branch repo is not broken", c.grade.label())
	}
	if !strings.Contains(c.fix, "no human promotion step") {
		t.Errorf("the waiver must state what is no longer protected: %s", c.fix)
	}
}

func TestDoctorPassesOnTheTwoBranchModel(t *testing.T) {
	dir := writeProject(t, `{"vcs":{"default_branch":"main","work_branch":"develop"}}`)

	c := checkBranchModel(dir)
	if c.grade != ok {
		t.Fatalf("grade = %v, want OK (%s)", c.grade.label(), c.fix)
	}
	if !strings.Contains(c.detail, "develop") || !strings.Contains(c.detail, "main") {
		t.Errorf("a passing check should still show the model: %s", c.detail)
	}
}

func writeProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
