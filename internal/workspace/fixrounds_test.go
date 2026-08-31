package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// defaultProjectConfig states the fix-round ceilings OUTRIGHT rather than
// leaving them to the shipped default, so raising the default in
// internal/config does nothing for a new workspace unless this file is raised
// with it -- the value here wins, and it wins silently.
//
// That is the regression this guards: every workspace created after the raise
// would run on the old ceiling, pinned by a number its author never chose, and
// nothing would be red. Asserted through config.Load rather than by grepping
// for "3" so it reads the value the way the stages do.
func TestTheNewWorkspaceConfigShipsTheCurrentFixCeilings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(defaultProjectConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Load(dir)
	if cfg.Degraded {
		t.Fatalf("defaultProjectConfig: %s", cfg.DegradedReason)
	}
	if got := cfg.QA.Rounds(); got != config.FixRounds {
		t.Errorf("a new workspace pins qa.max_rounds at %d; the shipped ceiling is %d",
			got, config.FixRounds)
	}
	if got := cfg.CI.Attempts(); got != config.FixRounds {
		t.Errorf("a new workspace pins ci.max_fix_attempts at %d; the shipped ceiling is %d",
			got, config.FixRounds)
	}
}
