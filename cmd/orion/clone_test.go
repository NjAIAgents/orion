package main

import (
	"bytes"
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

// Cloning ONTO an existing directory is how someone loses uncommitted work
// in it, and an overwrite is not recoverable. The directory is treated as a
// parent and the copy goes inside it (OR-418) -- what must never happen is
// git writing into the directory itself.
func TestCloningNeverWritesIntoAnExistingDirectoryItself(t *testing.T) {
	src := filepath.Join(t.TempDir(), "repo")
	gitInit(t, src)
	dest := t.TempDir()
	keep := filepath.Join(dest, "someone-elses-work.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := stubWorkspace(t, src)
	if err := cloneWorkspace(os.Stdout, ws, dest); err != nil {
		t.Fatalf("cloning into an existing folder failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		t.Error("git wrote into the directory itself, over what was already there")
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "mine" {
		t.Errorf("the file that was already there did not survive: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dest, ws.ID, ".git")); err != nil {
		t.Errorf("the copy is not in <dir>/<id>: %v", err)
	}
}

// A copy already at <dir>/<id> is still refused, because that IS the target
// and cloning onto it would overwrite a checkout someone may have work in.
func TestASecondCloneToTheSamePlaceIsRefused(t *testing.T) {
	src := filepath.Join(t.TempDir(), "repo")
	gitInit(t, src)
	dest := t.TempDir()

	ws := stubWorkspace(t, src)
	if err := cloneWorkspace(os.Stdout, ws, dest); err != nil {
		t.Fatal(err)
	}
	err := cloneWorkspace(os.Stdout, ws, dest)
	if err == nil {
		t.Fatal("a second clone overwrote the first")
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

// "~/Desktop/github/me" is a folder someone keeps repositories in, and it
// is the honest answer to "where do you want your copy". The copy goes
// inside it under the workspace's own name rather than being refused,
// which named no way forward (OR-418).
func TestCloneIntoADirectoryYouAlreadyKeepCodeIn(t *testing.T) {
	src := gitRepoForClone(t)
	ws := &workspace.Workspace{ID: "cloudlens", Dir: t.TempDir()}
	ws.Task.Remote = src
	parent := t.TempDir() // exists, holds other repositories

	var out bytes.Buffer
	if err := cloneWorkspace(&out, ws, parent); err != nil {
		t.Fatalf("cloning into an existing folder failed: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(parent, "cloudlens", ".git")); err != nil {
		t.Errorf("the copy is not at <parent>/cloudlens: %v", err)
	}
}

// A copy is made from the REMOTE when there is one. A clone of the sandbox
// has a directory under ~/.orion as its origin, which `orion rm` deletes
// and which no pull request can be opened from (OR-418).
func TestCloneUsesTheRemoteRatherThanTheSandbox(t *testing.T) {
	remote := gitRepoForClone(t)
	ws := &workspace.Workspace{ID: "cloudlens", Dir: t.TempDir()}
	ws.Task.Remote = remote
	dest := filepath.Join(t.TempDir(), "copy")

	var out bytes.Buffer
	if err := cloneWorkspace(&out, ws, dest); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out.String())
	}
	got := gitLineIn(t, dest, "remote", "get-url", "origin")
	if got != remote {
		t.Errorf("origin is %q, want the remote %q", got, remote)
	}
	if strings.Contains(got, ".orion") {
		t.Errorf("the copy points back into the sandbox: %q", got)
	}
}

// A workspace with no remote still gets a copy: the sandbox is the only
// source there is, and refusing would be worse than a local origin.
func TestCloneFallsBackToTheSandboxWithoutARemote(t *testing.T) {
	ws := &workspace.Workspace{ID: "cloudlens", Dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Dir(ws.RepoDir()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitRepoForClone(t), ws.RepoDir()); err != nil {
		t.Skip("symlink unavailable")
	}
	dest := filepath.Join(t.TempDir(), "copy")

	var out bytes.Buffer
	if err := cloneWorkspace(&out, ws, dest); err != nil {
		t.Fatalf("clone from the sandbox failed: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Errorf("nothing was cloned: %v", err)
	}
}

// gitRepoForClone makes a repository with one commit, to clone from.
func gitRepoForClone(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"commit", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@l",
			"GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@l")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v\n%s", err, b)
		}
	}
	return dir
}

func gitLineIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(b))
}

// The copy opens on the branch the chain committed to. Cloning leaves you
// on the repository default -- develop, which has none of the planning
// artifacts -- so the work looks lost until you check what branch you are
// on (OR-418).
func TestCloneChecksOutTheBranchTheChainWorkedOn(t *testing.T) {
	src := gitRepoForClone(t)
	runGitOrSkip(t, src, "checkout", "-q", "-b", "orion/001-thing")
	runGitOrSkip(t, src, "commit", "--allow-empty", "-m", "the planning work")
	runGitOrSkip(t, src, "checkout", "-q", "main")

	ws := &workspace.Workspace{ID: "thing", Dir: t.TempDir()}
	ws.Task.Remote = src
	ws.Task.PlanBranch = "orion/001-thing"
	dest := filepath.Join(t.TempDir(), "copy")

	var out bytes.Buffer
	if err := cloneWorkspace(&out, ws, dest); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out.String())
	}
	if got := gitLineIn(t, dest, "rev-parse", "--abbrev-ref", "HEAD"); got != "orion/001-thing" {
		t.Errorf("the copy opened on %q, want the branch the chain worked on", got)
	}
}

// A recorded branch the source does not have must not cost the copy: the
// clone is what was asked for, so it is made without one.
func TestCloneStillCopiesWhenTheRecordedBranchIsGone(t *testing.T) {
	src := gitRepoForClone(t)
	ws := &workspace.Workspace{ID: "thing", Dir: t.TempDir()}
	ws.Task.Remote = src
	ws.Task.PlanBranch = "orion/never-pushed"
	dest := filepath.Join(t.TempDir(), "copy")

	var out bytes.Buffer
	if err := cloneWorkspace(&out, ws, dest); err != nil {
		t.Fatalf("a missing branch cost the whole copy: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Errorf("nothing was cloned: %v", err)
	}
}

func runGitOrSkip(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@l",
		"GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@l")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v\n%s", err, b)
	}
}
