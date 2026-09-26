package supervisor

// OR-483: the scaffold is done when its README exists on the checked-out
// branch OR on a local branch under the configured prefix. The sandbox goes
// back to the work branch before the scaffold's pull request merges (OR-482),
// and reading only the checked-out branch made a resumed chain scaffold again.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// scaffoldRepo: develop has no README; orion/001-thing carries the scaffold.
func scaffoldRepo(t *testing.T, checkout string) *workspace.Workspace {
	t.Helper()
	w := ws(t, "")
	repo := w.RepoDir()
	gitT(t, repo, "init", "-q", "-b", "develop")
	gitT(t, repo, "config", "user.email", "t@example.com")
	gitT(t, repo, "config", "user.name", "t")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "docs: plan")
	gitT(t, repo, "checkout", "-q", "-b", "orion/001-thing")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "README.md")
	gitT(t, repo, "commit", "-q", "-m", "chore: scaffold")
	gitT(t, repo, "checkout", "-q", checkout)
	return w
}

func TestScaffoldIsDoneWhileItWaitsOnItsFeatureBranch(t *testing.T) {
	for _, checkout := range []string{"develop", "orion/001-thing"} {
		if !StageDone(scaffoldRepo(t, checkout), "scaffold") {
			t.Errorf("sandbox on %s: scaffold reads as not done, so a resumed chain would scaffold again", checkout)
		}
	}
}

func TestScaffoldIsNotDoneWhenNoBranchCarriesIt(t *testing.T) {
	w := ws(t, "")
	repo := w.RepoDir()
	gitT(t, repo, "init", "-q", "-b", "develop")
	gitT(t, repo, "config", "user.email", "t@example.com")
	gitT(t, repo, "config", "user.name", "t")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "docs: plan")
	gitT(t, repo, "checkout", "-q", "-b", "orion/001-thing")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "wip, no readme")
	gitT(t, repo, "checkout", "-q", "develop")
	if StageDone(w, "scaffold") {
		t.Error("scaffold reads as done with no README on any branch")
	}
}
