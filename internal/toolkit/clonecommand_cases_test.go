package toolkit

import (
	"path/filepath"
	"strings"
	"testing"
)

// OR-297: dedicated coverage for CloneCommand's git-clone generation, the
// nil-Confirm decline path, and InstallInto's missing-installer behavior.
// writeToolkit/writeToolkitAt are shared with required_test.go/discover_test.go
// in this package.

func TestCloneCommandDefaultRepoYieldsGitCloneWithDefaultVendorDir(t *testing.T) {
	home := filepath.Join("home", "orion")
	want := "git clone " + RepoURL + " " + VendorDir(home)
	if got := CloneCommand(home, Toolkit{}); got != want {
		t.Errorf("CloneCommand(default) = %q, want %q", got, want)
	}
}

func TestCloneCommandForeignRepoYieldsGitCloneWithForeignVendorDir(t *testing.T) {
	home := filepath.Join("home", "orion")
	foreign := Toolkit{Repo: "https://github.com/acme/house-skills.git"}
	want := "git clone " + foreign.Repo + " " + VendorDirFor(home, foreign.Repo)
	if got := CloneCommand(home, foreign); got != want {
		t.Errorf("CloneCommand(foreign) = %q, want %q", got, want)
	}
}

func TestCloneCommandVendorDirIsDerivedFromRepoNotFixed(t *testing.T) {
	home := filepath.Join("home", "orion")
	a := CloneCommand(home, Toolkit{Repo: "https://github.com/acme/one.git"})
	b := CloneCommand(home, Toolkit{Repo: "https://github.com/acme/two.git"})
	if a == b {
		t.Fatalf("two different repos produced the same clone command: %q", a)
	}
	if !strings.Contains(a, "one") || !strings.Contains(b, "two") {
		t.Errorf("CloneCommand should name each repo's own leaf: %q / %q", a, b)
	}
}

// A caller with no way to ask must not fetch a URL nobody approved.
func TestCloneNilConfirmActsAsDeclined(t *testing.T) {
	home := t.TempDir()
	foreign := Toolkit{Repo: "https://example.com/kit.git"}

	inst, err := Clone(home, foreign, "", nil)
	if err == nil {
		t.Fatal("Clone with a nil Confirm succeeded; nil must read as declined")
	}
	if inst != nil {
		t.Errorf("Install = %+v, want nil on decline", inst)
	}
}

func TestInstallIntoMissingInstallShNamesTheReasonNotAStatError(t *testing.T) {
	root := writeToolkit(t, "only")
	_, err := InstallInto(&Install{Root: root}, "/tmp/project")
	if err == nil {
		t.Fatal("InstallInto succeeded despite no install.sh")
	}
	if !strings.Contains(err.Error(), "ships no install.sh") {
		t.Errorf("error = %q, want it to say WHY (ships no install.sh), not a stat failure", err)
	}
}

// A missing install.sh does not stop the toolkit from being usable: skills
// are read from the clone directly, and Validate must still report healthy.
func TestInstallShMissingDoesNotBlockForeignToolkitUsage(t *testing.T) {
	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/only"}}
	root := writeToolkit(t, "only")

	inst := Validate(root, tk)
	if inst == nil || !inst.OK() {
		t.Fatalf("a foreign toolkit missing install.sh must still validate healthy: %+v", inst)
	}
	if !HasSkill(inst, "only") {
		t.Error("HasSkill(only) = false, want true: the skill is still readable from the clone")
	}
	if _, err := InstallInto(inst, "/tmp/project"); err == nil {
		t.Error("InstallInto succeeded with no install.sh present")
	}
}
