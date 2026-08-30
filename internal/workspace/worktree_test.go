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

// Git hooks belong to the clone, not to a run. CloneDir following RepoPath
// would point attribution setup at a worktree, whose .git is a file with no
// hooks directory -- so `dun init` would land somewhere git never reads and
// the commits would stay untrailered while everything reported success
// (OR-193).
func TestCloneDirIgnoresTheJobWorktree(t *testing.T) {
	job := Workspace{ID: "t", Dir: "/tmp/ws", RepoPath: "/tmp/ws/worktrees/orion-or-193"}
	if got := job.CloneDir(); got != filepath.Join("/tmp/ws", "repo") {
		t.Errorf("CloneDir = %q, want the shared clone regardless of RepoPath", got)
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

// The breaker's stop-note must not keep the worktree it was written in.
//
// OR-194 has the breaker write plans/BLOCKED.md at trip time, untracked by
// design so it never reaches the diff -- which also means it can never be
// committed away. The deletion guard then refused to prune the worktree
// because a file was uncommitted, for a ticket the forge had already merged.
// Both halves are right on their own; together they deadlock, and every
// tripped run leaves a full checkout behind (OR-220).
func TestBlockedNoteAloneDoesNotKeepAMergedWorktree(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/tripped")
	writeBlockedNote(t, j.Path)

	if dirty, detail := Dirty(j.Path); dirty {
		t.Fatalf("Orion's own stop-note counted as the operator's work: %s", detail)
	}
	if err := RemoveMergedWorktree(ws, j.Path); err != nil {
		t.Fatalf("kept the worktree over a note Orion wrote itself: %v", err)
	}
	if _, err := os.Stat(j.Path); err == nil {
		t.Error("the worktree is still on disk after a merged prune")
	}
}

// The other half of the rule: anything else untracked is still the operator's,
// and the refusal must name it exactly as before.
//
// The agent's draft goes in plans/ NEXT TO the note, which is the case a
// path-name check gets wrong: git reports a wholly untracked directory as one
// "?? plans/" entry, and that single line covers both files at once.
func TestOtherUntrackedWorkStillKeepsTheWorktree(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/tripped-with-work")
	writeBlockedNote(t, j.Path)
	if err := os.WriteFile(filepath.Join(j.Path, "plans", "OR-1.md"),
		[]byte("the agent's plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RemoveMergedWorktree(ws, j.Path)
	if err == nil {
		t.Fatal("removed a worktree holding a file the agent wrote")
	}
	if !strings.Contains(err.Error(), "OR-1.md") {
		t.Errorf("the refusal must name what it is protecting: %v", err)
	}
	if strings.Contains(err.Error(), "BLOCKED.md") {
		t.Errorf("Orion's own note was reported as a reason to keep: %v", err)
	}
}

// A tracked, modified BLOCKED.md is somebody's committed file, not Orion's
// scratch. Ignoring it by name alone would discard their edits, so only the
// untracked entry is Orion's to disregard.
func TestTrackedBlockedNoteIsStillProtected(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/tracked-note")
	writeBlockedNote(t, j.Path)
	gitT(t, j.Path, "add", "-f", "plans/BLOCKED.md")
	gitT(t, j.Path, "commit", "-q", "-m", "someone committed the note")
	if err := os.WriteFile(filepath.Join(j.Path, "plans", "BLOCKED.md"),
		[]byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RemoveMergedWorktree(ws, j.Path)
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("a tracked file's edits are not Orion's to discard, got: %v", err)
	}
}

// The note is the record of why a run stopped, so it must survive until the
// work has landed. Ignoring it for the dirty check must not weaken the
// unpushed-commits guard, which is what keeps an unmerged blocked run -- and
// its note -- on disk for whoever comes to read it.
func TestBlockedNoteDoesNotUnprotectUnpushedCommits(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/tripped-unpushed")
	if err := os.WriteFile(filepath.Join(j.Path, "half.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, j.Path, "add", ".")
	gitT(t, j.Path, "commit", "-q", "-m", "as far as it got")
	writeBlockedNote(t, j.Path)

	err := RemoveWorktree(ws, j.Path, false)
	if err == nil || !strings.Contains(err.Error(), "not on the remote") {
		t.Fatalf("a blocked run that was never pushed must stay readable, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(j.Path, "plans", "BLOCKED.md")); statErr != nil {
		t.Errorf("the account of the trip is gone: %v", statErr)
	}
}

// A worktree holding only Orion's runtime directory -- no stop-note at all --
// must be just as prunable. .orion/ was already ignored before OR-220; this
// pins that the merge into one shared function did not narrow it.
func TestOnlyOrionRuntimeDirAloneDoesNotKeepAMergedWorktree(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/only-runtime-dir")
	writeOrionRuntimeFile(t, j.Path, "state/run.json", "{}\n")

	if dirty, detail := Dirty(j.Path); dirty {
		t.Fatalf("Orion's own runtime directory counted as the operator's work: %s", detail)
	}
	if err := RemoveMergedWorktree(ws, j.Path); err != nil {
		t.Fatalf("kept the worktree over files Orion wrote itself: %v", err)
	}
}

// Both of Orion's own artefacts together still must not block a prune -- the
// deadlock OR-220 fixes was never about just one of them.
func TestBlockedNoteAndRuntimeDirTogetherDoNotKeepAMergedWorktree(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/note-and-runtime-dir")
	writeBlockedNote(t, j.Path)
	writeOrionRuntimeFile(t, j.Path, "state/run.json", "{}\n")
	writeOrionRuntimeFile(t, j.Path, "logs/run.log", "log\n")

	if dirty, detail := Dirty(j.Path); dirty {
		t.Fatalf("Orion's own files, together, counted as the operator's work: %s", detail)
	}
	if err := RemoveMergedWorktree(ws, j.Path); err != nil {
		t.Fatalf("kept the worktree over files Orion wrote itself: %v", err)
	}
}

// .orion/ plus one file the operator actually produced must still keep the
// worktree, and the refusal must name the operator's file -- not either of
// Orion's own artefacts sitting next to it.
func TestRuntimeDirPlusOperatorFileStillKeepsTheWorktree(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/runtime-dir-with-work")
	writeOrionRuntimeFile(t, j.Path, "state/run.json", "{}\n")
	if err := os.WriteFile(filepath.Join(j.Path, "notes.txt"), []byte("do not lose this\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RemoveMergedWorktree(ws, j.Path)
	if err == nil {
		t.Fatal("removed a worktree holding a file the operator wrote")
	}
	if !strings.Contains(err.Error(), "notes.txt") {
		t.Errorf("the refusal must name what it is protecting: %v", err)
	}
	if strings.Contains(err.Error(), ".orion/") {
		t.Errorf("Orion's own runtime directory was reported as a reason to keep: %v", err)
	}
}

// The dirt check has to read the SAME plans path config.Load() resolves, not
// a literal "plans/BLOCKED.md" -- a project that configures paths.plans
// elsewhere would otherwise have its real stop-note reported as the
// operator's work, and the literal "plans/BLOCKED.md" (unused by that
// project) would be wrongly ignored if anyone ever wrote to it.
func TestDirtRespectsTheConfiguredPlansPath(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/custom-plans-path")
	if err := os.WriteFile(filepath.Join(j.Path, "orion.json"),
		[]byte(`{"paths":{"plans":"docs/plans"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Committed: orion.json is the project's own tracked config, not
	// something this test means to exercise as "operator work in progress".
	gitT(t, j.Path, "add", "-f", "orion.json")
	gitT(t, j.Path, "commit", "-q", "-m", "configure a custom plans path")

	// The note at the CONFIGURED path is Orion's own and must be ignored.
	if err := os.MkdirAll(filepath.Join(j.Path, "docs", "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.Path, "docs", "plans", "BLOCKED.md"),
		[]byte("tripped\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, detail := Dirty(j.Path); dirty {
		t.Fatalf("a note at the configured plans path was not recognised as Orion's own: %s", detail)
	}

	// A file at the default, UNCONFIGURED "plans/BLOCKED.md" is not the
	// breaker's note for this project and must still be treated as work to
	// protect -- resolving by the hardcoded default would ignore it.
	writeBlockedNote(t, j.Path)
	dirty, detail := Dirty(j.Path)
	if !dirty {
		t.Fatal("a file at the unconfigured default plans path was ignored as if it were Orion's note")
	}
	if !strings.Contains(detail, "plans/BLOCKED.md") {
		t.Errorf("expected the default-path file to be reported, got: %s", detail)
	}
}

// No orion.json at all -- or one that sets paths.plans to "" -- must still
// resolve to the shipped default ("plans"), the same path the breaker itself
// falls back to. config.Load() guarantees this by filling empty fields from
// Defaults(); this pins that the dirt check actually goes through Load()
// rather than reading the raw (possibly empty) field.
func TestDirtDefaultsThePlansPathWhenConfigIsAbsentOrEmpty(t *testing.T) {
	ws := sandbox(t)

	j1, _ := AddWorktree(ws, "develop", "orion/no-config-file")
	writeBlockedNote(t, j1.Path)
	if dirty, detail := Dirty(j1.Path); dirty {
		t.Fatalf("with no orion.json, the default plans path was not applied: %s", detail)
	}

	j2, _ := AddWorktree(ws, "develop", "orion/empty-plans-config")
	if err := os.WriteFile(filepath.Join(j2.Path, "orion.json"),
		[]byte(`{"paths":{"plans":""}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, j2.Path, "add", "-f", "orion.json")
	gitT(t, j2.Path, "commit", "-q", "-m", "empty plans path in config")
	writeBlockedNote(t, j2.Path)
	if dirty, detail := Dirty(j2.Path); dirty {
		t.Fatalf("with an empty paths.plans, the default was not applied: %s", detail)
	}
}

// A blocked run that was never pushed must stay on disk even when the ONLY
// uncommitted files in it are Orion's own -- the unpushed-commits guard is a
// separate question from the dirt check, and ignoring Orion's artefacts for
// one must not quietly relax the other.
func TestUnpushedRunWithOnlyOrionArtifactsStillKept(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/unpushed-with-only-orion-artifacts")
	if err := os.WriteFile(filepath.Join(j.Path, "half.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, j.Path, "add", ".")
	gitT(t, j.Path, "commit", "-q", "-m", "as far as it got")
	writeBlockedNote(t, j.Path)
	writeOrionRuntimeFile(t, j.Path, "state/run.json", "{}\n")

	if dirty, detail := Dirty(j.Path); dirty {
		t.Fatalf("Orion's own artefacts alone were counted as uncommitted work: %s", detail)
	}
	err := RemoveWorktree(ws, j.Path, false)
	if err == nil || !strings.Contains(err.Error(), "not on the remote") {
		t.Fatalf("a blocked, unpushed run must stay, and for the unpushed-commits reason, got: %v", err)
	}
}

// A tracked (committed) BLOCKED.md that also happens to be freshly staged
// (status "A ", not "M ") must be protected too -- the rule is "anything but
// ??", not "anything but M".
func TestStagedBlockedNoteIsProtected(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/staged-note")
	writeBlockedNote(t, j.Path)
	gitT(t, j.Path, "add", "-f", "plans/BLOCKED.md")

	dirty, detail := Dirty(j.Path)
	if !dirty {
		t.Fatal("a staged BLOCKED.md was ignored as if it were Orion's untracked note")
	}
	if !strings.Contains(detail, "BLOCKED.md") {
		t.Errorf("expected the staged note to be named, got: %s", detail)
	}
}

// A file Orion wrote into .orion/ that somebody force-added and later
// modified is a TRACKED, uncommitted change -- exactly the case the deletion
// guard exists for (OR-122) -- and the ticket's own spec calls it out
// separately from the untracked case .orion/ is meant for. orionAuthored's
// ".orion/" branch currently matches on path alone, before it looks at
// status, so this is expected to catch that gap.
func TestTrackedChangesUnderOrionRuntimeDirAreProtected(t *testing.T) {
	ws := sandbox(t)
	j, _ := AddWorktree(ws, "develop", "orion/tracked-runtime-dir-change")
	writeOrionRuntimeFile(t, j.Path, "config.json", `{"v":1}`+"\n")
	gitT(t, j.Path, "add", "-f", ".orion/config.json")
	gitT(t, j.Path, "commit", "-q", "-m", "someone force-committed .orion/config.json")
	if err := os.WriteFile(filepath.Join(j.Path, ".orion", "config.json"),
		[]byte(`{"v":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty, detail := Dirty(j.Path)
	if !dirty {
		t.Fatal("a tracked, modified file under .orion/ was ignored as Orion's own scratch " +
			"(orionAuthored matches the \".orion/\" prefix before checking status, " +
			"so a tracked change there is dropped the same as an untracked one)")
	}
	if !strings.Contains(detail, ".orion/config.json") {
		t.Errorf("expected the tracked change to be named, got: %s", detail)
	}
}

// writeBlockedNote writes the file the breaker writes when it trips.
func writeBlockedNote(t *testing.T, worktree string) {
	t.Helper()
	dir := filepath.Join(worktree, "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "BLOCKED.md"),
		[]byte("# Blocked\n\nthe breaker tripped\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeOrionRuntimeFile writes a file under .orion/ the way Orion itself
// would -- state, logs, whatever -- at the given path relative to .orion/.
func writeOrionRuntimeFile(t *testing.T, worktree, rel, body string) {
	t.Helper()
	full := filepath.Join(worktree, ".orion", rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
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
