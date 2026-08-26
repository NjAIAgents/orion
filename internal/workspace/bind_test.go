package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// origin + a working copy that tracks it, mimicking a real adopted repo.
func adopted(t *testing.T) (source, origin string) {
	t.Helper()
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	source = filepath.Join(root, "work")

	gitIn(t, root, "init", "-q", "--bare", "-b", "main", origin)
	gitIn(t, root, "clone", "-q", origin, source)
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, source, "add", ".")
	gitIn(t, source, "commit", "-q", "-m", "first")
	gitIn(t, source, "push", "-q", "-u", "origin", "main")
	gitIn(t, source, "checkout", "-q", "-b", "develop")
	gitIn(t, source, "push", "-q", "-u", "origin", "develop")
	return source, origin
}

// The promise the whole sandbox design rests on: Orion clones from the
// remote and NEVER writes to the user's checkout. Asserted by fingerprinting
// the tree before and after, not by reading the code.
func TestBindNeverWritesToTheWorkingCopy(t *testing.T) {
	source, _ := adopted(t)
	t.Setenv("ORION_HOME", t.TempDir())

	before := treeState(t, source)
	ws, err := Bind(BindOptions{SourcePath: source, DefaultBranch: "main", WorkBranch: "develop"})
	if err != nil {
		t.Fatal(err)
	}
	after := treeState(t, source)

	if before != after {
		t.Errorf("the working copy changed:\n before %s\n after  %s", before, after)
	}
	if _, err := os.Stat(filepath.Join(ws.RepoDir(), "README.md")); err != nil {
		t.Errorf("the sandbox is not a usable checkout: %v", err)
	}
	if ws.Task.SourcePath != source {
		t.Errorf("SourcePath = %q, want the working copy recorded for later refresh", ws.Task.SourcePath)
	}
}

func treeState(t *testing.T, dir string) string {
	t.Helper()
	return gitIn(t, dir, "status", "--porcelain", "-b") + "|" +
		gitIn(t, dir, "rev-parse", "HEAD") + "|" +
		gitIn(t, dir, "branch", "--show-current")
}

