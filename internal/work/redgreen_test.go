package work

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeExec writes an executable file under root, creating parent
// directories as needed -- scripts/test.sh must carry its execute bit or
// runSuite's direct invocation fails before it ever reaches the logic under
// test.
func writeExec(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The whole point of OR-156: a test that already passes without this
// ticket's change proves nothing about it, and the only way to know is to
// watch it fail first. This is the mechanized version of that -- one test
// file that genuinely needs the change (proven red at the pre-change
// commit) and one that does not (unproven), partitioned without ever
// touching the branch under test.
func TestCheckRedBeforeGreenPartitionsProvenAndUnprovenTests(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")

	// sound_test.go only passes once feature.flag exists; weak_test.go
	// passes unconditionally, i.e. it does not actually exercise the change.
	writeExec(t, repo, "scripts/test.sh", "#!/bin/sh\n"+
		"set -e\n"+
		"if [ -f sound_test.go ] && [ ! -f feature.flag ]; then\n"+
		"  echo \"the feature is not implemented yet\" >&2\n"+
		"  exit 1\n"+
		"fi\n"+
		"exit 0\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "seed")
	baseSHA := git(t, repo, "rev-parse", "HEAD")

	// The implementer's change: a feature, nothing test-shaped.
	writeExec(t, repo, "feature.flag", "ENABLED\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "feat: add the feature")
	preQA := git(t, repo, "rev-parse", "HEAD")

	// QA's own commit: one test that needs the feature, one that does not.
	writeExec(t, repo, "sound_test.go", "package fake\n")
	writeExec(t, repo, "weak_test.go", "package fake\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "test: qa")

	res := checkRedBeforeGreen(repo, baseSHA, preQA)

	if res.Skipped != "" {
		t.Fatalf("skipped: %s", res.Skipped)
	}
	if len(res.Unclear) != 0 {
		t.Fatalf("unclear: %+v", res.Unclear)
	}
	if got := res.Proven; len(got) != 1 || got[0] != "sound_test.go" {
		t.Errorf("proven = %v, want [sound_test.go]", got)
	}
	if got := res.Unproven; len(got) != 1 || got[0] != "weak_test.go" {
		t.Errorf("unproven = %v, want [weak_test.go]", got)
	}

	// Checking must not disturb the branch: no worktree left registered,
	// and HEAD is still QA's commit, not the pre-change one.
	if out := git(t, repo, "worktree", "list"); strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("left a worktree behind:\n%s", out)
	}
	if head := git(t, repo, "rev-parse", "HEAD"); head == baseSHA {
		t.Errorf("HEAD moved to the pre-change commit")
	}
}

// Every way the check has nothing to prove, or nothing to prove it with,
// degrades to a stated reason rather than failing the stage: QA does not
// block on its own authority, and this check runs with the same authority.
func TestCheckRedBeforeGreenSkips(t *testing.T) {
	newRepo := func(t *testing.T, withScript bool) (dir, sha string) {
		t.Helper()
		dir = t.TempDir()
		git(t, dir, "init", "-q", "-b", "main")
		if withScript {
			writeExec(t, dir, "scripts/test.sh", "#!/bin/sh\nexit 0\n")
		} else {
			writeExec(t, dir, "README.md", "x\n")
		}
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "seed")
		return dir, git(t, dir, "rev-parse", "HEAD")
	}

	t.Run("no base commit", func(t *testing.T) {
		dir, sha := newRepo(t, true)
		if res := checkRedBeforeGreen(dir, "", sha); res.Skipped == "" {
			t.Error("did not skip without a base commit")
		}
	})

	t.Run("no scripts/test.sh", func(t *testing.T) {
		dir, sha := newRepo(t, false)
		if res := checkRedBeforeGreen(dir, sha, sha); res.Skipped == "" {
			t.Error("did not skip without scripts/test.sh")
		}
	})

	t.Run("QA touched no test file", func(t *testing.T) {
		dir, sha := newRepo(t, true)
		writeExec(t, dir, "notes.md", "unrelated\n")
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "unrelated")
		if res := checkRedBeforeGreen(dir, sha, sha); res.Skipped == "" {
			t.Error("did not skip when nothing test-shaped changed")
		}
	})
}

func TestIsTestFile(t *testing.T) {
	cases := map[string]bool{
		"internal/work/qa_test.go": true,
		"internal/work/qa.go":      false,
		"tests/test_login.py":      true,
		"tests/login_test.py":      true,
		"app/login.py":             false,
		"src/__tests__/login.js":   true,
		"src/login.test.ts":        true,
		"src/login.spec.tsx":       true,
		"src/login.ts":             false,
		"README.md":                false,
	}
	for path, want := range cases {
		if got := isTestFile(path); got != want {
			t.Errorf("isTestFile(%q) = %v, want %v", path, got, want)
		}
	}
}
