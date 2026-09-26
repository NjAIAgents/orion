package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// scaffoldedWS is the state OR-482 was found in: develop and main pushed to a
// real (bare) origin, and the scaffold committed on an unpushed orion/ branch
// that the sandbox is still on.
func scaffoldedWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	w := chainWS(t)
	repo := w.RepoDir()
	origin := filepath.Join(t.TempDir(), "origin.git")
	mustGit(t, t.TempDir(), "init", "-q", "--bare", origin)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-q", "-b", "main")
	mustGit(t, repo, "config", "user.email", "t@example.com")
	mustGit(t, repo, "config", "user.name", "t")
	mustGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	mustGit(t, repo, "checkout", "-q", "-b", "develop")
	mustGit(t, repo, "commit", "-q", "--allow-empty", "-m", "docs: plan")
	mustGit(t, repo, "remote", "add", "origin", origin)
	mustGit(t, repo, "push", "-q", "-u", "origin", "main", "develop")
	mustGit(t, repo, "checkout", "-q", "-b", "orion/001-thing")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "README.md")
	mustGit(t, repo, "commit", "-q", "-m", "chore: scaffold the repository")
	w.Task.Remote = origin
	return w
}

// stubPRs replaces the forge calls; the push and the branch switch stay real.
func stubPRs(t *testing.T, existing collect.Verdict) *[]string {
	t.Helper()
	var opened []string
	oldOpen, oldStatus := pubOpenPRFn, pubPRStatusFn
	pubOpenPRFn = func(_, branch, title, _, base string) (string, error) {
		opened = append(opened, branch+"->"+base+" "+title)
		return "https://example.test/pr/1", nil
	}
	pubPRStatusFn = func(_, _ string) (collect.PR, error) { return collect.PR{Verdict: existing}, nil }
	t.Cleanup(func() { pubOpenPRFn, pubPRStatusFn = oldOpen, oldStatus })
	return &opened
}

func TestScaffoldPublishPushesOpensPRAndReturnsToDevelop(t *testing.T) {
	w := scaffoldedWS(t)
	opened := stubPRs(t, collect.VerdictUnknown)
	if scaffoldPublishDone(w) {
		t.Fatal("Done before the scaffold branch was published")
	}
	var out bytes.Buffer
	if err := scaffoldPublishStep(&stepIO{Out: &out}, w); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if ls, _ := gitIn(w.RepoDir(), "ls-remote", "--heads", "origin", "orion/001-thing"); !strings.Contains(ls, "orion/001-thing") {
		t.Error("the scaffold branch was not pushed")
	}
	if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], "orion/001-thing->develop chore: scaffold") {
		t.Errorf("want one PR into develop titled from the scaffold commit, got %v", *opened)
	}
	if cur, _ := currentBranch(w.RepoDir()); cur != "develop" {
		t.Errorf("sandbox left on %q, want develop", cur)
	}
	if !scaffoldPublishDone(w) {
		t.Error("Done is false after publishing")
	}
}

// A resumed chain must not open a second pull request.
func TestScaffoldPublishLeavesAnOpenPRAlone(t *testing.T) {
	w := scaffoldedWS(t)
	opened := stubPRs(t, collect.VerdictPending)
	var out bytes.Buffer
	if err := scaffoldPublishStep(&stepIO{Out: &out}, w); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(*opened) != 0 {
		t.Errorf("opened a second pull request: %v", *opened)
	}
}

// The case the old warning led people into: sandbox put back on develop by
// hand, scaffold still only local. That is not done.
func TestScaffoldPublishNotDoneWhenSomeoneOnlyCheckedOutDevelop(t *testing.T) {
	w := scaffoldedWS(t)
	mustGit(t, w.RepoDir(), "checkout", "-q", "develop")
	if scaffoldPublishDone(w) {
		t.Error("Done while the scaffold exists only on a local branch")
	}
}

func TestScaffoldPublishWithoutARemoteIsDegraded(t *testing.T) {
	w := scaffoldedWS(t)
	w.Task.Remote = ""
	stubPRs(t, collect.VerdictUnknown)
	err := scaffoldPublishStep(&stepIO{Out: &bytes.Buffer{}}, w)
	if _, ok := err.(*Degraded); !ok {
		t.Errorf("want Degraded with no remote, got %v", err)
	}
}