// A clone comes from the remote, so uncommitted work is simply absent from
// the sandbox and the agent solves the wrong version of the problem.
func TestBindRefusesUncommittedWork(t *testing.T) {
	source, _ := adopted(t)
	t.Setenv("ORION_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(source, "wip.txt"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Bind(BindOptions{SourcePath: source, WorkBranch: "develop"})
	if err == nil {
		t.Fatal("cloned from a base that does not match what the user sees")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("the refusal must say why, got: %v", err)
	}
	if _, err := Bind(BindOptions{SourcePath: source, WorkBranch: "develop", Force: true}); err != nil {
		t.Errorf("--force must still proceed: %v", err)
	}
}

func TestBindRefusesUnpushedCommits(t *testing.T) {
	source, _ := adopted(t)
	t.Setenv("ORION_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(source, "local.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, source, "add", ".")
	gitIn(t, source, "commit", "-q", "-m", "not pushed")

	_, err := Bind(BindOptions{SourcePath: source, WorkBranch: "develop"})
	if err == nil || !strings.Contains(err.Error(), "unpushed") {
		t.Errorf("expected a refusal naming unpushed commits, got: %v", err)
	}
}

// The sandbox must start from origin/develop, not from a local branch made
// out of main. Getting this wrong put the first real sandbox a commit behind.
func TestBindTracksTheRemoteWorkBranch(t *testing.T) {
	source, _ := adopted(t)
	t.Setenv("ORION_HOME", t.TempDir())

	// A commit that exists only on develop.
	if err := os.WriteFile(filepath.Join(source, "only-develop.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, source, "add", ".")
	gitIn(t, source, "commit", "-q", "-m", "develop only")
	gitIn(t, source, "push", "-q", "origin", "develop")

	ws, err := Bind(BindOptions{SourcePath: source, DefaultBranch: "main", WorkBranch: "develop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.RepoDir(), "only-develop.txt")); err != nil {
		t.Error("the sandbox is behind: it branched from main rather than origin/develop")
	}
	if b := gitIn(t, ws.RepoDir(), "branch", "--show-current"); b != "develop" {
		t.Errorf("sandbox is on %q, want develop", b)
	}
	if up := gitIn(t, ws.RepoDir(), "rev-parse", "--abbrev-ref", "develop@{upstream}"); up != "origin/develop" {
		t.Errorf("develop tracks %q, want origin/develop", up)
	}
}

// Refresh fast-forwards; it must never merge, rebase, or touch a dirty tree.
func TestRefreshFastForwardsACleanCheckout(t *testing.T) {
	source, origin := adopted(t)

	// Someone else advances origin/develop.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", "-b", "develop", origin, other)
	if err := os.WriteFile(filepath.Join(other, "new.txt"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", ".")
	gitIn(t, other, "commit", "-q", "-m", "from elsewhere")
	gitIn(t, other, "push", "-q", "origin", "develop")

	msg, err := Refresh(source, "develop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "fast-forwarded") {
		t.Errorf("msg = %q", msg)
	}
	if _, err := os.Stat(filepath.Join(source, "new.txt")); err != nil {
		t.Error("the checkout was not brought up to date")
	}
}

// The guarantee that matters: a tool that merges into a directory someone
// has open in an editor can destroy work with no undo.
func TestRefreshRefusesToTouchADirtyTree(t *testing.T) {
	source, origin := adopted(t)

	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", "-b", "develop", origin, other)
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", ".")
	gitIn(t, other, "commit", "-q", "-m", "remote work")
	gitIn(t, other, "push", "-q", "origin", "develop")

	// The user is mid-edit.
	precious := filepath.Join(source, "README.md")
	if err := os.WriteFile(precious, []byte("MY UNSAVED EDIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitIn(t, source, "rev-parse", "HEAD")

	msg, err := Refresh(source, "develop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "uncommitted") {
		t.Errorf("msg = %q, want an explanation of what was skipped", msg)
	}
	if got := gitIn(t, source, "rev-parse", "HEAD"); got != head {
		t.Error("HEAD moved under a dirty tree")
	}
	b, _ := os.ReadFile(precious)
	if string(b) != "MY UNSAVED EDIT\n" {
		t.Fatalf("the user's edit was destroyed: %q", b)
	}
}

// Refreshing a branch the user is not on must not check it out from under
// them; it fetches and tells them what to run.
func TestRefreshDoesNotSwitchBranches(t *testing.T) {
	source, _ := adopted(t)
	gitIn(t, source, "checkout", "-q", "main")

	msg, err := Refresh(source, "develop")
	if err != nil {
		t.Fatal(err)
	}
	if b := gitIn(t, source, "branch", "--show-current"); b != "main" {
		t.Errorf("Refresh moved the user to %q", b)
	}
	if !strings.Contains(msg, "checkout develop") {
		t.Errorf("msg = %q, want the command to run", msg)
	}
}

// Diverged means a human has to reconcile; guessing would be a merge nobody
// asked for.
func TestRefreshRefusesWhenDiverged(t *testing.T) {
	source, origin := adopted(t)

	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", "-b", "develop", origin, other)
	if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", ".")
	gitIn(t, other, "commit", "-q", "-m", "theirs")
	gitIn(t, other, "push", "-q", "origin", "develop")

	if err := os.WriteFile(filepath.Join(source, "mine.txt"), []byte("m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, source, "add", ".")
	gitIn(t, source, "commit", "-q", "-m", "mine")
	head := gitIn(t, source, "rev-parse", "HEAD")

	msg, err := Refresh(source, "develop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "diverged") {
		t.Errorf("msg = %q", msg)
	}
	if got := gitIn(t, source, "rev-parse", "HEAD"); got != head {
		t.Error("Refresh rewrote history on a diverged branch")
	}
}

func TestRemoteOfExplainsWhenThereIsNoOrigin(t *testing.T) {
	d := t.TempDir()
	gitIn(t, filepath.Dir(d), "init", "-q", d)
	_, err := RemoteOf(d)
	if err == nil || !strings.Contains(err.Error(), "clones from the remote") {
		t.Errorf("error should explain why a remote is needed, got: %v", err)
	}
}
