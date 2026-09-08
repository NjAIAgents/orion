package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The contract with the real spec-kit release Orion pins: every string a
// gate reads is one the templates that release installs actually contain.
// Runs the real `specify` when it is on PATH and skips otherwise -- it is
// the check to run after moving SpecKitTag, and the one that says what
// drifted. The fake `specify` the other tests install is per-test (PATH
// is restored after each), so this one sees the machine's own.
func TestSpecKitReleaseSaysWhatTheGatesRead(t *testing.T) {
	if _, err := exec.LookPath("specify"); err != nil {
		t.Skip("specify is not on PATH; install it to run the contract test:  " + SpecKitInstall)
	}
	repo := repoAt(t, true)
	if did, err := InitSpecKit(repo); err != nil || !did {
		t.Fatalf("InitSpecKit against the real CLI: did=%v err=%v", did, err)
	}

	skill := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(repo, ".claude", "skills", "speckit-"+name, "SKILL.md"))
		if err != nil {
			t.Fatalf("the default orion.json delegates to /speckit-%s, which this release does not install: %v", name, err)
		}
		return string(b)
	}
	for _, name := range []string{"constitution", "specify", "plan", "tasks", "analyze"} {
		skill(name)
	}

	// discovery.markerRe reads this spelling from the spec; the preset asks
	// for it, but the base command must know the form too.
	if s := skill("specify"); !strings.Contains(s, "[NEEDS CLARIFICATION") {
		t.Error("speckit-specify no longer speaks of [NEEDS CLARIFICATION]; the discovery gate reads that spelling")
	} else if !strings.Contains(s, wrapMarker) {
		t.Error("the orion preset's wrap was not composed into speckit-specify")
	}
	// supervisor.criticalRe reads this line from the analyze report.
	if !strings.Contains(skill("analyze"), "Critical Issues Count") {
		t.Error("speckit-analyze no longer reports a Critical Issues Count; the analyze gate reads that line")
	}
	// supervisor.placeholderRe fails a constitution still holding a slot;
	// the template must ship as slots for that check to mean anything.
	tmpl, err := os.ReadFile(filepath.Join(repo, ".specify", "templates", "constitution-template.md"))
	if err != nil {
		t.Fatalf("constitution template: %v", err)
	}
	if !regexp.MustCompile(`\[[A-Z][A-Z0-9_]+\]`).Match(tmpl) {
		t.Error("the constitution template no longer uses [ALL_CAPS] slots; the placeholder check reads that form")
	}
	if !exists(filepath.Join(repo, SpecKitDir, "presets", PresetID, "preset.yml")) {
		t.Error("the orion preset is not registered under .specify/presets/")
	}
}
