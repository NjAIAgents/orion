package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
