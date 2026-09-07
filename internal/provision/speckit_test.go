package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/fakebin"
)

// fakeSpecify puts a `specify` on PATH that records its arguments and, on
// init, creates the directories the real one would.
func fakeSpecify(t *testing.T) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls.log")
	dir := t.TempDir()
	fakebin.Install(t, dir, "specify", "#!/bin/sh\n"+
		"echo \"$@\" >> "+fakebin.ShPath(log)+"\n"+
		"case \"$1\" in init) mkdir -p .specify/memory .claude/skills/speckit-specify; echo x > .claude/skills/speckit-specify/SKILL.md;; esac\n"+
		"exit 0\n")
	return log
}

func TestInitSpecKitRunsOnceNonInteractivelyAndCommits(t *testing.T) {
	log := fakeSpecify(t)
	repo := repoAt(t, true)

	did, err := InitSpecKit(repo)
	if err != nil || !did {
		t.Fatalf("first InitSpecKit: did=%v err=%v", did, err)
	}
	calls, _ := os.ReadFile(log)
	got := strings.TrimSpace(string(calls))
	for _, want := range []string{"init", "--here", "--force", "--non-interactive", "--integration claude"} {
		if !strings.Contains(got, want) {
			t.Errorf("specify was called without %q: %q", want, got)
		}
	}
	// What it wrote is committed: the next stage reads the repository.
	if out, err := git(repo, "ls-files", "--error-unmatch", ".claude/skills/speckit-specify/SKILL.md"); err != nil {
		t.Errorf("the installed skill is not tracked: %s", out)
	}
	if out, _ := git(repo, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Errorf("the tree is dirty after InitSpecKit:\n%s", out)
	}

	// Second call: already initialised, nothing invoked.
	did, err = InitSpecKit(repo)
	if err != nil || did {
		t.Fatalf("second InitSpecKit: did=%v err=%v; must be a no-op", did, err)
	}
	if calls2, _ := os.ReadFile(log); string(calls2) != string(calls) {
		t.Errorf("specify was invoked again on an initialised project:\n%s", calls2)
	}
}

// Without the CLI the error names the install command, not a bare exec
// failure -- the fix has to be on screen.
func TestInitSpecKitNamesTheInstallCommandWhenTheCLIIsAbsent(t *testing.T) {
	repo := repoAt(t, true)
	t.Setenv("PATH", t.TempDir()) // nothing on it -- after git has made the repo
	_, err := InitSpecKit(repo)
	if err == nil || !strings.Contains(err.Error(), SpecKitInstall) {
		t.Errorf("error does not name the install command: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, SpecKitDir)); statErr == nil {
		t.Error("something created .specify/ without the CLI")
	}
}

// TestMain lets fakebin's Windows path work: a fake binary there is a copy
// of this test binary that runs its sidecar script.
func TestMain(m *testing.M) {
	fakebin.Main()
	os.Exit(m.Run())
}
