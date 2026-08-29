package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OR-179: applyProtection must send whatever strict value it is given
// rather than hardcoding true -- that hardcode was the bug, silently
// reverting an operator's deliberate strict=false.
func TestApplyProtectionSendsTheRequestedStrictValue(t *testing.T) {
	for _, strict := range []bool{true, false} {
		strict := strict
		t.Run(boolLabel(strict), func(t *testing.T) {
			dir := t.TempDir()
			capture := filepath.Join(dir, "captured.json")
			fakeBin(t, "gh", "#!/bin/sh\ncat > "+capture+"\nexit 0\n")

			if err := applyProtection(t.TempDir(), "acme/widgets", "main", []string{"build"}, 1, strict); err != nil {
				t.Fatalf("applyProtection returned an error: %v", err)
			}

			body, err := os.ReadFile(capture)
			if err != nil {
				t.Fatalf("gh was never invoked with a request body: %v", err)
			}
			var sent struct {
				RequiredStatusChecks struct {
					Strict bool `json:"strict"`
				} `json:"required_status_checks"`
			}
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatalf("could not decode the body applyProtection sent: %v (%s)", err, body)
			}
			if sent.RequiredStatusChecks.Strict != strict {
				t.Errorf("required_status_checks.strict = %v, want %v (body: %s)",
					sent.RequiredStatusChecks.Strict, strict, body)
			}
		})
	}
}

func boolLabel(b bool) string {
	if b {
		return "strict-true"
	}
	return "strict-false"
}

// currentRequireUpToDate is what a dry-run compares the desired value
// against, so it must tell "branch has strict=false on GitHub already" apart
// from "branch has no required-status-checks block to compare at all" --
// collapsing those two would make a dry-run claim "no change" when there
// was never anything to change against.
func TestCurrentRequireUpToDateReadsWhatIsOnGitHub(t *testing.T) {
	cases := []struct {
		name       string
		response   string
		wantHave   bool
		wantStrict bool
	}{
		{
			name:       "strict currently false",
			response:   `{"required_status_checks":{"strict":false,"contexts":[]}}`,
			wantHave:   true,
			wantStrict: false,
		},
		{
			name:       "strict currently true",
			response:   `{"required_status_checks":{"strict":true,"contexts":["build"]}}`,
			wantHave:   true,
			wantStrict: true,
		},
		{
			name:     "no required_status_checks block at all",
			response: `{"required_status_checks":null}`,
			wantHave: false,
		},
		{
			name:     "not valid JSON",
			response: `not json`,
			wantHave: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fakeBin(t, "gh", "#!/bin/sh\ncat <<'EOF'\n"+tc.response+"\nEOF\n")

			have, strict := currentRequireUpToDate(t.TempDir(), "acme/widgets", "main")
			if have != tc.wantHave {
				t.Fatalf("haveCurrent = %v, want %v", have, tc.wantHave)
			}
			if have && strict != tc.wantStrict {
				t.Errorf("strict = %v, want %v", strict, tc.wantStrict)
			}
		})
	}
}

// A gh that exits non-zero (no protection exists yet, or no permission) must
// read as "nothing to compare against", not panic or misreport strict=false
// as a real observed value.
func TestCurrentRequireUpToDateHandlesGhFailure(t *testing.T) {
	fakeBin(t, "gh", "#!/bin/sh\necho 'HTTP 404: Branch not protected' >&2\nexit 1\n")

	have, _ := currentRequireUpToDate(t.TempDir(), "acme/widgets", "main")
	if have {
		t.Error("a failing gh call must report haveCurrent=false, not a fabricated value")
	}
}

// The message strings themselves aren't asserted line-for-line elsewhere,
// but this pins that a dry-run detecting a mismatch actually calls out
// GitHub's current value, the desired value and the source -- otherwise a
// re-run of `orion protect --dry-run` could report "no change" the very run
// it would silently revert an operator's edit.
func TestReportStrictDryRunNamesTheMismatch(t *testing.T) {
	fakeBin(t, "gh", "#!/bin/sh\ncat <<'EOF'\n{\"required_status_checks\":{\"strict\":false,\"contexts\":[]}}\nEOF\n")

	out := captureStdout(t, func() {
		reportStrictDryRun(t.TempDir(), "acme/widgets", "main", true, "orion.json (vcs.require_up_to_date)")
	})

	for _, want := range []string{"main", "false", "true", "orion.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output must mention %q, got: %s", want, out)
		}
	}
}
