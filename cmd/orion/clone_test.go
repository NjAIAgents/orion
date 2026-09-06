package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@x"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// stubWorkspace is a workspace whose repository is wherever the test put it.
// RepoPath overrides the derived sandbox path, which is the whole reason the
// field exists.
func stubWorkspace(t *testing.T, repo string) *workspace.Workspace {
	t.Helper()
	return &workspace.Workspace{ID: "thing", RepoPath: repo}
}

func TestTildeAndRelativePathsAreExpanded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	got, err := expandPath("~/code/thing")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "code", "thing"); got != want {
		t.Errorf("expandPath(~) = %q, want %q", got, want)
	}
	// A tilde left unexpanded creates a directory literally named "~", which
	// is the failure this exists to prevent.
	if strings.Contains(got, "~") {
		t.Errorf("the tilde survived: %q", got)
	}

	abs, err := expandPath("relative/path")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(abs) {
		t.Errorf("expandPath returned a relative path: %q", abs)
	}

	if _, err := expandPath("   "); err == nil {
		t.Error("an empty path was accepted")
	}
}

// Cloning ONTO an existing directory is how someone loses uncommitted work in
// it. "It already exists" is recoverable; an overwrite is not.
func TestCloningOntoAnExistingDirectoryIsRefused(t *testing.T) {
	src := filepath.Join(t.TempDir(), "repo")
	gitInit(t, src)
	dest := t.TempDir() // exists

	ws := stubWorkspace(t, src)
	err := cloneWorkspace(os.Stdout, ws, dest)
	if err == nil {
		t.Fatal("cloning onto an existing directory was allowed")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// A workspace whose repository has no commits yet cannot be cloned, and must
// say what to run rather than failing with git's own message.
func TestCloningBeforeThereIsARepositorySaysWhatToRun(t *testing.T) {
	src := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := stubWorkspace(t, src)

	err := cloneWorkspace(os.Stdout, ws, filepath.Join(t.TempDir(), "dest"))
	if err == nil {
		t.Fatal("cloning a non-repository succeeded")
	}
	if !strings.Contains(err.Error(), "orion plan") {
		t.Errorf("the error does not name the command that would fix it: %v", err)
	}
}

func TestACloneCarriesTheCommittedWork(t *testing.T) {
	src := filepath.Join(t.TempDir(), "repo")
	gitInit(t, src)
	dest := filepath.Join(t.TempDir(), "mine")

	ws := stubWorkspace(t, src)
	if err := cloneWorkspace(os.Stdout, ws, dest); err != nil {
		t.Fatalf("cloneWorkspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("the clone does not carry the committed file: %v", err)
	}
	// Its origin is the sandbox, so a later `git pull` brings across what the
	// stages commit next.
	out, err := exec.Command("git", "-C", dest, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatalf("reading origin: %v", err)
	}
	if !strings.Contains(string(out), filepath.Base(src)) {
		t.Errorf("origin is not the sandbox: %s", out)
	}
}
