package collect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

// Two workspaces built from the same idea within one test must be
// independently openable AND independently saveable: a Save on one must not
// bleed into the other's task.json, since both started life as copies of the
// same seed tree (OR-315).
func TestTwoWorkspacesFromSameIdeaAreIndependentlySavable(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())

	first, second := newWorkspace(t, "indep"), newWorkspace(t, "indep")
	if first.Dir == second.Dir {
		t.Fatalf("both workspaces were provisioned at %s", first.Dir)
	}

	first.Task.Status = "first-status"
	if err := first.SaveTask(); err != nil {
		t.Fatalf("saving the first workspace: %v", err)
	}
	second.Task.Status = "second-status"
	if err := second.SaveTask(); err != nil {
		t.Fatalf("saving the second workspace: %v", err)
	}

	reopenedFirst, err := workspace.Open(first.ID)
	if err != nil {
		t.Fatalf("reopening the first workspace: %v", err)
	}
	reopenedSecond, err := workspace.Open(second.ID)
	if err != nil {
		t.Fatalf("reopening the second workspace: %v", err)
	}
	if reopenedFirst.Task.Status != "first-status" {
		t.Errorf("first workspace status = %q, want %q (second workspace's save leaked in)",
			reopenedFirst.Task.Status, "first-status")
	}
	if reopenedSecond.Task.Status != "second-status" {
		t.Errorf("second workspace status = %q, want %q (first workspace's save leaked in)",
			reopenedSecond.Task.Status, "second-status")
	}
}

// copyTree must reproduce both the tree shape and each file's executable bit
// exactly: a git repository and a workspace tree both carry hooks and
// scripts whose exec bit matters to whatever runs them next.
func TestCopyTreePreservesExecutableBitsAndStructure(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(src, "plain.txt")
	if err := os.WriteFile(plain, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(src, "sub", "run.sh")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(src, "sub", "nested", "deep.txt")
	if err := os.WriteFile(nested, []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	copyTree(t, src, dst)

	for rel, wantMode := range map[string]os.FileMode{
		"plain.txt":                                0o644,
		filepath.Join("sub", "run.sh"):             0o755,
		filepath.Join("sub", "nested", "deep.txt"): 0o644,
	} {
		info, err := os.Stat(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("copied file %s missing: %v", rel, err)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Errorf("%s copied with mode %o, want %o", rel, got, wantMode)
		}
	}
	if info, err := os.Stat(filepath.Join(dst, "sub", "nested")); err != nil || !info.IsDir() {
		t.Errorf("nested directory structure was not preserved under %s", dst)
	}
}

// git's background maintenance writes files under .git/objects and removes
// them again mid-walk (OR-293); copyTreeErr must tolerate a file vanishing
// between being listed and being read rather than failing the whole copy. A
// dangling symlink reproduces the same os.ReadFile-sees-ErrNotExist path
// deterministically, without racing a real git process.
func TestCopyTreeToleratesAFileThatDisappearsDuringCopy(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "stays.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "gone-before-read"), filepath.Join(src, "transient")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyTreeErr(src, dst); err != nil {
		t.Fatalf("copyTreeErr failed on a transient file instead of skipping it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stays.txt")); err != nil {
		t.Errorf("the real file was not copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "transient")); err == nil {
		t.Errorf("the vanished file was copied anyway")
	}
}

// seedWorkspace must build once per unique idea for the life of the binary:
// three calls with the same idea return the same directory, and a different
// idea gets its own.
func TestSeedWorkspaceReturnsTheSameDirForRepeatedCalls(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())

	a1 := seedWorkspace(t, "seedonce-a")
	a2 := seedWorkspace(t, "seedonce-a")
	a3 := seedWorkspace(t, "seedonce-a")
	if a1 != a2 || a2 != a3 {
		t.Errorf("seedWorkspace rebuilt for the same idea: %s, %s, %s", a1, a2, a3)
	}

	b := seedWorkspace(t, "seedonce-b")
	if b == a1 {
		t.Errorf("a different idea got the same seed directory %s", b)
	}
}
