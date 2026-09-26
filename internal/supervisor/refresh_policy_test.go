package supervisor

// OR-479: every supervised stage run regenerates the sandbox policy, so a
// workspace created by an older release stops running its stages under that
// release's allowlist.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

func TestRunRefreshesAStaleSandboxPolicy(t *testing.T) {
	w := ws(t, "")
	writeIntentCapture(t, w, "# Intent\n\n## Open questions\n- [x] Settled.\n")
	fakeClaudeThatFinishes(t)

	// What an older release left behind: a policy with none of this build's hosts.
	if err := os.MkdirAll(filepath.Dir(w.SettingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"sandbox":{"network":{"allowedDomains":["api.anthropic.com"]}}}`
	if err := os.WriteFile(w.SettingsPath(), []byte(stale), workspace.PrivateFileMode); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(w, Options{Stage: "spec", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	b, err := os.ReadFile(w.SettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Sandbox struct {
			Network struct {
				AllowedDomains []string `json:"allowedDomains"`
			} `json:"network"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	// raw.githubusercontent.com arrived with OR-466; a stale policy lacks it.
	if !slices.Contains(got.Sandbox.Network.AllowedDomains, "raw.githubusercontent.com") {
		t.Errorf("the stage ran under the stale policy; allowedDomains = %v", got.Sandbox.Network.AllowedDomains)
	}
	if info, err := os.Stat(w.SettingsPath()); err == nil && info.Mode().Perm() != workspace.PrivateFileMode && os.PathSeparator == '/' {
		t.Errorf("refreshed policy is mode %v, want the private mode", info.Mode().Perm())
	}
}
