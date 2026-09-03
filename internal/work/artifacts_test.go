package work

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/decide"
)

// The implementer's prompt names the documents it may treat as agreed. A
// confirmed recommendation belongs there; an unconfirmed one is a proposal,
// and an implementer that builds on it makes the thing nobody agreed to the
// premise of the diff (OR-153).
func TestArtifactsForCarriesConfirmedDecisionsAndNotPendingOnes(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{decide.PendingDir, decide.ConfirmedDir} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(artifactsFor(dir, config.Config{}), " ")
	if !strings.Contains(got, filepath.Clean(decide.ConfirmedDir)) {
		t.Errorf("the implementer cannot see confirmed decisions: %q", got)
	}
	if strings.Contains(got, filepath.Clean(decide.PendingDir)) {
		t.Errorf("the implementer was handed an UNCONFIRMED recommendation: %q", got)
	}
}
