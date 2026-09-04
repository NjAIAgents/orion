package suite

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "scripts", "test.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestARepositoryScriptWins: scripts/test.sh is the repository's own
// statement of how its tests run, and second-guessing it would fight the
// choice it was written to express.
func TestARepositoryScriptWins(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "exit 0\n")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	argv, err := Detect(dir, 4)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	// On Windows a .sh file is not an executable, so shellScript prepends
	// bash (OR-334) and argv is [bash, <path>]. The property under test is
	// which SCRIPT was detected, not how the OS is made to run it.
	if !strings.HasSuffix(argv[len(argv)-1], "test.sh") {
		t.Errorf("expected the repository's own script, got %v", argv)
	}
	if runtime.GOOS != "windows" && len(argv) != 1 {
		t.Errorf("the script needs no interpreter here, got %v", argv)
	}
}

// TestConcurrencyIsPassedToTheRunner. The whole argument for a process rather
// than a fan of agents is that the toolchain already parallelises; if the
// number never reaches it, the feature is a config field that does nothing.
func TestConcurrencyIsPassedToTheRunner(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	argv, err := Detect(dir, 7)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-p 7") {
		t.Errorf("the concurrency never reached the runner: %q", joined)
	}
}

// TestAnUnknownStackIsNotGuessedAt. Returning some plausible command for a
// repository this package does not understand would produce a verdict nobody
// should trust; ErrNotFound keeps the delegated path.
func TestAnUnknownStackIsNotGuessedAt(t *testing.T) {
	if _, err := Detect(t.TempDir(), 4); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unrecognised repository, got %v", err)
	}
}

// TestAPassingSuitePasses, and TestAFailingSuiteFails: the exit code is the
// verdict, and nothing else is.
func TestAPassingSuitePasses(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "echo all good\nexit 0\n")
	argv, _ := Detect(dir, 0)

	res := Run(dir, argv, 30*time.Second)
	if !res.Passed || res.Err != nil || res.TimedOut {
		t.Errorf("expected a clean pass, got %+v", res)
	}
	if !strings.Contains(res.Output, "all good") {
		t.Errorf("output was not captured: %q", res.Output)
	}
}

func TestAFailingSuiteFails(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "echo FAIL TestThing\nexit 1\n")
	argv, _ := Detect(dir, 0)

	res := Run(dir, argv, 30*time.Second)
	if res.Passed {
		t.Error("a suite that exited 1 was reported as passing")
	}
	// A non-zero exit is a VERDICT, not an error. Reporting it as Err would
	// make a real failure indistinguishable from a runner that would not start.
	if res.Err != nil {
		t.Errorf("a failing suite set Err, so a real failure reads as an unknown verdict: %v", res.Err)
	}
	if !strings.Contains(res.Output, "FAIL TestThing") {
		t.Errorf("the failure output was lost: %q", res.Output)
	}
}

// TestAHungSuiteIsKilledAndSaysSo. A timeout and a failure call for different
// responses, so they must not report the same way.
func TestAHungSuiteIsKilledAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "sleep 60\n")
	argv, _ := Detect(dir, 0)

	start := time.Now()
	res := Run(dir, argv, 300*time.Millisecond)

	if !res.TimedOut {
		t.Error("a hung suite was not reported as timed out")
	}
	if res.Passed {
		t.Error("a hung suite was reported as passing")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("Run waited for the child rather than its own deadline: %s", elapsed)
	}
}

// TestACommandThatCannotRunIsNotAVerdict. "The runner is missing" and "the
// tests failed" are different facts, and conflating them turns a broken
// environment into a code defect nobody can find.
func TestACommandThatCannotRunIsNotAVerdict(t *testing.T) {
	res := Run(t.TempDir(), []string{"orion-no-such-binary-anywhere"}, 5*time.Second)
	if res.Passed {
		t.Error("a command that could not run was reported as passing")
	}
	if res.Err == nil {
		t.Error("a command that could not run left Err nil, so it reads as an ordinary failure")
	}
}

// TestOutputIsCappedFromTheTail. Go prints its FAIL lines last, so a
// head-capped log keeps the part nobody needs and drops the part they do.
func TestOutputIsCappedFromTheTail(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "i=0\nwhile [ $i -lt 20000 ]; do echo padding-line-$i; i=$((i+1)); done\n"+
		"echo THE-LAST-LINE\nexit 1\n")
	argv, _ := Detect(dir, 0)

	res := Run(dir, argv, 60*time.Second)
	if len(res.Output) > maxOutput+64 {
		t.Errorf("output was not capped: %d bytes", len(res.Output))
	}
	if !strings.Contains(res.Output, "THE-LAST-LINE") {
		t.Error("the tail was trimmed away, which is the half that names the failure")
	}
}

// TestNothingIsHandedToAShell. Run execs argv directly, so a repository path
// containing a space or a quote is an argument rather than a command.
func TestNothingIsHandedToAShell(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a dir; touch pwned")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, dir, "exit 0\n")
	argv, err := Detect(dir, 0)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	if res := Run(dir, argv, 30*time.Second); !res.Passed {
		t.Errorf("a path with a space did not run: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("the path was interpreted by a shell")
	}
}
