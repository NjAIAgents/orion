package ciscaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pythonRepo is a git repository with the given manifests, which is what the
// sandbox clone is.
func pythonRepo(t *testing.T, manifests map[string]string) string {
	t.Helper()
	d := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", d).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, body := range manifests {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			t.Skip("no python on PATH")
		}
	}
}

// The whole point: a sandbox clone that declares dependencies comes out of
// adoption with a virtualenv, so scripts/test.sh's git-common-dir fallback
// has something to find and the per-worktree bootstrap never runs.
func TestEnsureVenvBuildsTheEnvironmentTheTestScriptLooksFor(t *testing.T) {
	requirePython(t)
	dir := pythonRepo(t, map[string]string{"requirements.txt": ""})

	res, err := EnsureVenv(dir)
	if err != nil {
		t.Fatalf("EnsureVenv: %v", err)
	}
	if res.Action != "created" {
		t.Fatalf("Action = %q, want created (%s)", res.Action, res.Reason)
	}
	// scripts/test.sh tests exactly this path, in the main worktree, before
	// it falls through to building one of its own.
	//
	// Through venvPython rather than a literal, because the layout differs by
	// platform -- Scripts\python.exe on Windows -- and OR-334 taught the
	// product code that while leaving this assertion behind (OR-341).
	if _, err := os.Stat(venvPython(filepath.Join(dir, ".venv"))); err != nil {
		t.Fatalf("the virtualenv's python is not where EnsureVenv puts it: %v", err)
	}
}

// Building an empty virtualenv for a repository that never asked for one is
// a surprise, not a help -- and it is the same condition scripts/test.sh
// guards its own bootstrap with.
func TestEnsureVenvSkipsAProjectThatDeclaresNoDependencies(t *testing.T) {
	dir := pythonRepo(t, map[string]string{"main.go": "package main"})

	res, err := EnsureVenv(dir)
	if err != nil {
		t.Fatalf("EnsureVenv: %v", err)
	}
	if res.Action != "skipped" {
		t.Errorf("Action = %q, want skipped", res.Action)
	}
	if res.Reason == "" {
		t.Error("a skip with no reason tells the reader nothing")
	}
	if _, err := os.Stat(filepath.Join(dir, ".venv")); !os.IsNotExist(err) {
		t.Error(".venv was built for a project with no dependency manifest")
	}
}

// Adoption is re-run to repair things, and a second run must not pay for the
// install again.
func TestEnsureVenvIsIdempotent(t *testing.T) {
	requirePython(t)
	dir := pythonRepo(t, map[string]string{"requirements.txt": ""})

	if _, err := EnsureVenv(dir); err != nil {
		t.Fatalf("first EnsureVenv: %v", err)
	}
	res, err := EnsureVenv(dir)
	if err != nil {
		t.Fatalf("second EnsureVenv: %v", err)
	}
	if res.Action != "current" {
		t.Errorf("Action = %q, want current: a re-run reinstalled dependencies nothing had changed", res.Action)
	}
}

// A dependency change must reach the environment. Without this the sandbox
// serves a stale virtualenv forever, and the failure looks like a broken
// test rather than a missing package.
func TestEnsureVenvReinstallsWhenAManifestIsNewer(t *testing.T) {
	requirePython(t)
	dir := pythonRepo(t, map[string]string{"requirements.txt": ""})

	if _, err := EnsureVenv(dir); err != nil {
		t.Fatalf("first EnsureVenv: %v", err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, "requirements.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	res, err := EnsureVenv(dir)
	if err != nil {
		t.Fatalf("second EnsureVenv: %v", err)
	}
	if res.Action != "refreshed" {
		t.Errorf("Action = %q, want refreshed: an edited manifest did not trigger a reinstall", res.Action)
	}
}

// The sandbox clone is fast-forwarded between runs, and SyncSandbox refuses
// to do that when the tree is dirty. An untracked .venv/ in a repository
// whose .gitignore does not list one would freeze the sandbox at the commit
// it was created from -- silently, since nothing fails.
func TestEnsureVenvLeavesTheSandboxCloneClean(t *testing.T) {
	requirePython(t)
	dir := pythonRepo(t, map[string]string{"requirements.txt": ""})

	if _, err := EnsureVenv(dir); err != nil {
		t.Fatalf("EnsureVenv: %v", err)
	}
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.Contains(string(out), ".venv") {
		t.Fatalf("the virtualenv makes the sandbox clone dirty, which blocks its fast-forward:\n%s", out)
	}

	// Asserted directly rather than only through git status: venv writes a
	// .gitignore inside itself from Python 3.11 on, so on a new enough
	// interpreter the status check above would pass with this exclude gone --
	// and quietly stop protecting anyone running an older python.
	excl, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("reading .git/info/exclude: %v", err)
	}
	if !strings.Contains(string(excl), ".venv/") {
		t.Errorf(".venv/ was not excluded locally; on Python < 3.11 the sandbox clone would read as dirty:\n%s", excl)
	}
}

// Once, not once per adoption: the exclude is appended to a file the user's
// own clone-local ignores live in.
func TestEnsureVenvExcludesTheVirtualenvOnlyOnce(t *testing.T) {
	requirePython(t)
	dir := pythonRepo(t, map[string]string{"requirements.txt": ""})

	for i := 0; i < 3; i++ {
		if _, err := EnsureVenv(dir); err != nil {
			t.Fatalf("EnsureVenv #%d: %v", i+1, err)
		}
		// Force the next call down the refresh path, which is the one that
		// writes the exclude.
		later := time.Now().Add(time.Duration(i+2) * time.Second)
		if err := os.Chtimes(filepath.Join(dir, "requirements.txt"), later, later); err != nil {
			t.Fatal(err)
		}
	}
	excl, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(excl), ".venv/"); n != 1 {
		t.Errorf(".venv/ appears %d times in .git/info/exclude, want 1", n)
	}
}
