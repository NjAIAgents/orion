package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The bug OR-88 fixed was not in Orion's logic. It was a belief about git
// that is true under one merge strategy and false under the one we use, and
// it survived review because it reads as obviously correct.
//
// These tests pin the git behaviour itself. If a future reader wonders why
// pruneBranch reaches for the blunt -D, this is the answer, executable.

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gitMayFail runs git and returns whether it succeeded, for the cases where
// the refusal IS the thing under test.
func gitMayFail(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// repoWithMergedBranch builds a repository where `feature` has been merged
// into `main` by the named strategy, and returns its path.
func repoWithMergedBranch(t *testing.T, strategy string) string {
	t.Helper()
	dir := t.TempDir()

	gitT(t, dir, "init", "--initial-branch=main")
	gitT(t, dir, "config", "user.email", "test@example.com")
	gitT(t, dir, "config", "user.name", "Test")

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("base.txt", "base\n")
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-m", "base")

	gitT(t, dir, "checkout", "-b", "feature")
	write("feature.txt", "feature\n")
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-m", "feature work")

	// main moves on, so a rebase genuinely has to replay rather than
	// fast-forward. Without this the strategies are indistinguishable and
	// the test proves nothing.
	gitT(t, dir, "checkout", "main")
	write("other.txt", "other\n")
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-m", "unrelated work on main")

	switch strategy {
	case "rebase":
		// What GitHub's "Rebase and merge" does: replay the branch's commits
		// onto the base as NEW objects. The originals stay where they were,
		// unreferenced by main.
		gitT(t, dir, "checkout", "feature")
		gitT(t, dir, "rebase", "main")
		tip := strings.TrimSpace(gitT(t, dir, "rev-parse", "HEAD"))
		gitT(t, dir, "checkout", "main")
		gitT(t, dir, "merge", "--ff-only", tip)
		// Then put feature back where it was before the rebase, which is
		// what the remote branch looks like after GitHub merges it: the
		// branch still points at the ORIGINAL commits.
		gitT(t, dir, "branch", "-f", "feature", "feature@{1}")
	case "squash":
		gitT(t, dir, "merge", "--squash", "feature")
		gitT(t, dir, "commit", "-m", "squashed feature")
	case "merge":
		gitT(t, dir, "merge", "--no-ff", "feature", "-m", "merge feature")
	default:
		t.Fatalf("unknown strategy %q", strategy)
	}
	return dir
}

// TestGitDisagreesAboutMergedForRebaseAndSquash is the regression itself.
//
// pruneBranch used `git branch -d`, whose safety check is an ANCESTRY test.
// Two of the three merge strategies GitHub offers do not preserve ancestry,
// so for those two the check refuses a branch that has demonstrably landed.
// Orion merges by rebase, which is the worst of the three for this.
func TestGitDisagreesAboutMergedForRebaseAndSquash(t *testing.T) {
	cases := []struct {
		strategy string
		// wantMinusDWorks is whether `git branch -d` accepts the deletion.
		wantMinusDWorks bool
	}{
		{"merge", true},   // ancestry preserved; -d is correct here
		{"rebase", false}, // OUR strategy: new SHAs, so -d always refuses
		{"squash", false}, // one new commit, originals unreachable
	}

	for _, c := range cases {
		t.Run(c.strategy, func(t *testing.T) {
			dir := repoWithMergedBranch(t, c.strategy)

			out, ok := gitMayFail(dir, "branch", "-d", "feature")
			if ok != c.wantMinusDWorks {
				t.Fatalf("`git branch -d` succeeded=%v, want %v\n%s", ok, c.wantMinusDWorks, out)
			}

			// Whatever -d decided, -D must remove it. This is what
			// pruneBranch relies on, and it is safe only because the caller
			// has already confirmed MERGED via the pull request.
			if !ok {
				if out, ok := gitMayFail(dir, "branch", "-D", "feature"); !ok {
					t.Fatalf("`git branch -D` refused a merged branch\n%s", out)
				}
			}
			if out, _ := gitMayFail(dir, "rev-parse", "--verify", "feature"); !strings.Contains(out, "fatal") {
				t.Fatalf("branch still present after deletion:\n%s", out)
			}
		})
	}
}

// TestDeleteLocalBranchRemovesWhatOrionMerged is the test that actually
// guards the fix.
//
// The cases above assert git's behaviour, which is true regardless of what
// pruneBranch does -- they would pass just as happily with the old, broken
// flag in place. This one calls OUR function against a branch merged the way
// Orion merges, so reverting -D to -d fails here. Verified by doing exactly
// that before committing.
func TestDeleteLocalBranchRemovesWhatOrionMerged(t *testing.T) {
	for _, strategy := range []string{"rebase", "squash", "merge"} {
		t.Run(strategy, func(t *testing.T) {
			dir := repoWithMergedBranch(t, strategy)

			if out, err := deleteLocalBranch(dir, "feature"); err != nil {
				t.Fatalf("a %s-merged branch was not deleted: %v\n%s\n\n"+
					"If this is a `-d` refusal, that is the OR-88 regression: -d "+
					"tests ancestry, which %s does not preserve.", strategy, err, out, strategy)
			}
			if out, _ := gitMayFail(dir, "rev-parse", "--verify", "feature"); !strings.Contains(out, "fatal") {
				t.Fatalf("branch survived deletion:\n%s", out)
			}
		})
	}
}

// TestDeleteLocalBranchReportsARealObstacle checks the safety that remains.
// A branch checked out in another worktree cannot be deleted by any flag,
// and that refusal must surface rather than be swallowed.
func TestDeleteLocalBranchReportsARealObstacle(t *testing.T) {
	dir := repoWithMergedBranch(t, "rebase")
	gitT(t, dir, "checkout", "feature")

	if _, err := deleteLocalBranch(dir, "feature"); err == nil {
		t.Fatal("deleting the checked-out branch should have failed")
	}
}

// TestAncestryIsNotMergedness states the same fact from the other direction,
// because this is the exact command the staleness gate uses and a reader
// could reasonably assume it answers "has this merged". It does not.
func TestAncestryIsNotMergedness(t *testing.T) {
	dir := repoWithMergedBranch(t, "rebase")

	_, ok := gitMayFail(dir, "merge-base", "--is-ancestor", "feature", "main")
	if ok {
		t.Fatal("expected a rebase-merged branch NOT to be an ancestor of main; " +
			"if this now passes, git changed and pruneBranch can be simplified")
	}
}

// TestAlreadyGoneIsNotAFailure covers the case that became the COMMON one
// the moment init started setting delete_branch_on_merge: GitHub has already
// deleted the remote branch by the time collect gets there.
func TestAlreadyGoneIsNotAFailure(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"deleted server-side at merge", "error: unable to delete 'orion/or-39': remote ref does not exist", true},
		{"unable to delete", "error: unable to delete 'x': remote ref does not exist", true},
		{"no remote configured", "fatal: 'origin' does not appear to be a git repository", true},
		{"case is not significant", "REMOTE REF DOES NOT EXIST", true},
		{"a real refusal is reported", "! [remote rejected] orion/or-39 (protected branch hook declined)", false},
		{"auth failure is reported", "fatal: Authentication failed for 'https://github.com/x/y'", false},
		{"empty output is not a reason to stay quiet", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAlreadyGone(c.out); got != c.want {
				t.Errorf("isAlreadyGone(%q) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}

// TestProtectionFailureNamesTheFreeOption exists because GitHub's own error
// gives incomplete advice: it says to upgrade, and omits that making the
// repository public enables protection at no cost. That omission cost a real
// evening here.
func TestProtectionFailureNamesTheFreeOption(t *testing.T) {
	msg := explainProtectionFailure(
		"Upgrade to GitHub Pro or make this repository public to enable this feature.")
	if !strings.Contains(msg, "public") {
		t.Errorf("the free way out is not mentioned: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "private") {
		t.Errorf("the message should say the limit applies to PRIVATE repos: %q", msg)
	}
}

// `orion sandbox prune` is the command that sweeps the worktrees a tripped run
// left behind, so its own dirt check has to agree with the deletion guard --
// otherwise it keeps a merged checkout over a note Orion wrote itself (OR-220)
// and the backlog of stale worktrees only grows.
func TestSandboxPruneIgnoresOrionsOwnStopNote(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "--initial-branch=main")
	gitT(t, dir, "config", "user.email", "test@example.com")
	gitT(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-m", "base")

	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("plans/BLOCKED.md", "the breaker tripped\n")
	write(".orion/state/run.json", "{}\n")
	if dirty, lines := agentDirt(dir); dirty {
		t.Fatalf("Orion's own files counted as somebody's work: %v", lines)
	}

	write("plans/OR-1.md", "the agent's plan\n")
	dirty, lines := agentDirt(dir)
	if !dirty {
		t.Fatal("a file the agent wrote was not reported")
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "OR-1.md") {
		t.Errorf("expected only the agent's file, got %v", lines)
	}
}

func TestBranchListIsDeduplicated(t *testing.T) {
	// The common single-branch repo: default and work branch are both main,
	// and protection should be applied once, not twice.
	got := uniqueNonEmpty("main", "main", "", "develop")
	want := []string{"main", "develop"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
