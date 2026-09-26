package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// static/index.html has to be IN the repository, not just present on the
// machine that wrote it: a fresh clone gets whatever git tracked, nothing
// more. `git ls-files` only lists what the index actually holds, so this
// fails if the placeholder were ever added to .gitignore or left staged
// without being committed.
func TestIndexHTMLIsTrackedByGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	out, err := exec.Command("git", "ls-files", "--error-unmatch", "static/index.html").CombinedOutput()
	if err != nil {
		t.Fatalf("static/index.html is not tracked by git, so a fresh clone will not "+
			"have it: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// "Tracked" (the index) and "committed" (HEAD) are not the same thing -- a
// file can be staged for deletion and still pass `git ls-files`. Reading it
// out of HEAD is the check that actually matches what a fresh clone gets,
// and is the guard against the placeholder being removed as premature
// cleanup before OR-51 lands: it must stay in history until that commit
// explicitly deletes it.
func TestPlaceholderPersistsInHEADUntilExplicitlyDeleted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	out, err := exec.Command("git", "cat-file", "-e", "HEAD:internal/web/static/index.html").CombinedOutput()
	if err != nil {
		t.Fatalf("internal/web/static/index.html is missing from HEAD -- the placeholder "+
			"must stay committed until OR-51's build replaces it: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// buildStandaloneAssetsPackage copies assets.go into a throwaway module with
// the given static/ contents and runs `go build`, returning its combined
// output and error. assets.go imports nothing outside the standard library,
// so it builds on its own without the rest of this repo.
func buildStandaloneAssetsPackage(t *testing.T, populateStatic func(staticDir string) error) ([]byte, error) {
	t.Helper()

	src, err := os.ReadFile("assets.go")
	if err != nil {
		t.Fatalf("reading assets.go: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module assetscheck\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets.go"), src, 0o644); err != nil {
		t.Fatalf("writing assets.go: %v", err)
	}
	staticDir := filepath.Join(dir, "static")
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatalf("creating static/: %v", err)
	}
	if err := populateStatic(staticDir); err != nil {
		t.Fatalf("populating static/: %v", err)
	}

	cmd := exec.Command("go", "build", ".")
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// //go:embed is a compile-time pattern: a pattern matching nothing is a
// build error, not a runtime one. Deleting static/index.html and leaving the
// directory otherwise as this repo has it -- nothing else in static/ -- must
// fail `go build`, because that is the exact scenario the committed
// placeholder exists to prevent.
func TestDeletingIndexHTMLFailsTheBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	out, err := buildStandaloneAssetsPackage(t, func(staticDir string) error {
		// static/ exists but nothing is written into it: this is what
		// `rm static/index.html` leaves behind, since index.html is the
		// only tracked file in that directory.
		return nil
	})
	if err == nil {
		t.Fatalf("go build succeeded with static/index.html missing, want a build error; output:\n%s", out)
	}
	if !strings.Contains(string(out), "static") {
		t.Errorf("go build failed for an unexpected reason (want a //go:embed static/ mismatch): %s", out)
	}
}

// Distinct from the file being deleted: static/ can be empty because nothing
// was ever written into it -- a directory that exists (so it survives
// `.gitignore`/checkout) but holds no index.html. //go:embed static treats
// that identically to a missing file: no match, build error.
func TestEmptyStaticDirectoryFailsTheBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	out, err := buildStandaloneAssetsPackage(t, func(staticDir string) error {
		entries, err := os.ReadDir(staticDir)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			t.Fatalf("static/ was not empty before the build: %v", entries)
		}
		return nil
	})
	if err == nil {
		t.Fatalf("go build succeeded with an empty static/, want a build error; output:\n%s", out)
	}
	if !strings.Contains(string(out), "static") {
		t.Errorf("go build failed for an unexpected reason (want a //go:embed static/ mismatch): %s", out)
	}
}
