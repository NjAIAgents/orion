package toolkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OR-295 renamed internal/njagents to internal/toolkit with no behaviour
// change. These pin the mechanics of that move: the old directory is gone,
// the new one is in place with every file re-labelled `package toolkit`,
// the renamed test file landed under its new name, and the module still
// builds end to end.

func repoRootForOR295(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod) above internal/toolkit")
		}
		dir = parent
	}
}

func TestOR295ToolkitDirectoryExistsWithExpectedFiles(t *testing.T) {
	root := repoRootForOR295(t)
	dir := filepath.Join(root, "internal", "toolkit")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("internal/toolkit must exist as a directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "toolkit.go")); err != nil {
		t.Errorf("internal/toolkit/toolkit.go missing: %v", err)
	}
}

func TestOR295NjagentsDirectoryNoLongerExists(t *testing.T) {
	root := repoRootForOR295(t)
	if _, err := os.Stat(filepath.Join(root, "internal", "njagents")); !os.IsNotExist(err) {
		t.Errorf("internal/njagents must not exist after the rename, stat err = %v", err)
	}
}

func TestOR295ChecktoolkitTestFileRenamed(t *testing.T) {
	root := repoRootForOR295(t)
	doctorDir := filepath.Join(root, "internal", "doctor")
	if _, err := os.Stat(filepath.Join(doctorDir, "checktoolkit_cases_test.go")); err != nil {
		t.Errorf("internal/doctor/checktoolkit_cases_test.go missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(doctorDir, "checknjagents_cases_test.go")); !os.IsNotExist(err) {
		t.Errorf("internal/doctor/checknjagents_cases_test.go must not exist after the rename, stat err = %v", err)
	}
}

func TestOR295AllToolkitSourceFilesDeclarePackageToolkit(t *testing.T) {
	root := repoRootForOR295(t)
	dir := filepath.Join(root, "internal", "toolkit")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		found++
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// The package CLAUSE, not the first line. A file may open with its
		// package doc comment -- toolkit.go does, and this ticket's own
		// acceptance criteria require that doc comment -- so asserting on
		// the first line failed the very shape the rename was asked for.
		if !packageClauseIs(string(src), "toolkit") {
			t.Errorf("%s does not declare `package toolkit`", e.Name())
		}
	}
	if found == 0 {
		t.Fatal("no .go files found in internal/toolkit")
	}
}

func TestOR295GoBuildAllPackagesSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full module build in -short mode")
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = repoRootForOR295(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./... failed: %v\n%s", err, out)
	}
}

// packageClauseIs reports whether the file's package clause names want,
// ignoring any leading comments and blank lines.
func packageClauseIs(src, want string) bool {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") ||
			strings.HasPrefix(line, "*") {
			continue
		}
		return strings.HasPrefix(line, "package "+want)
	}
	return false
}
