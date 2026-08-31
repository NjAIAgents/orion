package adopt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// The adopted-repo twin of the same guard in internal/workspace: this template
// states both ceilings outright, so it wins over the shipped default and wins
// silently. A raise in internal/config that misses this file leaves every
// adopted repository on the old number with nothing red to say so.
func TestTheAdoptedConfigShipsTheCurrentFixCeilings(t *testing.T) {
	dir := t.TempDir()
	body := fmt.Sprintf(defaultConfig, false)
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Load(dir)
	if cfg.Degraded {
		t.Fatalf("defaultConfig: %s", cfg.DegradedReason)
	}
	if got := cfg.QA.Rounds(); got != config.FixRounds {
		t.Errorf("an adopted repo pins qa.max_rounds at %d; the shipped ceiling is %d",
			got, config.FixRounds)
	}
	if got := cfg.CI.Attempts(); got != config.FixRounds {
		t.Errorf("an adopted repo pins ci.max_fix_attempts at %d; the shipped ceiling is %d",
			got, config.FixRounds)
	}
	// An adopted repo is exactly where "a knob nobody can find" bites: its
	// author did not choose these settings and will not go looking for them,
	// so the comment that explains the trade has to travel with the value.
	for _, want := range []string{"_comment_qa", "_comment_ci", "orion config limits"} {
		if !strings.Contains(body, want) {
			t.Errorf("the adopted config carries no %s", want)
		}
	}
}
