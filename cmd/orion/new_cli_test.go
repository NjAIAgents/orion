package main

import (
	"github.com/orion-sdlc/orion/internal/testproc"
	"strings"
	"testing"
)

// These exercise `orion new` as a real subprocess rather than through
// newRun(), because the behaviour under test -- the deprecated-flag refusal
// and the terminal requirement -- lives in runNew() (cmd/orion/new.go),
// which newRun() does not cover and which calls os.Exit via exitOn. Building
// the binary and driving it follows the same pattern as
// releaseclose_cli_test.go's orionBinary/TestCLI* tests in this package.
//
// A subprocess run by `go test` has no controlling terminal on its stdin, so
// isTerminal(os.Stdin) is false here exactly as it would be for a script or
// a CI job piping input in -- which is the case these tests are named for.

// Command errors when stdin is not a terminal, and the error names the
// manual-creation fallback rather than just failing silently.
func TestCLIRefusesWhenStdinIsNotATerminal(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "new", "customers should see claim status in the portal")
	cmd.Env = append(cmd.Env, "ORION_HOME=/nonexistent-home-for-test")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit with no terminal attached, got success:\n%s", out)
	}
	for _, want := range []string{"terminal", "orion plan"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("refusal output missing %q:\n%s", want, out)
		}
	}
}

// Each deprecated flag is refused before any question could be asked, and
// each error names the workspace concept that no longer applies here --
// checked before the terminal gate even matters, since none of these should
// reach the interview regardless.
func TestCLIRefusesEachDeprecatedFlagBeforeQuestions(t *testing.T) {
	bin := orionBinary(t)
	for _, flag := range []string{"--from", "--template", "--container", "--skip-discovery"} {
		t.Run(flag, func(t *testing.T) {
			cmd := testproc.Command(t, bin, "new", "some idea", flag, "x")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s: expected a non-zero exit, got success:\n%s", flag, out)
			}
			text := string(out)
			if !strings.Contains(text, flag) {
				t.Errorf("%s: refusal does not name the flag itself:\n%s", flag, text)
			}
			if !strings.Contains(text, "workspace") {
				t.Errorf("%s: refusal does not mention the workspace it would have provisioned:\n%s", flag, text)
			}
			if !strings.Contains(text, "orion plan") {
				t.Errorf("%s: refusal does not direct to `orion plan <KEY>`:\n%s", flag, text)
			}
		})
	}
}

// A boolean flag with no leading dashes stripped -- e.g. `--from=repo-url` --
// is refused the same way as the bare flag, since argFlag also matches the
// `name=value` form.
func TestCLIRefusesDeprecatedFlagGivenAsEqualsForm(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "new", "some idea", "--from=git@example.com:org/repo.git")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit, got success:\n%s", out)
	}
	if !strings.Contains(string(out), "--from") {
		t.Errorf("refusal does not name --from:\n%s", out)
	}
}
