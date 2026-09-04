package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runDecompose exits the process on every error path (a missing arg, a
// file it cannot read, Jira not configured) rather than returning one, so
// those paths can only be observed from outside the test binary: run this
// same test binary again with a marker env var, and let it call
// runDecompose for real. This is the same technique Go's own os/exec tests
// use for testing exit behaviour.
const decomposeHelperEnv = "ORION_DECOMPOSE_TEST_HELPER_ARGS"

// decomposeHelperArgSep joins packed args in decomposeHelperEnv. An env var
// VALUE may not contain a NUL byte -- exec rejects it with "environment
// variable contains NUL" before the child even starts -- so this uses a
// control byte that is not NUL instead.
const decomposeHelperArgSep = "\x1f"

// decomposeHelperNoArgs marks "no args at all" distinctly from "unset",
// since os.Getenv cannot tell an empty value from a missing one.
const decomposeHelperNoArgs = "\x1e"

// TestHelperProcess is not a real test; it is the re-exec entry point.
// It runs iff decomposeHelperEnv is set, and calls runDecompose with the
// args packed into it so the parent can observe stdout/stderr/exit code.
func TestHelperProcess(t *testing.T) {
	raw := os.Getenv(decomposeHelperEnv)
	if raw == "" {
		return
	}
	var args []string
	if raw != decomposeHelperNoArgs {
		args = strings.Split(raw, decomposeHelperArgSep)
	}
	runDecompose(args)
}

// runDecomposeSubprocess re-execs this test binary with runDecompose(args)
// as its whole job, in a fresh working directory and environment so the
// developer's own ORION_HOME / ORION_JIRA_* never leak in.
func runDecomposeSubprocess(t *testing.T, dir string, args []string) (stdout, stderr string, exitCode int) {
	t.Helper()
	packed := decomposeHelperNoArgs
	if len(args) > 0 {
		packed = strings.Join(args, decomposeHelperArgSep)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Dir = dir
	cmd.Env = []string{
		decomposeHelperEnv + "=" + packed,
		"HOME=" + dir,
		"ORION_HOME=" + filepath.Join(dir, ".orion"),
		"PATH=" + os.Getenv("PATH"),
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running the helper process: %v", err)
	}
	return outBuf.String(), errBuf.String(), code
}

// A destination project is not optional: with none named, this is a usage
// error, not an attempt to guess one.
func TestRunDecomposeWithNoProjectKeyIsAUsageError(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runDecomposeSubprocess(t, dir, nil)
	if code != 64 {
		t.Errorf("exit code = %d, want 64 (usage error)", code)
	}
	if !strings.Contains(stderr, "orion decompose <KEY>") {
		t.Errorf("stderr should show the command's own usage, got: %q", stderr)
	}
}

// A path that does not exist is reported plainly rather than as a panic or
// a bare "no such file" with no indication which command tripped over it.
func TestRunDecomposeReportsAMissingFileClearly(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "specs", "001-x", "tasks.md")
	_, stderr, code := runDecomposeSubprocess(t, dir, []string{"CAT", missing})
	if code == 0 {
		t.Fatal("want a non-zero exit for a task list that does not exist")
	}
	if !strings.Contains(stderr, missing) {
		t.Errorf("stderr should name the path it could not read, got: %q", stderr)
	}
}

// os.ReadFile on a directory fails deterministically on every OS this runs
// on, with no dependence on the invoking user's permissions -- unlike a
// chmod-0000 file, which root ignores. It stands in for "the path exists
// but cannot be read as the file this command needs".
func TestRunDecomposeReportsAnUnreadablePathClearly(t *testing.T) {
	dir := t.TempDir()
	notAFile := filepath.Join(dir, "specs", "001-x")
	if err := os.MkdirAll(notAFile, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runDecomposeSubprocess(t, dir, []string{"CAT", notAFile})
	if code == 0 {
		t.Fatal("want a non-zero exit when the named path is not a readable file")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("want an error message rather than a silent non-zero exit")
	}
}

// With an explicit path given, that path is used as-is -- findTasks's
// specs/*/tasks.md search never runs, so a repo with no specs/ directory
// (or several tasks.md files, which would otherwise be ambiguous) is not a
// problem when the file is named directly. Reaching Jira-not-configured
// rather than "no specs/*/tasks.md here" is what proves the named path,
// and not the search, was used.
func TestRunDecomposeWithAnExplicitPathSkipsTheSpecsSearch(t *testing.T) {
	dir := t.TempDir()
	tasks := filepath.Join(dir, "elsewhere", "tasks.md")
	if err := os.MkdirAll(filepath.Dir(tasks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tasks, []byte("# Tasks: X\n\n- [ ] T001 Do it in a.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runDecomposeSubprocess(t, dir, []string{"CAT", tasks})
	if code == 0 {
		t.Fatal("want a non-zero exit: Jira is not configured in this environment")
	}
	if strings.Contains(stderr, "no specs/*/tasks.md") || strings.Contains(stderr, "task lists here") {
		t.Errorf("the explicit path must bypass findTasks entirely, got: %q", stderr)
	}
	if !strings.Contains(stderr, "Jira is not configured") {
		t.Errorf("want the run to reach the Jira step, proving the named file was read and "+
			"parsed rather than the search running instead; stdout=%q stderr=%q", stdout, stderr)
	}
}

// ORION_JIRA_URL / ORION_JIRA_EMAIL / ORION_JIRA_TOKEN are how this command
// authenticates; with none set, the error names exactly those variables
// rather than failing some other, less actionable way.
func TestRunDecomposeReadsJiraCredentialsFromTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	tasks := writeTasks(t, dir, "001-alpha")

	t.Run("none set", func(t *testing.T) {
		_, stderr, code := runDecomposeSubprocess(t, dir, []string{"CAT", tasks})
		if code == 0 {
			t.Fatal("want a non-zero exit with no Jira credentials in the environment")
		}
		for _, want := range []string{"ORION_JIRA_URL", "ORION_JIRA_EMAIL", "ORION_JIRA_TOKEN"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr should name the missing %s, got: %q", want, stderr)
			}
		}
	})

	t.Run("partially set", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
		cmd.Dir = dir
		cmd.Env = []string{
			decomposeHelperEnv + "=" + strings.Join([]string{"CAT", tasks}, decomposeHelperArgSep),
			"HOME=" + dir,
			"ORION_HOME=" + filepath.Join(dir, ".orion"),
			"PATH=" + os.Getenv("PATH"),
			"ORION_JIRA_URL=https://example.atlassian.net",
			"ORION_JIRA_EMAIL=orion@example.com",
		}
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		err := cmd.Run()
		if err == nil {
			t.Fatal("want a non-zero exit: ORION_JIRA_TOKEN is still unset")
		}
		stderr := errBuf.String()
		if strings.Contains(stderr, "ORION_JIRA_URL") || strings.Contains(stderr, "ORION_JIRA_EMAIL") {
			t.Errorf("URL and email were set in the environment and must not be reported "+
				"missing, got: %q", stderr)
		}
		if !strings.Contains(stderr, "ORION_JIRA_TOKEN") {
			t.Errorf("only the token is missing; it should be named, got: %q", stderr)
		}
	})
}
