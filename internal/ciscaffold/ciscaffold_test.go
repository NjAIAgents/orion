package ciscaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T, files ...string) string {
	t.Helper()
	d := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(d, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestDetectPicksTheToolchainFromMarkerFiles(t *testing.T) {
	for _, tc := range []struct {
		files []string
		want  Stack
	}{
		{[]string{"go.mod"}, StackGo},
		{[]string{"pyproject.toml"}, StackPython},
		{[]string{"requirements.txt"}, StackPython},
		{[]string{"package.json"}, StackNode},
		{nil, StackUnknown},
		// A Go repository with package.json for tooling is still a Go
		// repository; running npm test in it would prove nothing.
		{[]string{"go.mod", "package.json"}, StackGo},
	} {
		if got := Detect(repo(t, tc.files...)); got != tc.want {
			t.Errorf("Detect(%v) = %s, want %s", tc.files, got, tc.want)
		}
	}
}

// The generated script must actually run. A template with a shell syntax
// error is invisible until CI, where it surfaces as a failing check on a
// branch that is fine.
func TestGeneratedScriptsAreValidShell(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for _, s := range []Stack{StackGo, StackPython, StackNode, StackUnknown} {
		dir := t.TempDir()
		p := filepath.Join(dir, "test.sh")
		if err := os.WriteFile(p, []byte(scriptFor(s)), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
			t.Errorf("%s script is not valid shell: %v\n%s", s, err, out)
		}
	}
}

// An unrecognised toolchain gets a script that FAILS. A stub exiting 0 would
// be worse than no script: CI would report green having run nothing, and
// every pull request would carry a check proving only that the check exists.
func TestAnUnknownToolchainProducesAFailingStubNotAPassingOne(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := repo(t)
	res, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsAttention(res) {
		t.Fatal("an unknown toolchain must be flagged for a human to finish")
	}
	cmd := exec.Command("bash", filepath.Join(dir, "scripts", "test.sh"))
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatal("the stub exited 0; CI would be green having run nothing")
	}
}

// Nothing is overwritten. A repository that already has a test script has
// one for reasons this package cannot see.
func TestAnExistingScriptIsNeverOverwritten(t *testing.T) {
	dir := repo(t, "go.mod")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "#!/bin/sh\necho mine\n"
	p := filepath.Join(dir, "scripts", "test.sh")
	if err := os.WriteFile(p, []byte(mine), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.ScriptCreated {
		t.Error("reported creating a script that already existed")
	}
	b, _ := os.ReadFile(p)
	if string(b) != mine {
		t.Fatal("an existing test script was overwritten")
	}
}

// A second workflow running the same suite doubles the CI bill and makes the
// checks list ambiguous about which verdict Orion should act on.
func TestAnExistingWorkflowStopsASecondFromBeingAdded(t *testing.T) {
	dir := repo(t, "go.mod")
	flows := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(flows, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flows, "existing.yml"), []byte("name: theirs"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.FlowCreated {
		t.Fatal("added a competing workflow")
	}
	if _, err := os.Stat(filepath.Join(flows, "orion-ci.yml")); err == nil {
		t.Fatal("orion-ci.yml was written despite an existing workflow")
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "scripts/test.sh") {
		t.Errorf("the note must say what to do instead: %v", res.Notes)
	}
}

// The whole contract is that CI runs the SAME script a person runs. A
// workflow that inlines its own commands can drift from the script, and the
// drift shows up as a pull request green in CI and broken locally.
func TestTheWorkflowRunsTheScriptRatherThanItsOwnCommands(t *testing.T) {
	for _, s := range []Stack{StackGo, StackPython, StackNode} {
		flow := workflowFor(s)
		if !strings.Contains(flow, "./scripts/test.sh") {
			t.Errorf("%s workflow does not call scripts/test.sh", s)
		}
		// Orion reads the checks list to decide whether a branch may merge,
		// so the job's name is load-bearing, not decoration.
		if !strings.Contains(flow, "name: test") {
			t.Errorf("%s workflow has no stable job name for Orion to read", s)
		}
		if !strings.Contains(flow, "cancel-in-progress") {
			t.Errorf("%s workflow lacks concurrency control; a force-push mid-review "+
				"would leave an older run reporting on code that is gone", s)
		}
	}
}

func TestTheScriptIsExecutable(t *testing.T) {
	dir := repo(t, "go.mod")
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "scripts", "test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatal("scripts/test.sh is not executable; the workflow calls it directly")
	}
}
