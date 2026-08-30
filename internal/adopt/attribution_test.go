package adopt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A real git repository with a commit, which `git worktree add` requires.
// The repo() helper in adopt_test.go only mkdirs .git, and none of these
// tests can use that: every one is about what git itself reports.
func clonedRepo(t *testing.T) string { return gitRepo(t, true) }

func addWorktree(t *testing.T, clone string) string {
	t.Helper()
	// Outside the clone: a worktree nested inside it would be picked up by
	// the clone's own status, which is not how Orion lays them out.
	wt := filepath.Join(t.TempDir(), "job")
	cmd := exec.Command("git", "worktree", "add", "-q", "-b", "job", wt)
	cmd.Dir = clone
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return wt
}

// writeHook plants a hook that names dun, which is what DunLook looks for.
func writeHook(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec dun hook "+name+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The regression this ticket is about: every agent commit is made inside a
// job worktree, where .git is a FILE, so reading <dir>/.git/hooks/<name>
// reported "not instrumented" no matter what was actually installed.
func TestDunLookSeesHooksFromInsideAWorktree(t *testing.T) {
	clone := clonedRepo(t)
	wt := addWorktree(t, clone)

	// Sanity: the thing that broke the old implementation.
	fi, err := os.Stat(filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.IsDir() {
		t.Fatal("expected .git to be a file inside a worktree; this test proves nothing otherwise")
	}

	writeHook(t, filepath.Join(clone, ".git", "hooks"), "prepare-commit-msg")

	if !DunLook(clone).Instrumented {
		t.Fatal("clone: want instrumented")
	}
	if !DunLook(wt).Instrumented {
		t.Fatal("worktree: want instrumented, got not instrumented -- " +
			"a worktree resolves hooks from the clone's common dir")
	}
}

func TestDunLookReportsUninstrumentedWorktreeHonestly(t *testing.T) {
	clone := clonedRepo(t)
	wt := addWorktree(t, clone)

	// No hook anywhere. The fix must not make every worktree report
	// instrumented; that would trade one silent wrong answer for another.
	if DunLook(wt).Instrumented {
		t.Fatal("worktree: want NOT instrumented when no dun hook exists")
	}
}

func TestHooksDirFollowsCoreHooksPath(t *testing.T) {
	clone := clonedRepo(t)
	custom := filepath.Join(clone, ".husky")
	writeHook(t, custom, "commit-msg")

	cmd := exec.Command("git", "config", "core.hooksPath", ".husky")
	cmd.Dir = clone
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	if got := HooksDir(clone); got != custom {
		t.Fatalf("HooksDir = %q, want %q", got, custom)
	}
	// git runs the hooks there, so that is where the answer has to come from.
	if !DunLook(clone).Instrumented {
		t.Fatal("want instrumented: the dun hook is where core.hooksPath points")
	}
}

func TestHooksDirOutsideAGitRepoDoesNotInvent(t *testing.T) {
	d := t.TempDir() // no .git at all
	want := filepath.Join(d, ".git", "hooks")
	if got := HooksDir(d); got != want {
		t.Fatalf("HooksDir = %q, want the plain fallback %q", got, want)
	}
}

// EnsureSandboxDun must not shell out at all once the hooks are there: it
// runs on every job, and a dun invocation per job for a no-op is a cost
// paid N times over for nothing.
func TestEnsureSandboxDunIsANoOpWhenAlreadyInstrumented(t *testing.T) {
	clone := clonedRepo(t)
	writeHook(t, filepath.Join(clone, ".git", "hooks"), "commit-msg")

	// PATH emptied: if this tried to run dun it would fail, and passing
	// proves it did not try.
	t.Setenv("PATH", t.TempDir())
	if err := EnsureSandboxDun(clone); err != nil {
		t.Fatalf("want no-op, got %v", err)
	}
}

// The uninstrumented-and-no-dun case has to report, not shrug. A sandbox
// whose commits carry no trailer while nothing says so is the failure this
// ticket exists to end.
func TestEnsureSandboxDunReportsWhenDunIsMissing(t *testing.T) {
	clone := clonedRepo(t)
	t.Setenv("PATH", t.TempDir())

	err := EnsureSandboxDun(clone)
	if err == nil {
		t.Fatal("want an error naming the missing dun, got nil")
	}
}

// The end-to-end claim: EnsureSandboxDun on the CLONE makes DunLook report
// instrumented from inside a WORKTREE of it. That is the whole fix in one
// assertion -- hooks installed once on the clone, honest from every job, and
// so honest for N concurrent jobs too.
//
// Skipped when dun is absent, because it exercises the real `dun init` and
// there is no honest way to fake that. Everything assertable without the
// binary is covered above; this is the one that needs it.
func TestEnsureSandboxDunInstrumentsEveryWorktree(t *testing.T) {
	if _, err := exec.LookPath("dun"); err != nil {
		t.Skip("dun is not installed")
	}
	// dun keeps its journal and repo registry under $HOME/.whodunit and
	// creates them on init. Redirected so a test run neither reads nor
	// writes the developer's real attribution data.
	t.Setenv("HOME", t.TempDir())

	clone := clonedRepo(t)
	if DunLook(clone).Instrumented {
		t.Fatal("a fresh clone should not be instrumented")
	}
	if err := EnsureSandboxDun(clone); err != nil {
		t.Fatalf("EnsureSandboxDun: %v", err)
	}

	wt := addWorktree(t, clone)
	if !DunLook(wt).Instrumented {
		t.Fatalf("worktree %s reports not instrumented after the clone was", wt)
	}
}
