package main

// Additional OR-61 coverage for `orion web`'s bad-port paths and `orion
// help`'s listing, at the level the shipped tests in web_test.go do not
// reach: the actual binary and exit code, not just webPort's return value.

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/testproc"
)

// A port above the valid range must stop the command at the binary boundary,
// not just fail webPort in isolation.
func TestTheWebSubcommandRejectsAnOutOfRangePort(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "web", "--port", "65536")
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 64 {
		t.Fatalf("`orion web --port 65536` = %v, want exit 64 (stderr: %q)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--port") {
		t.Errorf("stderr should name the flag it rejected, got: %q", stderr.String())
	}
}

// A non-numeric value must be rejected the same way, at the binary.
func TestTheWebSubcommandRejectsANonNumericPort(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "web", "--port", "abc")
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 64 {
		t.Fatalf("`orion web --port abc` = %v, want exit 64 (stderr: %q)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--port") {
		t.Errorf("stderr should name the flag it rejected, got: %q", stderr.String())
	}
}

// A trailing --port with nothing after it is a half-typed command, not a
// request for the default -- argFlag cannot tell the two apart, so this has
// to be checked before the value is even read.
func TestTheWebSubcommandRejectsATrailingPortFlagWithNoValue(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "web", "--port")
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 64 {
		t.Fatalf("`orion web --port` = %v, want exit 64 (stderr: %q)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--port") {
		t.Errorf("stderr should name the flag it rejected, got: %q", stderr.String())
	}
}

// `orion help` is what a person actually types to discover the command --
// TestHelpShowsTheWebCommand only checks the usage constant, not that the
// help subcommand prints it.
func TestOrionHelpListsTheWebSubcommand(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "help")
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("orion help failed: %v", err)
	}
	if !strings.Contains(string(out), "orion web") {
		t.Errorf("orion help output does not mention `orion web`, got: %q", truncateStr(string(out), 2000))
	}
}
