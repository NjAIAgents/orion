package collect

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OR-261, the git half. MergeInto checks the batch ref out in a worktree, and
// a batch that parks to wait for CI returns before DropRef -- so that worktree
// outlives the tick by design. git then refuses to force-update the branch:
//
//	fatal: cannot force update the branch 'orion/batch' used by worktree at ...
//
// Which wedged every subsequent batch, forever, overnight.

// readSource reads a file from this package, so a test can assert on a
// property of the code that only a live git repository would otherwise show.
func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

// section returns the text from a marker to the blank line that follows the
// block it starts. Crude on purpose: it is enough to tell whether a specific
// function mentions a specific call.
func section(src, marker string) string {
	at := strings.Index(src, marker)
	if at < 0 {
		return ""
	}
	rest := src[at:]
	if end := strings.Index(rest, "\n}\n"); end > 0 {
		return rest[:end]
	}
	return rest
}

func batchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// repoWithBranch builds a throwaway repository with one commit on develop.
func repoWithBranch(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "--initial-branch=develop")
	gitRun(t, dir, "config", "user.email", "t@example.com")
	gitRun(t, dir, "config", "user.name", "t")
	gitRun(t, dir, "commit", "--quiet", "--allow-empty", "-m", "root")
	return dir
}

// The reproduction, at the level git actually failed: a branch held by a
// worktree cannot be force-updated. This is the fact the fix is built on, and
// it is worth pinning because it is the part that was assumed away -- CutRef's
// doc claimed the ref was "created DETACHED from any worktree".
func TestGitRefusesToForceUpdateABranchAWorktreeHolds(t *testing.T) {
	dir := repoWithBranch(t)
	gitRun(t, dir, "branch", "orion/batch", "develop")

	wt := filepath.Join(t.TempDir(), "orion-batch")
	gitRun(t, dir, "worktree", "add", "--force", wt, "orion/batch")

	cmd := exec.Command("git", "branch", "-f", "orion/batch", "develop")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Skip("this git force-updates a branch held by a worktree; the guard is " +
			"harmless here but the failure it prevents is version-dependent")
	}
	if !strings.Contains(string(out), "used by worktree") {
		t.Fatalf("git refused for a different reason than expected:\n%s", out)
	}

	// And the fix: releasing the worktree first makes the same update succeed.
	gitRun(t, dir, "worktree", "remove", "--force", wt)
	gitRun(t, dir, "worktree", "prune")
	gitRun(t, dir, "branch", "-f", "orion/batch", "develop")
}

// An operator who deletes the worktree directory by hand -- which is what a
// person does when this wedges -- leaves git still believing the branch is
// checked out. Without a prune, the fatal survives the cleanup meant to fix it.
func TestPruningRecoversFromAWorktreeDirectoryDeletedByHand(t *testing.T) {
	dir := repoWithBranch(t)
	gitRun(t, dir, "branch", "orion/batch", "develop")

	wt := filepath.Join(t.TempDir(), "orion-batch")
	gitRun(t, dir, "worktree", "add", "--force", wt, "orion/batch")

	// Simulate the hand cleanup: the directory goes, the record stays.
	if out, err := exec.Command("rm", "-rf", wt).CombinedOutput(); err != nil {
		t.Fatalf("removing the worktree directory: %v\n%s", err, out)
	}

	gitRun(t, dir, "worktree", "prune")
	// Now the branch is updatable again. Before the prune it need not be.
	gitRun(t, dir, "branch", "-f", "orion/batch", "develop")
}

// CutRef must say it releases the worktree, and must not claim the ref is
// detached from one -- MergeInto made that false and the comment outlived it.
func TestCutRefReleasesTheRefsWorktreeAndSaysSo(t *testing.T) {
	b, err := readSource("batchgit.go")
	if err != nil {
		t.Fatal(err)
	}
	cut := section(b, "func (r repoGit) CutRef(")
	if cut == "" {
		t.Fatal("could not find CutRef")
	}
	if !strings.Contains(cut, "worktree") || !strings.Contains(cut, "remove") {
		t.Error("CutRef does not release the ref's worktree; a batch that parked to " +
			"wait for CI leaves one behind, and every later batch hits " +
			"`cannot force update the branch ... used by worktree`")
	}
	if !strings.Contains(cut, "prune") {
		t.Error("CutRef does not prune worktree records, so a directory removed by " +
			"hand still blocks the branch")
	}
	// The doc above it must not repeat the claim that was false.
	doc := section(b, "// CutRef creates ref at base")
	if strings.Contains(doc, "created DETACHED from any worktree, so nothing has it checked out") {
		t.Error("CutRef's doc still claims the ref is detached from any worktree; " +
			"MergeInto checks it out, which is the whole bug")
	}
}
