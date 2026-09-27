package ciscaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// OR-541: a worktree shares the main tree's .venv, where the project is
// installed editable from the MAIN tree. The generated script must make the
// worktree import its OWN src/, not that copy.
//
// Runs the script's own import-path block against two trees whose package
// says which tree it came from, with the other tree already on PYTHONPATH --
// the position an editable install puts it in.
func TestAWorktreeImportsItsOwnSourceNotTheInstalledCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash script")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	script := scriptFor(StackPython)
	start := strings.Index(script, "if [ -d src ]; then")
	end := strings.Index(script[start:], "fi\n")
	if start < 0 || end < 0 {
		t.Fatal("the Python script puts no src/ on the import path")
	}
	block := script[start : start+end+len("fi\n")]

	tree := func(who string) string {
		d := t.TempDir()
		pkg := filepath.Join(d, "src", "pkg")
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "__init__.py"),
			[]byte("WHO = \""+who+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	main, worktree := tree("main"), tree("worktree")

	cmd := exec.Command("bash", "-c", block+`python3 -c "import pkg; print(pkg.WHO)"`)
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(main, "src"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "worktree" {
		t.Errorf("the worktree imported the %s tree's code", got)
	}
}
