package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/changelog"
	"github.com/orion-sdlc/orion/internal/events"
)

// collateChangelogRepo builds a bare origin plus a work clone on
// workBranch, with a seeded CHANGELOG.md -- the shape collateChangelog
// expects to find at root, mirroring what shipProduction hands it.
func collateChangelogRepo(t *testing.T, workBranch string) (repo string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo = filepath.Join(root, "repo")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "-q", "--bare", "-b", "main", origin)
	git(root, "clone", "-q", origin, repo)
	if err := os.WriteFile(filepath.Join(repo, "CHANGELOG.md"),
		[]byte("# Changelog\n\n## Unreleased\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".")
	git(repo, "commit", "-q", "-m", "seed")
	git(repo, "checkout", "-q", "-b", workBranch)
	git(repo, "push", "-q", "-u", "origin", workBranch)
	return repo
}

func writeFragment(t *testing.T, repo, key, body string) {
	t.Helper()
	dir := filepath.Join(repo, changelog.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changelog.Path(repo, key), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testLog(t *testing.T) *events.Log {
	t.Helper()
	log, err := events.Open(filepath.Join(t.TempDir(), "events.jsonl"), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// THE CASE OR-422 EXISTS TO FIX: fragments sitting in .changelog.d/ at ship
// time get collated into CHANGELOG.md and pushed to the work branch, so the
// release carries a changelog section instead of shipping silent.
func TestCollateChangelogCollatesAndPushesPendingFragments(t *testing.T) {
	repo := collateChangelogRepo(t, "develop")
	writeFragment(t, repo, "OR-1", "### Fixed\n\n- Fixed the thing.\n")

	var buf bytes.Buffer
	collateChangelog(repo, "develop", "v1.0.0", testLog(t), &buf)

	body, err := os.ReadFile(filepath.Join(repo, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "## v1.0.0") {
		t.Errorf("CHANGELOG.md missing the v1.0.0 section:\n%s", body)
	}
	if !strings.Contains(string(body), "Fixed the thing") {
		t.Errorf("CHANGELOG.md missing the fragment's content:\n%s", body)
	}
	if _, err := os.Stat(changelog.Path(repo, "OR-1")); !os.IsNotExist(err) {
		t.Error("the collated fragment should have been removed")
	}

	// Pushed, not merely committed locally -- shipProduction opens the
	// promotion PR from origin/workBranch, so a local-only commit would
	// never reach it.
	out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%s",
		"origin/develop").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); !strings.HasPrefix(got, "docs(changelog): collate") {
		t.Errorf("origin/develop's tip = %q, want a collate commit", got)
	}
}

// The common case: nothing pending. Must be a silent no-op, not a failure --
// a release with no changelog-worthy fragments is not a broken release.
func TestCollateChangelogIsANoOpWithNoFragments(t *testing.T) {
	repo := collateChangelogRepo(t, "develop")

	before, err := exec.Command("git", "-C", repo, "rev-parse", "origin/develop").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	collateChangelog(repo, "develop", "v1.0.0", testLog(t), &buf)

	after, err := exec.Command("git", "-C", repo, "rev-parse", "origin/develop").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("origin/develop moved with no fragments to collate")
	}
}

// A version already collated (a resumed ship after an earlier partial
// failure) must warn and continue rather than crash the whole command --
// collation is best-effort, never a gate on the release itself.
func TestCollateChangelogWarnsRatherThanFailsOnAnAlreadyCollatedVersion(t *testing.T) {
	repo := collateChangelogRepo(t, "develop")
	if err := os.WriteFile(filepath.Join(repo, "CHANGELOG.md"),
		[]byte("# Changelog\n\n## Unreleased\n\n## v1.0.0\n\nalready here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", repo, "add", ".").Run()
	exec.Command("git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "-q", "-m", "seed v1.0.0").Run()
	exec.Command("git", "-C", repo, "push", "-q", "origin", "develop").Run()
	writeFragment(t, repo, "OR-1", "### Fixed\n\n- Fixed the thing.\n")

	var buf bytes.Buffer
	// Must not panic or os.Exit; a plain call returning is the assertion.
	collateChangelog(repo, "develop", "v1.0.0", testLog(t), &buf)

	if !strings.Contains(buf.String(), "already has a v1.0.0 section") {
		t.Errorf("expected a warning about the existing section, got: %s", buf.String())
	}
	// The fragment must survive: Collate refused before touching it.
	if _, err := os.Stat(changelog.Path(repo, "OR-1")); err != nil {
		t.Errorf("the fragment should still exist after a refused collation: %v", err)
	}
}
