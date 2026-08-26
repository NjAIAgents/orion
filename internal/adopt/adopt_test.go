package adopt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	return m
}

func TestAdoptsCleanRepo(t *testing.T) {
	d := repo(t)
	res, err := Run(Options{Dir: d, Binary: "/usr/local/bin/orion"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"orion.json", "docs/intent", "specs", "plans", "evals", ".claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(d, p)); err != nil {
			t.Errorf("%s was not created", p)
		}
	}
	if len(res.Created) == 0 {
		t.Error("nothing reported as created")
	}

	// The generated config must parse. One that does not falls back to
	// defaults silently, so the limits in force are not the ones written.
	b, _ := os.ReadFile(filepath.Join(d, "orion.json"))
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("generated orion.json is invalid: %v", err)
	}
	gates := cfg["gates"].(map[string]any)
	if gates["require_plan_before_edit"] != false {
		t.Error("the plan gate should default OFF for an adopted repo")
	}
}

// The whole reason this package exists: settings.json usually already holds a
// team's own hooks, permissions and MCP servers, and losing them is the worst
// possible outcome of adopting a tool.
func TestPreservesExistingSettings(t *testing.T) {
	d := repo(t)
	existing := map[string]any{
		"permissions": map[string]any{"deny": []any{"Read(.env)"}},
		"mcpServers":  map[string]any{"mine": map[string]any{"command": "my-server"}},
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "Bash",
				"hooks":   []any{map[string]any{"type": "command", "command": "my-own-hook"}},
			}},
		},
	}
	if err := os.MkdirAll(filepath.Join(d, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(filepath.Join(d, ".claude", "settings.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(Options{Dir: d, Binary: "orion"})
	if err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, d)

	if got["permissions"] == nil {
		t.Error("permissions were destroyed")
	}
	if got["mcpServers"] == nil {
		t.Error("mcpServers were destroyed")
	}
	if res.Backup == "" {
		t.Error("an existing settings.json must be backed up before writing")
	}
	if _, err := os.Stat(res.Backup); err != nil {
		t.Error("the reported backup does not exist")
	}

	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), "my-own-hook") {
		t.Error("an existing hook was lost")
	}
	if !strings.Contains(string(raw), "orion hook gate") {
		t.Error("Orion's own hook was not added")
	}
}

// Adopting twice must change nothing the second time, or every run adds
// another copy of every hook.
func TestIdempotent(t *testing.T) {
	d := repo(t)
	if _, err := Run(Options{Dir: d, Binary: "orion"}); err != nil {
		t.Fatal(err)
	}
	first := readSettings(t, d)

	res, err := Run(Options{Dir: d, Binary: "orion"})
	if err != nil {
		t.Fatal(err)
	}
	second := readSettings(t, d)

	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Error("a second adoption changed settings.json; hooks are being duplicated")
	}
	if len(res.Updated) != 0 {
		t.Errorf("second run reported updates: %v", res.Updated)
	}
	if res.Backup != "" {
		t.Error("a no-op run should not leave a backup file behind")
	}
}

// The breaker must be on both events. One without the other gives a breaker
// that counts but never refuses.
func TestBreakerOnBothEvents(t *testing.T) {
	d := repo(t)
	if _, err := Run(Options{Dir: d, Binary: "orion"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(readSettings(t, d)["hooks"])
	s := string(raw)
	for _, want := range []string{"PreToolUse", "PostToolUse", "orion hook breaker", "orion hook shield", "orion hook session-start"} {
		if !strings.Contains(s, want) {
			t.Errorf("hooks missing %q", want)
		}
	}
	if n := strings.Count(s, "orion hook breaker"); n != 2 {
		t.Errorf("breaker appears %d times, want 2 (PreToolUse and PostToolUse)", n)
	}
}

// A settings file we cannot parse may still be precious. Overwriting it is
// exactly the loss this package exists to prevent.
func TestRefusesToClobberUnparseableSettings(t *testing.T) {
	d := repo(t)
	if err := os.MkdirAll(filepath.Join(d, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := []byte("{ this is not json")
	p := filepath.Join(d, ".claude", "settings.json")
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Run(Options{Dir: d, Binary: "orion"})
	if err == nil {
		t.Fatal("expected a refusal rather than an overwrite")
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(orig) {
		t.Error("the unparseable file was modified; it must be left alone")
	}
}

func TestExistingConfigNotOverwrittenWithoutForce(t *testing.T) {
	d := repo(t)
	p := filepath.Join(d, "orion.json")
	if err := os.WriteFile(p, []byte(`{"version":1,"mine":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Dir: d, Binary: "orion"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "mine") {
		t.Error("an existing orion.json was overwritten without --force")
	}
}

func TestWarnsWhenNotAGitRepo(t *testing.T) {
	d := t.TempDir() // no .git
	res, err := Run(Options{Dir: d, Binary: "orion"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 {
		t.Error("adopting a non-repo should warn: the artifact chain is meant to be committed")
	}
}
