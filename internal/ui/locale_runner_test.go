package ui

import (
	"github.com/orion-sdlc/orion/internal/testproc"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The whole point of OR-210 is that these four tests must pass no matter what
// the runner has exported into the environment before go test ever starts --
// that is exactly what a bare t.Setenv("LANG", ...) cannot guarantee, because
// a pre-exported LC_ALL or LC_CTYPE outranks it. Running the package's own
// test binary as a subprocess, under a controlled runner environment, is the
// only way to prove that: an in-process test can set env vars but cannot
// simulate "the runner already exported something before the test process
// started".
var localeSensitiveTests = []string{
	"TestEveryStatusRendersItsOwnIcon",
	"TestTheIconsFallBackToASCII",
	"TestTheIconColumnIsPaddedInCellsNotRunes",
	"TestABoundaryDegradesToASCIIWithTheTransitionLegible",
}

func TestLocaleSensitiveTestsSurviveWhateverTheRunnerExports(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a nested go test binary; skipped with -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	runnerEnvs := []struct {
		name string
		env  []string
	}{
		{"LC_ALL=C", []string{"LC_ALL=C"}},
		{"LC_CTYPE=UTF-8", []string{"LC_CTYPE=UTF-8"}},
		{"neither exported", nil},
	}

	for _, tc := range runnerEnvs {
		t.Run(tc.name, func(t *testing.T) {
			cmd := testproc.Command(t, "go", "test", "-run", "^("+strings.Join(localeSensitiveTests, "|")+")$", ".")
			cmd.Dir = "."
			cmd.Env = filterLocaleVars(os.Environ())
			cmd.Env = append(cmd.Env, tc.env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("locale-sensitive tests failed with runner exporting %s:\n%s", tc.name, out)
			}
		})
	}
}

// filterLocaleVars strips LC_ALL, LC_CTYPE and LANG from the parent
// environment so the subprocess starts from the exact runner condition the
// subtest is simulating, not whatever happens to be set on this machine.
func filterLocaleVars(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "LC_ALL="),
			strings.HasPrefix(kv, "LC_CTYPE="),
			strings.HasPrefix(kv, "LANG="):
			continue
		}
		out = append(out, kv)
	}
	return out
}
