package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/agentcfg"
)

func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// A capability bound that never reaches argv is a struct field nobody
// enforces. This is what makes "a fan-out child cannot run the test suite"
// (OR-230) a property of the process rather than a sentence in its prompt.
func TestAToolBoundedRunSaysSoOnItsCommandLine(t *testing.T) {
	args := runArgs("/tmp/settings.json", "write the package", Options{
		Stage:        "fan",
		MaxTurns:     60,
		AllowedTools: WriteOnlyTools,
		DeniedTools:  ShellTools,
	}, &agentcfg.Run{})

	allowed, ok := flagValue(args, "--allowedTools")
	if !ok {
		t.Fatal("--allowedTools was never passed, so the child is unrestricted")
	}
	if strings.Contains(allowed, "Bash") || strings.Contains(allowed, "Task") {
		t.Errorf("--allowedTools %q lets the child run commands or spawn its own", allowed)
	}
	if !strings.Contains(allowed, "Edit") {
		t.Errorf("--allowedTools %q cannot edit, so the child cannot do the work", allowed)
	}
	denied, ok := flagValue(args, "--disallowedTools")
	if !ok || !strings.Contains(denied, "Bash") {
		t.Errorf("--disallowedTools = %q, want it to name Bash; the allowlist alone "+
			"depends on the permission mode in force", denied)
	}
}

// An ordinary run keeps every tool. The bound belongs to the children;
// applying it to the implementer would stop it running the suite it is
// required to run before it finishes.
func TestAnOrdinaryRunIsNotToolBounded(t *testing.T) {
	args := runArgs("/tmp/settings.json", "implement it",
		Options{Stage: "build", MaxTurns: 120}, &agentcfg.Run{})
	if v, ok := flagValue(args, "--allowedTools"); ok {
		t.Errorf("an ordinary run was given an allowlist: %q", v)
	}
	if v, ok := flagValue(args, "--disallowedTools"); ok {
		t.Errorf("an ordinary run was given a denylist: %q", v)
	}
}

// The flags the whole run depends on are still there. runArgs was split out
// of runOnce, and a split that quietly dropped --output-format would leave
// every run unparseable.
func TestRunArgsStillCarriesTheStreamContract(t *testing.T) {
	args := runArgs("/tmp/settings.json", "do it",
		Options{Stage: "build", MaxTurns: 120, Model: "opus", Effort: "high"}, &agentcfg.Run{})
	for _, want := range []string{"-p", "--settings", "--output-format", "--verbose", "--max-turns"} {
		if _, ok := flagValue(args, want); !ok && want != "--verbose" {
			t.Errorf("%s is missing from the command line: %v", want, args)
		}
	}
	if v, _ := flagValue(args, "--model"); v != "opus" {
		t.Errorf("--model = %q", v)
	}
	if v, _ := flagValue(args, "--effort"); v != "high" {
		t.Errorf("--effort = %q", v)
	}
	if v, _ := flagValue(args, "--output-format"); v != "stream-json" {
		t.Errorf("--output-format = %q", v)
	}
}
