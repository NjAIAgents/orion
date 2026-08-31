package main

// `cmd/orion/hold.go` is the wiring that binds internal/work's hold state
// machine to the real `claude`/`gh`/doctor checks -- untestable in-process
// because it shells out and calls os.Exit directly. It had no coverage at
// all before this file: these run the built binary as a subprocess, the
// same pattern releaseclose_cli_test.go uses for `orion release close`.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/work"
)

// `orion reset --held unknown-fault` when a real fault (quota) is standing
// must exit non-zero, name the fault that IS held so the operator can
// correct the typo, and must not clear the standing hold.
func TestCLIResetHeldUnknownFaultListsKnownFaultsAndExits64(t *testing.T) {
	bin := orionBinary(t)
	home := t.TempDir()
	if _, _, err := work.RecordFault(home,
		work.Fault{Kind: work.FaultQuota, Cause: "quota exhausted", Fix: "wait"},
		"FCIA-6", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "reset", "--held", "unknown-fault")
	cmd.Env = append(os.Environ(), "ORION_HOME="+home)
	out, runErr := cmd.CombinedOutput()

	code := 0
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if runErr != nil {
		t.Fatalf("running orion reset --held unknown-fault: %v\n%s", runErr, out)
	}
	if code != 64 {
		t.Errorf("exit code = %d, want 64\n%s", code, out)
	}
	if !strings.Contains(string(out), "quota") {
		t.Errorf("the error does not name the fault that is actually held:\n%s", out)
	}
	if h := work.Holds(home); len(h) != 1 {
		t.Errorf("an unrecognized --held value cleared the standing hold anyway: %+v", h)
	}
}

// With nothing held, `orion reset --held` reports that plainly and exits
// zero -- it is not an error to run the command when there is nothing to
// clear.
func TestCLIResetHeldWithNothingHeldReportsAndExitsZero(t *testing.T) {
	bin := orionBinary(t)
	home := t.TempDir()

	cmd := exec.Command(bin, "reset", "--held")
	cmd.Env = append(os.Environ(), "ORION_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("orion reset --held with nothing held should exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "nothing is held") {
		t.Errorf("did not report that nothing is held:\n%s", out)
	}
}
