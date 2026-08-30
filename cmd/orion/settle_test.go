package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// settleRepo builds a worktree in the state a stuck job leaves behind: one
// commit on a branch, and whatever the caller writes after it.
func settleRepo(t *testing.T) workspace.Job {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=orion/or-217"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@t"},
		{"commit", "--allow-empty", "-m", "feat: implement"},
	} {
		if err := runGit(t, dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	return workspace.Job{Key: "OR-217", Branch: "orion/or-217", Path: dir}
}

// settleWorktreeRepo builds the same starting state as settleRepo, but as a
// genuine LINKED git worktree rather than a plain repo.
//
// `settleRefusal` resolves MERGE_HEAD/CHERRY_PICK_HEAD/REVERT_HEAD via `git
// rev-parse --git-path`, and that only returns a path independent of the
// calling process's own working directory when it is run against a linked
// worktree -- exactly the shape every real orion job checkout has, and the
// reason settle.go reads state that way instead of guessing `.git/<file>` by
// hand. A plain repo (settleRepo) returns a path relative to itself, which
// happens to still get caught for mid-rebase (rebase leaves HEAD detached,
// and that check is independent of cwd) but silently passes for a mid-merge,
// mid-cherry-pick or mid-revert conflict, since nothing else about those
// states is visible in `git status --porcelain`.
func settleWorktreeRepo(t *testing.T) workspace.Job {
	t.Helper()
	main := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "--initial-branch=main"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@t"},
		{"commit", "-q", "--allow-empty", "-m", "feat: implement"},
	} {
		if err := runGit(t, main, args...); err != nil {
			t.Fatal(err)
		}
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := runGit(t, main, "worktree", "add", "-q", "-b", "orion/or-217", wt, "main"); err != nil {
		t.Fatal(err)
	}
	return workspace.Job{Key: "OR-217", Branch: "orion/or-217", Path: wt}
}

// OR-233, the second half. The automatic path is fixed, but a killed process
// or a full disk leaves the same artefact, and the only recovery available on
// OR-217 was an operator being talked through `cd`-ing into a hashed path
// under ORION_HOME and running git against an agent's branch.
//
// One command, given only the ticket key, has to end that.
func TestSettleCommitsAStuckWorktree(t *testing.T) {
	job := settleRepo(t)
	// Staged and never committed -- the OR-217 shape.
	if err := os.WriteFile(filepath.Join(job.Path, "qa_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "add", "qa_test.go"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := settleJob(&out, job, false); code != 0 {
		t.Fatalf("settleJob() = %d, want 0\n%s", code, out.String())
	}

	// The condition collect's rebaseOnto actually tests before it will replay
	// a branch. Nothing else about this command matters if this is not true.
	dirty, err := workspace.DirtyTracked(job.Path)
	if err != nil {
		t.Fatal(err)
	}
	if dirty != "" {
		t.Errorf("the worktree is still stuck, so the branch still cannot rebase:\n%s", dirty)
	}
	// Committed, not discarded.
	files, err := gitIn(job.Path, "show", "--name-only", "--format=", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files, "qa_test.go") {
		t.Errorf("the work was not committed:\n%s", files)
	}
	// It reports what it found and what it did, both.
	o := out.String()
	for _, want := range []string{"qa_test.go", "uncommitted tracked file(s)", "settled"} {
		if !strings.Contains(o, want) {
			t.Errorf("the report does not say %q:\n%s", want, o)
		}
	}
	// And the commit says a person had to intervene, which is a fact about
	// Orion that `git log` should still carry in a month.
	msg, err := gitIn(job.Path, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "orion settle OR-217") {
		t.Errorf("the commit does not record how it got there:\n%s", msg)
	}
}

// Orion's own runtime files are not what blocks a rebase and must not ride
// along on the branch. Same exclusions as the automatic path, for the same
// reason: plans/BLOCKED.md is written for whoever opens the worktree.
func TestSettleLeavesBlockedAndTheRuntimeDirectoryOutOfTheCommit(t *testing.T) {
	job := settleRepo(t)
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(job.Path, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("impl.go", "package x\n")
	if err := runGit(t, job.Path, "add", "impl.go"); err != nil {
		t.Fatal(err)
	}
	write("plans/BLOCKED.md", "## breaker/loop tripped\n")
	write(".orion/state/sess.json", "{}\n")

	var out strings.Builder
	if code := settleJob(&out, job, false); code != 0 {
		t.Fatalf("settleJob() = %d, want 0\n%s", code, out.String())
	}

	files, err := gitIn(job.Path, "show", "--name-only", "--format=", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files, "impl.go") {
		t.Errorf("the work was not committed:\n%s", files)
	}
	for _, unwanted := range []string{"BLOCKED.md", ".orion"} {
		if strings.Contains(files, unwanted) {
			t.Errorf("%s rode along on the branch:\n%s", unwanted, files)
		}
	}
}

// "Refuses rather than guessing when the state is not one it understands."
//
// An unmerged path committed wholesale records the conflict markers as if
// they were the resolution -- the exact mistake `orion conflict verify`
// exists to catch after the fact. A recovery tool that does that quietly is
// worse than the manual path it replaces.
func TestSettleRefusesAWorktreeHoldingAConflict(t *testing.T) {
	job := settleRepo(t)

	// A real conflict, produced by a real merge, so what this reads is git's
	// own account of the state rather than a shape the test invented.
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(job.Path, "impl.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base\n")
	for _, args := range [][]string{
		{"add", "impl.go"}, {"commit", "-m", "base"},
		{"checkout", "-b", "other"},
	} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("theirs\n")
	for _, args := range [][]string{
		{"commit", "-am", "theirs"},
		{"checkout", "orion/or-217"},
	} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("ours\n")
	if err := runGit(t, job.Path, "commit", "-am", "ours"); err != nil {
		t.Fatal(err)
	}
	// Expected to fail: that is the point.
	if err := runGit(t, job.Path, "merge", "other"); err == nil {
		t.Fatal("the merge was supposed to conflict")
	}

	var out strings.Builder
	if code := settleJob(&out, job, false); code == 0 {
		t.Fatalf("settleJob() = 0 on a conflicted worktree; it committed a half-resolution\n%s", out.String())
	}
	o := out.String()
	if !strings.Contains(o, "refusing to settle") || !strings.Contains(o, "unmerged paths") {
		t.Errorf("the refusal was not reported, or not for the reason that matters:\n%s", o)
	}
	// A refusal is only useful with the way out attached.
	if !strings.Contains(o, "orion sandbox OR-217 --path") {
		t.Errorf("the refusal does not say how to resolve it:\n%s", o)
	}
	// And it changed nothing.
	if head, err := gitIn(job.Path, "log", "-1", "--format=%s"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(head, "ours") {
		t.Errorf("the refusal still committed something:\n%s", head)
	}
}

// The harder half of the same refusal: a rebase whose conflict has already
// been RESOLVED and staged has no unmerged path left to see, and looks exactly
// like an ordinary dirty worktree.
//
// Committing there is worse than committing a conflict. HEAD is detached
// mid-rebase, so the commit lands on no branch, `git rebase --continue` no
// longer has the state it expects, and the work is reachable from nothing --
// which is the outcome this whole ticket exists to prevent.
func TestSettleRefusesAWorktreeMidRebase(t *testing.T) {
	job := settleRepo(t)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(job.Path, "impl.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base\n")
	for _, args := range [][]string{
		{"add", "impl.go"}, {"commit", "-m", "base"},
		{"checkout", "-b", "other"},
	} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("theirs\n")
	for _, args := range [][]string{
		{"commit", "-am", "theirs"},
		{"checkout", "orion/or-217"},
	} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("ours\n")
	if err := runGit(t, job.Path, "commit", "-am", "ours"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "rebase", "other"); err == nil {
		t.Fatal("the rebase was supposed to conflict")
	}
	// Resolved and staged, but not continued. Nothing in the porcelain says
	// "conflict" any more.
	write("resolved\n")
	if err := runGit(t, job.Path, "add", "impl.go"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := settleJob(&out, job, false); code == 0 {
		t.Fatalf("settleJob() = 0 mid-rebase; it committed onto a detached HEAD\n%s", out.String())
	}
	if o := out.String(); !strings.Contains(o, "refusing to settle") || !strings.Contains(o, "rebase") {
		t.Errorf("the refusal does not name the rebase it stopped for:\n%s", o)
	}
}

// A worktree that is not stuck is reported as such and left alone. A recovery
// command that manufactures a commit every time it is run trains people not
// to run it.
func TestSettleCommitsNothingWhenNothingIsBlocking(t *testing.T) {
	job := settleRepo(t)
	// Untracked files do not block a rebase -- collect passes
	// --untracked-files=no for exactly that reason -- so one here must not be
	// mistaken for residue.
	if err := os.WriteFile(filepath.Join(job.Path, "scratch.txt"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := gitIn(job.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if code := settleJob(&out, job, false); code != 0 {
		t.Fatalf("settleJob() = %d, want 0\n%s", code, out.String())
	}
	after, err := gitIn(job.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("a clean worktree was committed anyway")
	}
	if o := out.String(); !strings.Contains(o, "nothing is blocking") {
		t.Errorf("the report does not say the branch is fine:\n%s", o)
	}
}

// A worktree mid-merge with no conflict at all -- two branches touching
// different files -- still has MERGE_HEAD set and the merge unfinished.
// Committing here would finish somebody else's merge under the "settle"
// label, discarding the distinction between the two.
func TestSettleRefusesAWorktreeMidMerge(t *testing.T) {
	job := settleWorktreeRepo(t)
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(job.Path, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"checkout", "-b", "other"}} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("other.go", "other\n")
	for _, args := range [][]string{{"add", "other.go"}, {"commit", "-m", "add other"}, {"checkout", "orion/or-217"}} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("mine.go", "mine\n")
	for _, args := range [][]string{{"add", "mine.go"}, {"commit", "-m", "add mine"}} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Not a conflict: different files, so this succeeds and merely leaves the
	// merge unfinished.
	if err := runGit(t, job.Path, "merge", "--no-commit", "--no-ff", "other"); err != nil {
		t.Fatalf("the merge was supposed to succeed without a commit: %v", err)
	}

	var out strings.Builder
	if code := settleJob(&out, job, false); code == 0 {
		t.Fatalf("settleJob() = 0 mid-merge; it committed someone else's merge\n%s", out.String())
	}
	if o := out.String(); !strings.Contains(o, "refusing to settle") || !strings.Contains(o, "merge") {
		t.Errorf("the refusal does not name the merge it stopped for:\n%s", o)
	}
}

// The conflict-resolved-and-staged half of the cherry-pick case, matching the
// rebase test above: once resolved, nothing in the porcelain says "conflict"
// but CHERRY_PICK_HEAD is still there, and finishing it silently under
// "settle" would hide that a cherry-pick was in flight.
func TestSettleRefusesAWorktreeMidCherryPick(t *testing.T) {
	job := settleWorktreeRepo(t)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(job.Path, "shared.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base\n")
	for _, args := range [][]string{{"add", "shared.go"}, {"commit", "-m", "base"}, {"checkout", "-b", "other"}} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("theirs\n")
	for _, args := range [][]string{{"commit", "-am", "theirs"}} {
		if err := runGit(t, job.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	otherHead, err := gitIn(job.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "checkout", "orion/or-217"); err != nil {
		t.Fatal(err)
	}
	write("ours\n")
	if err := runGit(t, job.Path, "commit", "-am", "ours"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "cherry-pick", strings.TrimSpace(otherHead)); err == nil {
		t.Fatal("the cherry-pick was supposed to conflict")
	}
	// Resolved and staged, but never continued.
	write("resolved\n")
	if err := runGit(t, job.Path, "add", "shared.go"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := settleJob(&out, job, false); code == 0 {
		t.Fatalf("settleJob() = 0 mid-cherry-pick; it committed onto a half-finished cherry-pick\n%s", out.String())
	}
	if o := out.String(); !strings.Contains(o, "refusing to settle") || !strings.Contains(o, "cherry-pick") {
		t.Errorf("the refusal does not name the cherry-pick it stopped for:\n%s", o)
	}
}

// Same shape again for revert: REVERT_HEAD survives a resolved-and-staged
// conflict until the revert is explicitly continued or aborted.
func TestSettleRefusesAWorktreeMidRevert(t *testing.T) {
	job := settleWorktreeRepo(t)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(job.Path, "shared.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("v1\n")
	if err := runGit(t, job.Path, "add", "shared.go"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "commit", "-m", "v1"); err != nil {
		t.Fatal(err)
	}
	write("v2\n")
	if err := runGit(t, job.Path, "commit", "-am", "v2"); err != nil {
		t.Fatal(err)
	}
	v2, err := gitIn(job.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	write("v3\n")
	if err := runGit(t, job.Path, "commit", "-am", "v3"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "revert", "--no-edit", strings.TrimSpace(v2)); err == nil {
		t.Fatal("the revert was supposed to conflict")
	}
	write("resolved\n")
	if err := runGit(t, job.Path, "add", "shared.go"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := settleJob(&out, job, false); code == 0 {
		t.Fatalf("settleJob() = 0 mid-revert; it committed onto a half-finished revert\n%s", out.String())
	}
	if o := out.String(); !strings.Contains(o, "refusing to settle") || !strings.Contains(o, "revert") {
		t.Errorf("the refusal does not name the revert it stopped for:\n%s", o)
	}
}

// A detached HEAD is refused even though nothing else about the tree looks
// unusual: the danger is only that a commit here is reachable from no
// branch, which `git status` alone does not warn about.
func TestSettleRefusesADetachedHead(t *testing.T) {
	job := settleRepo(t)
	sha, err := gitIn(job.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "checkout", strings.TrimSpace(sha)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(job.Path, "impl.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "add", "impl.go"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := settleJob(&out, job, false); code == 0 {
		t.Fatalf("settleJob() = 0 on a detached HEAD; the commit would be reachable from nothing\n%s", out.String())
	}
	o := out.String()
	if !strings.Contains(o, "refusing to settle") || !strings.Contains(o, "detached") {
		t.Errorf("the refusal does not name the detached HEAD:\n%s", o)
	}
	if !strings.Contains(o, "git checkout") {
		t.Errorf("the refusal does not say how to resolve it:\n%s", o)
	}
	// Unchanged: still detached, still nothing committed.
	if head, err := gitIn(job.Path, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		t.Errorf("HEAD is no longer detached after the refusal: %s", head)
	}
}

// The commit itself has to say the work was never verified -- the whole
// point of the wording being distinct from the automatic path's is that a
// reader still needs to know NOT to trust it just because a person ran the
// command.
func TestSettleCommitMessageSaysTheWorkWasNotVerified(t *testing.T) {
	msg := msgOperatorSettle("OR-217")
	if !strings.Contains(msg, "NOTHING here has been verified") {
		t.Errorf("the commit message does not say the work is unverified:\n%s", msg)
	}
	if !strings.Contains(msg, "orion settle OR-217") {
		t.Errorf("the commit message does not identify it as operator-settled:\n%s", msg)
	}
}

// A worktree this cannot even read -- removed out from under it, or on a
// filesystem gone away -- is reported as an error, not treated as clean.
func TestSettleReportsAnErrorWhenTheWorktreeCannotBeRead(t *testing.T) {
	job := workspace.Job{Key: "OR-217", Branch: "orion/or-217",
		Path: filepath.Join(t.TempDir(), "does-not-exist")}

	var out strings.Builder
	code := settleJob(&out, job, false)
	if code == 0 {
		t.Fatalf("settleJob() = 0 for an unreadable worktree; want non-zero\n%s", out.String())
	}
	if o := out.String(); !strings.Contains(o, "could not read the worktree") {
		t.Errorf("the report does not say the worktree could not be read:\n%s", o)
	}
}

// settleFixture registers a project the way `orion init` does and adds one
// real worktree for it, so findJob can be exercised the way `orion settle`
// actually calls it: by ticket key alone, never a path.
func settleFixture(t *testing.T) (home, key string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("ORION_HOME", home)
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
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

	ws, err := workspace.Bind(workspace.BindOptions{
		SourcePath: seed, DefaultBranch: "main", WorkBranch: "develop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Bind(home, registry.Entry{Key: "FCIA", Source: seed, Workspace: ws.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.AddWorktree(ws, "develop", "orion/fcia-217"); err != nil {
		t.Fatal(err)
	}
	return home, "FCIA-217"
}

// "Command finds the worktree for that key without operator providing a
// path." No path is passed in anywhere here -- only the key.
func TestFindJobLocatesTheWorktreeByKeyAlone(t *testing.T) {
	home, key := settleFixture(t)

	job, _, err := findJob(home, key)
	if err != nil {
		t.Fatalf("findJob(%q) error: %v", key, err)
	}
	if !strings.Contains(job.Branch, "fcia-217") {
		t.Errorf("findJob() found branch %q, want the one for %s", job.Branch, key)
	}
	if _, statErr := os.Stat(job.Path); statErr != nil {
		t.Errorf("findJob() returned a path that does not exist: %v", statErr)
	}
}

// "Settling a ticket key with no worktree fails appropriately." The project
// is registered -- FCIA is a real, adopted repository -- but no run has ever
// created a sandbox for this particular ticket.
func TestFindJobFailsWhenTheTicketHasNoWorktree(t *testing.T) {
	home, _ := settleFixture(t)

	_, _, err := findJob(home, "FCIA-999")
	if err == nil {
		t.Fatal("findJob() succeeded for a ticket with no worktree, want an error")
	}
	if !strings.Contains(err.Error(), "no sandbox for FCIA-999") {
		t.Errorf("findJob() error = %v, want it to name the ticket", err)
	}
}

// A key for a project nobody has ever run `orion init` in should fail the
// same way -- distinctly from "no worktree", but still a plain error rather
// than a panic or a confusing zero value.
func TestFindJobFailsForAnUnregisteredProject(t *testing.T) {
	home := t.TempDir()

	_, _, err := findJob(home, "GHOST-1")
	if err == nil {
		t.Fatal("findJob() succeeded for an unregistered project, want an error")
	}
}

// --dry-run says what it would do and does none of it, so an operator can
// look before letting a tool commit to an agent's branch.
func TestSettleDryRunCommitsNothing(t *testing.T) {
	job := settleRepo(t)
	if err := os.WriteFile(filepath.Join(job.Path, "qa_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, job.Path, "add", "qa_test.go"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := settleJob(&out, job, true); code != 0 {
		t.Fatalf("settleJob() = %d, want 0\n%s", code, out.String())
	}
	dirty, err := workspace.DirtyTracked(job.Path)
	if err != nil {
		t.Fatal(err)
	}
	if dirty == "" {
		t.Error("--dry-run committed the work")
	}
	if o := out.String(); !strings.Contains(o, "would") {
		t.Errorf("the dry run does not say what it would do:\n%s", o)
	}
}
