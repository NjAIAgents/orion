package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox builds a workspace with a real clone, mimicking Bind's layout:
// an "origin" bare repo, and a clone of it at <ws>/repo.
func sandbox(t *testing.T) *Workspace {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q", "--bare", "-b", "main", origin)
	run(root, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(seed, "add", ".")
	run(seed, "commit", "-q", "-m", "first")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "-q", "origin", "main")
	run(seed, "checkout", "-q", "-b", "develop")
	run(seed, "push", "-q", "origin", "develop")

	ws := &Workspace{ID: "t", Dir: filepath.Join(root, "ws")}
	for _, d := range []string{"worktrees", ".orion/logs", ".orion/state"} {
		if err := os.MkdirAll(filepath.Join(ws.Dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	run(root, "clone", "-q", origin, filepath.Join(ws.Dir, "repo"))
	return ws
}

// The property the whole layout exists for: a second job on the same ticket
// must not land on the first job's branch. Reusing it would stack a retry on
// top of a failed attempt, and silently rewrite any pull request already
// open on that branch.
func TestWorktreeBranchesAreUnique(t *testing.T) {
	ws := sandbox(t)

	a, err := AddWorktree(ws, "develop", "orion/fcia-6")
	if err != nil {
		t.Fatal(err)
	}
	b, err := AddWorktree(ws, "develop", "orion/fcia-6")
	if err != nil {
		t.Fatal(err)
	}
	if a.Branch == b.Branch {
		t.Fatalf("both jobs got %q; a retry would rewrite the first attempt", a.Branch)
	}
	if a.Branch != "orion/fcia-6" || b.Branch != "orion/fcia-6-2" {
		t.Errorf("branches = %q, %q", a.Branch, b.Branch)
	}
	if a.Path == b.Path {
		t.Error("both jobs share a directory; one would overwrite the other")
	}
	for _, j := range []*Job{a, b} {
		if _, err := os.Stat(filepath.Join(j.Path, "README.md")); err != nil {
			t.Errorf("%s is not a usable checkout: %v", j.Path, err)
		}
	}
}

// Jobs must be isolated: writing in one must not appear in the other, which
// is what makes concurrent or sequential runs safe without resets.
func TestWorktreesAreIsolated(t *testing.T) {
	ws := sandbox(t)
	a, _ := AddWorktree(ws, "develop", "orion/a")
	b, _ := AddWorktree(ws, "develop", "orion/b")

	if err := os.WriteFile(filepath.Join(a.Path, "only-in-a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.Path, "only-in-a.txt")); err == nil {
		t.Error("a file written in one job appeared in another")
	}
	if dirty, _ := Dirty(a.Path); !dirty {
		t.Error("Dirty missed an uncommitted file")
	}
	if dirty, _ := Dirty(b.Path); dirty {
		t.Error("Dirty reported the untouched job as dirty")
	}
}

// Removing a worktree must refuse while it holds the only copy of work. An
// agent killed on the wall clock is the common case, not the rare one.
func TestRemoveRefusesToDiscardWork(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/keep")
	if err := os.WriteFile(filepath.Join(j.Path, "wip.txt"), []byte("half done"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RemoveWorktree(ws, j.Path, false)
	if err == nil {
		t.Fatal("removed a worktree holding uncommitted work")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("the refusal should say what is at stake, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(j.Path, "wip.txt")); statErr != nil {
		t.Error("the work was destroyed despite the refusal")
	}
	if err := RemoveWorktree(ws, j.Path, true); err != nil {
		t.Fatalf("--force should still work: %v", err)
	}
}

func TestListWorktreesExcludesTheSandboxItself(t *testing.T) {
	ws := sandbox(t)
	if jobs, err := ListWorktrees(ws); err != nil || len(jobs) != 0 {
		t.Fatalf("jobs = %v err = %v; the clone is not a job", jobs, err)
	}
	_, _ = AddWorktree(ws, "develop", "orion/one")
	jobs, err := ListWorktrees(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Branch != "orion/one" {
		t.Errorf("jobs = %+v", jobs)
	}
}

// A job's RepoDir must be its worktree, or the supervisor runs in the shared
// clone and every job writes over the same checkout.
func TestRepoPathOverridesRepoDir(t *testing.T) {
	ws := &Workspace{ID: "t", Dir: "/tmp/ws"}
	if got := ws.RepoDir(); got != filepath.Join("/tmp/ws", "repo") {
		t.Errorf("RepoDir = %q", got)
	}
	job := *ws
	job.RepoPath = "/tmp/ws/worktrees/orion-fcia-6"
	if got := job.RepoDir(); got != "/tmp/ws/worktrees/orion-fcia-6" {
		t.Errorf("job RepoDir = %q, want the worktree", got)
	}
	if ws.RepoDir() == job.RepoDir() {
		t.Error("overriding one job's path changed the workspace's own")
	}
}

// Branching from a stale base produces a pull request full of other people's
// changes, so the sandbox fetches first.
func TestWorktreeBranchesFromTheRemoteTip(t *testing.T) {
	ws := sandbox(t)
	repo := filepath.Join(ws.Dir, "repo")

	// Advance origin/develop behind the sandbox's back.
	tmp := t.TempDir()
	clone := filepath.Join(tmp, "other")
	originURL, _ := git(repo, "remote", "get-url", "origin")
	if out, err := exec.Command("git", "clone", "-q", "-b", "develop", originURL, clone).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(clone, "new.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "later"}, {"push", "-q", "origin", "develop"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = clone
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	j, err := AddWorktree(ws, "develop", "orion/fresh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(j.Path, "new.txt")); err != nil {
		t.Error("the job branched from a stale base; the newest commit on develop is missing")
	}
}

// A worktree cut from the base and left untouched holds nothing that is not
// already on a remote, so removing it must be allowed.
//
// The first implementation compared against origin/HEAD -- the DEFAULT
// branch -- so a worktree branched from develop appeared to carry every
// commit develop has that main does not, and cleanup was refused for work
// that did not exist. A safety check that fires on nothing teaches people to
// pass --force by habit, and then it is not there when it matters.
func TestUntouchedWorktreeCanBeRemoved(t *testing.T) {
	ws := sandbox(t)
	repo := filepath.Join(ws.Dir, "repo")

	// Put a commit on develop only, so develop and main differ -- the exact
	// shape that produced the false positive.
	origin, _ := git(repo, "remote", "get-url", "origin")
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("git", "clone", "-q", "-b", "develop", origin, other).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(other, "on-develop.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "develop only"}, {"push", "-q", "origin", "develop"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = other
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	j, err := AddWorktree(ws, "develop", "orion/untouched")
	if err != nil {
		t.Fatal(err)
	}
	if unpushed, n := hasUnpushedCommits(j.Path); unpushed {
		t.Errorf("an untouched worktree reported %d unpushed commit(s)", n)
	}
	if err := RemoveWorktree(ws, j.Path, false); err != nil {
		t.Fatalf("refused to remove a worktree holding nothing: %v", err)
	}
}

// The counterpart: a real commit that exists on no remote must still block
// removal, or the check protects nothing.
func TestWorktreeWithARealCommitIsStillProtected(t *testing.T) {
	ws := sandbox(t)
	j, err := AddWorktree(ws, "develop", "orion/has-work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.Path, "new.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "real work"}} {
		cmd := exec.Command("git", append([]string{"-C", j.Path}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	unpushed, n := hasUnpushedCommits(j.Path)
	if !unpushed || n != 1 {
		t.Fatalf("unpushed = %v, n = %d; a real commit must be protected", unpushed, n)
	}
	err = RemoveWorktree(ws, j.Path, false)
	if err == nil {
		t.Fatal("removed a worktree holding the only copy of a commit")
	}
	if !strings.Contains(err.Error(), "not on the remote") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// A squash-merged branch whose remote ref GitHub then deleted is the shape
// that made every merged ticket leave its worktree behind.
//
// After a squash the branch's own commit objects are unreachable from any
// remote by construction -- the forge replayed them as one NEW object -- and
// delete_branch_on_merge removes the ref that would otherwise have covered
// them. hasUnpushedCommits therefore answers "yes, unpushed" about work the
// forge has fully accepted, which is why the merged path must not consult it.
func TestSquashMergedWorktreeIsPrunedDespiteUnreachableCommits(t *testing.T) {
	ws := sandbox(t)

	j, err := AddWorktree(ws, "develop", "orion/squashed")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.Path, "feature.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, j.Path, "add", ".")
	gitT(t, j.Path, "commit", "-q", "-m", "the work")
	gitT(t, j.Path, "push", "-q", "origin", "HEAD:refs/heads/orion/squashed")

	// The forge's side of a squash merge: replay the branch onto develop as
	// one new commit, then delete the head branch.
	origin, _ := git(filepath.Join(ws.Dir, "repo"), "remote", "get-url", "origin")
	forge := filepath.Join(t.TempDir(), "forge")
	if out, err := exec.Command("git", "clone", "-q", "-b", "develop", origin, forge).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	gitT(t, forge, "fetch", "-q", "origin", "orion/squashed")
	gitT(t, forge, "merge", "--squash", "FETCH_HEAD")
	gitT(t, forge, "commit", "-q", "-m", "the work (#1)")
	gitT(t, forge, "push", "-q", "origin", "develop")
	gitT(t, forge, "push", "-q", "origin", "--delete", "orion/squashed")

	// The job's checkout learns the branch is gone, as any later fetch would.
	gitT(t, j.Path, "fetch", "-q", "--prune", "origin")

	if unpushed, _ := hasUnpushedCommits(j.Path); !unpushed {
		t.Fatal("the setup no longer reproduces the bug: ancestry now finds the commit on a remote")
	}
	if err := RemoveWorktree(ws, j.Path, false); err == nil {
		t.Fatal("without a merged verdict the guard must still hold")
	}
	if err := RemoveMergedWorktree(ws, j.Path); err != nil {
		t.Fatalf("kept the worktree for a branch the forge has merged: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(j.Path, "feature.go")); statErr == nil {
		t.Error("the worktree is still on disk after a merged prune")
	}
}

// A merged prune still refuses to discard uncommitted work: the pull request
// says nothing about what is sitting unstaged in the checkout.
func TestMergedRemoveStillRefusesUncommittedWork(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/merged-but-dirty")
	if err := os.WriteFile(filepath.Join(j.Path, "wip.txt"), []byte("half done"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RemoveMergedWorktree(ws, j.Path)
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("a merged PR does not vouch for uncommitted work, got: %v", err)
	}
}

// gitT runs git in dir with a deterministic identity, failing the test on error.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
