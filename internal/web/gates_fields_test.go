package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Both gates whose Clears asks the operator to react on a Slack message share
// the same emoji for the same reason gates.go gives: two hardcoded copies of
// the affordance can drift apart silently, one constant cannot.
var gatesClearedByReacting = []string{"plan-confirmation", "environment-hold"}

func TestConfirmAffordanceUsedConsistentlyInClears(t *testing.T) {
	if strings.TrimSpace(confirmAffordance) == "" {
		t.Fatal("confirmAffordance is empty")
	}
	for _, kind := range gatesClearedByReacting {
		g := gateByKind(t, kind)
		if !strings.Contains(g.Clears, confirmAffordance) {
			t.Errorf("%s: Clears %q does not contain confirmAffordance %q", g.Kind, g.Clears, confirmAffordance)
		}
	}
}

// A source cited as an absolute path, or one escaping the repo, stops meaning
// "where in this tree" -- the whole reason gates.go cites a path rather than a
// line is so the survey can be walked from repo root.
func TestSourcePathsAreRelativeAndLocateFile(t *testing.T) {
	for _, g := range Gates {
		if g.Source == "" {
			t.Errorf("%s: cites no source", g.Kind)
			continue
		}
		if filepath.IsAbs(g.Source) {
			t.Errorf("%s: Source %q is an absolute path, not relative to repo root", g.Kind, g.Source)
		}
		full := filepath.Join(repoRoot, filepath.FromSlash(g.Source))
		info, err := os.Stat(full)
		if err != nil {
			t.Errorf("%s: Source %q does not resolve to a file from repo root: %v", g.Kind, g.Source, err)
			continue
		}
		if info.IsDir() {
			t.Errorf("%s: Source %q resolves to a directory, not the file implementing the gate", g.Kind, g.Source)
		}
	}
}

// substantiveMinLen is short prose, not a sentence budget -- it exists only to
// catch a placeholder like "n/a" or a single word standing in for a reason.
const substantiveMinLen = 20

// The Why field is the whole justification for leaving a gate off the board;
// a blank or token Why is indistinguishable from nobody having checked.
func TestWhyFieldSubstantiveForUnmappedGates(t *testing.T) {
	for _, g := range Gates {
		if g.State != "" {
			continue
		}
		why := strings.TrimSpace(g.Why)
		if len(why) < substantiveMinLen {
			t.Errorf("%s: Why %q is not substantive (want at least %d non-whitespace chars)", g.Kind, g.Why, substantiveMinLen)
		}
	}
}
