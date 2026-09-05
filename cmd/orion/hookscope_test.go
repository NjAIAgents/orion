package main

import (
	"github.com/orion-sdlc/orion/internal/testproc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The breaker bounds an unattended agent. Applying it to a person at a
// keyboard is not merely noisy: a trip COMMITS, so on 2026-09-01 an
// interactive session past the wall-clock ceiling had seven files committed
// to develop as an unverified snapshot and every subsequent call refused
// (OR-263).
func TestTheBreakerIsInactiveOutsideASupervisedRun(t *testing.T) {
	t.Setenv("ORION_WORKSPACE", "")
	t.Setenv("ORION_BREAKER_FORCE", "")
	if supervisedRun() {
		t.Error("a session with no workspace is not a supervised run, so the breaker must not arm")
	}
}

// And the half that matters just as much: nothing is relaxed INSIDE a run.
// A scope fix that quietly disarmed the breaker where it belongs would trade
// one silent failure for a worse one.
func TestTheBreakerIsActiveInsideASupervisedRun(t *testing.T) {
	t.Setenv("ORION_WORKSPACE", "orion-83d87b")
	t.Setenv("ORION_BREAKER_FORCE", "")
	if !supervisedRun() {
		t.Error("a run the supervisor started must still be bounded")
	}
}

// The way back in, so the breaker stays reachable from a shell for testing
// and a future non-supervised runner can opt in without faking a workspace.
func TestTheForceFlagArmsTheBreakerWithoutAWorkspace(t *testing.T) {
	t.Setenv("ORION_WORKSPACE", "")
	t.Setenv("ORION_BREAKER_FORCE", "1")
	if !supervisedRun() {
		t.Error("ORION_BREAKER_FORCE=1 must arm the breaker with no workspace set")
	}
}

// Only "1" opts in. A stray empty or "0" in the environment must not read as
// consent, or the escape hatch becomes the default by accident.
func TestOnlyAnExplicitOneArmsTheBreaker(t *testing.T) {
	for _, v := range []string{"", "0", "false", "yes"} {
		t.Setenv("ORION_WORKSPACE", "")
		t.Setenv("ORION_BREAKER_FORCE", v)
		if v == "1" {
			continue
		}
		if supervisedRun() {
			t.Errorf("ORION_BREAKER_FORCE=%q must not arm the breaker", v)
		}
	}
}

// The end-to-end contract, through the real binary and the real hook
// protocol: a human session gets exit 0 and is told why, rather than being
// blocked or -- the actual bug -- committed for.
//
// gate and shield are checked in the same run because this change must NOT
// widen: they guard dangerous commands and self-editing guardrails, which
// apply to anyone holding the tool, so they stay armed with no workspace.
func TestOutsideARunTheBreakerAllowsAndSaysSoWhileGateAndShieldStayArmed(t *testing.T) {
	bin := buildOrion(t)
	payload := `{"session_id":"or-263","cwd":"` + repoRoot(t) +
		`","hook_event_name":"PreToolUse","tool_name":"Bash",` +
		`"tool_input":{"command":"echo hello"}}`

	t.Run("breaker is inactive and names the reason", func(t *testing.T) {
		out, code := runHookBinary(t, bin, "breaker", payload, "ORION_WORKSPACE=")
		if code != 0 {
			t.Fatalf("a chat session must not be breakered; exit %d\n%s", code, out)
		}
		if !strings.Contains(out, "not a supervised run") {
			t.Errorf("the operator is not told why the breaker did nothing:\n%s", out)
		}
	})

	// Allowing is the correct verdict for `echo hello`; what is pinned here
	// is that these two still RAN rather than being skipped by run scope.
	for _, name := range []string{"gate", "shield"} {
		t.Run(name+" is unaffected by run scope", func(t *testing.T) {
			out, _ := runHookBinary(t, bin, name, payload, "ORION_WORKSPACE=")
			if strings.Contains(out, "not a supervised run") {
				t.Errorf("%s must not be scoped to supervised runs:\n%s", name, out)
			}
		})
	}
}

func buildOrion(t *testing.T) string {
	t.Helper()
	// The suffix matters: `go build -o` writes exactly the name it is given,
	// and Windows will not exec an extension-less file -- every CLI test
	// then failed with "executable file not found in %PATH%" (OR-342).
	bin := filepath.Join(t.TempDir(), "orion"+exeSuffix())
	cmd := testproc.Command(t, "go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

// runHookBinary feeds one hook payload to the built binary and returns its
// combined output and exit code. env entries are appended last so they win.
func runHookBinary(t *testing.T, bin, name, payload string, env ...string) (string, int) {
	t.Helper()
	cmd := testproc.Command(t, bin, "hook", name)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), append([]string{"ORION_BREAKER_FORCE="}, env...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v\n%s", name, err, out)
	}
	return string(out), code
}
