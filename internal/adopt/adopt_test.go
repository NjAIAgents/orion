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

// A re-run after an upgrade must REPAIR the stale entry, not append beside
// it. Appending leaves every hook running twice, one against a path that no
// longer exists, so the command meant to fix the repo damages it further.
func TestReRunRepointsStaleHooksInsteadOfDuplicating(t *testing.T) {
	d := repo(t)
	old := "/opt/homebrew/Cellar/orion/0.3.2/bin/orion"
	if _, err := Run(Options{Dir: d, Binary: old}); err != nil {
		t.Fatal(err)
	}

	newBin := "/opt/homebrew/bin/orion"
	res, err := Run(Options{Dir: d, Binary: newBin})
	if err != nil {
		t.Fatal(err)
	}

	var stale, current int
	for _, entries := range readSettings(t, d)["hooks"].(map[string]any) {
		for _, re := range entries.([]any) {
			for _, h := range re.(map[string]any)["hooks"].([]any) {
				cmd := h.(map[string]any)["command"].(string)
				if strings.HasPrefix(cmd, old+" ") {
					stale++
				}
				if strings.HasPrefix(cmd, newBin+" ") {
					current++
				}
			}
		}
	}
	if stale != 0 {
		t.Errorf("%d hook(s) still point at the removed path", stale)
	}
	if current != len(specs()) {
		t.Errorf("got %d current hooks, want %d", current, len(specs()))
	}
	if len(res.Updated) == 0 || !strings.Contains(res.Updated[0], "repointed") {
		t.Errorf("the repair must be reported, got %v", res.Updated)
	}
}

// Only Orion's own hooks may be rewritten.
func TestRetargetLeavesForeignHooksAlone(t *testing.T) {
	hooks := map[string]any{
		"PreToolUse": []any{
			map[string]any{"hooks": []any{
				map[string]any{"command": "/usr/local/bin/orion hook gate"},
				map[string]any{"command": "/usr/local/bin/prettier --write"},
				map[string]any{"command": "some-tool hook thing extra"},
			}},
		},
	}
	if n := retargetOrionHooks(hooks, "/opt/homebrew/bin/orion"); n != 1 {
		t.Fatalf("repointed %d, want exactly the one orion hook", n)
	}
	got := hooks["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)
	if c := got[1].(map[string]any)["command"].(string); c != "/usr/local/bin/prettier --write" {
		t.Errorf("a foreign hook was modified: %q", c)
	}
	if c := got[2].(map[string]any)["command"].(string); c != "some-tool hook thing extra" {
		t.Errorf("a non-orion binary was rewritten: %q", c)
	}
}

// An unreadable settings.json must abort. Treating a permission error as
// "no file" would write a fresh one over a team's hooks and MCP servers,
// which is the exact loss this package exists to prevent.
func TestUnreadableSettingsAbortsRatherThanOverwriting(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read anything, so the failure cannot be provoked")
	}
	d := repo(t)
	cdir := filepath.Join(d, ".claude")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cdir, "settings.json")
	precious := []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"keep-me"}]}]}}`)
	if err := os.WriteFile(p, precious, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })

	if _, err := Run(Options{Dir: d, Binary: "orion"}); err == nil {
		t.Fatal("expected a refusal, not a silent overwrite")
	}
	_ = os.Chmod(p, 0o600)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(precious) {
		t.Errorf("the unreadable file was overwritten:\n%s", b)
	}
}

func TestVersionedPathDetection(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"/opt/homebrew/Cellar/orion/0.3.2/bin/orion", true},
		{"/opt/orion/1.2.10/bin/orion", true},
		{"/opt/orion/v0.3.2/bin/orion", true},
		{"/opt/homebrew/bin/orion", false},
		{"/usr/local/bin/orion", false},
		{"orion", false},
	} {
		if got := versionedPath(tc.in); got != tc.want {
			t.Errorf("versionedPath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A hook command that cannot run is worse than an absent one: the gates read
// as enabled in orion.json while nothing enforces them. init must say so.
func TestInitWarnsWhenTheHookBinaryIsMissing(t *testing.T) {
	d := repo(t)
	res, err := Run(Options{Dir: d, Binary: filepath.Join(t.TempDir(), "gone", "orion")})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "silently") {
		t.Errorf("expected a warning that the gates will do nothing, got:\n%s", joined)
	}
	if !strings.Contains(res.Summary(), "WARNING") {
		t.Errorf("the warning must reach the printed summary:\n%s", res.Summary())
	}
}

// A locally built binary (orion-dev, orion.exe) is still our hook. Requiring
// the name to be exactly "orion" meant a re-run did not recognise it and
// appended a second full set beside it -- the duplication this is meant to
// prevent, caused by the fix for it.
func TestRetargetRecognisesOrionPrefixedBinaries(t *testing.T) {
	hooks := map[string]any{
		"PreToolUse": []any{map[string]any{"hooks": []any{
			map[string]any{"command": "/tmp/orion-dev hook gate"},
			map[string]any{"command": "/usr/bin/orion.exe hook shield"},
			map[string]any{"command": "/usr/bin/orionade hook nonsense"},
		}}},
	}
	if n := retargetOrionHooks(hooks, "/opt/homebrew/bin/orion"); n != 2 {
		t.Fatalf("repointed %d, want 2", n)
	}
	got := hooks["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)
	if c := got[2].(map[string]any)["command"].(string); c != "/usr/bin/orionade hook nonsense" {
		t.Errorf("an unknown hook subcommand must not be rewritten: %q", c)
	}
}

// Duplicates are not cosmetic: Claude Code runs every matching hook, so a
// doubled breaker counts each tool call twice and trips at half the limit.
func TestReRunRemovesDuplicateHooks(t *testing.T) {
	d := repo(t)
	if _, err := Run(Options{Dir: d, Binary: "/tmp/orion-dev"}); err != nil {
		t.Fatal(err)
	}
	// Simulate the older binary that failed to recognise orion-dev and
	// appended its own set alongside.
	p := filepath.Join(d, ".claude", "settings.json")
	b, _ := os.ReadFile(p)
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	for ev, raw := range hooks {
		for _, re := range raw.([]any) {
			e := re.(map[string]any)
			clone := map[string]any{"hooks": []any{map[string]any{
				"type": "command",
				"command": strings.Replace(e["hooks"].([]any)[0].(map[string]any)["command"].(string),
					"/tmp/orion-dev", "/opt/homebrew/bin/orion", 1),
			}}}
			if m, ok := e["matcher"]; ok {
				clone["matcher"] = m
			}
			hooks[ev] = append(hooks[ev].([]any), clone)
		}
	}
	nb, _ := json.MarshalIndent(root, "", "  ")
	if err := os.WriteFile(p, nb, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(Options{Dir: d, Binary: "/opt/homebrew/bin/orion"})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, entries := range readSettings(t, d)["hooks"].(map[string]any) {
		for _, re := range entries.([]any) {
			n += len(re.(map[string]any)["hooks"].([]any))
		}
	}
	if n != len(specs()) {
		t.Errorf("got %d hooks, want %d; duplicates survived", n, len(specs()))
	}
	if !strings.Contains(strings.Join(res.Updated, " "), "duplicate") {
		t.Errorf("the removal must be reported, got %v", res.Updated)
	}
}

// --force repairs WIRING. It must never reset configuration.
//
// This happened for real. `orion doctor` reports broken hooks with "Repair
// with: orion init --force" -- so that is the command people run when a
// binary moves. Running it silently replaced orion.json: approval
// requirements, the merge allowlist, the CI fix loop and the Slack channel
// prefix all reverted to defaults, and the reverted prefix meant the next
// run bound a DIFFERENT Slack channel and reported success.
//
// Configuration is what a person spent time deciding. Hooks are what a tool
// can always rebuild. --force may rebuild the second and must not touch the
// first.
func TestForceRepairsHooksWithoutResettingConfiguration(t *testing.T) {
	dir := t.TempDir()
	mine := `{"version":1,"slack":{"require_approval":true,"channel_prefix":""},` +
		`"ci":{"auto_fix":true,"max_fix_attempts":3}}`
	cfg := filepath.Join(dir, "orion.json")
	if err := os.WriteFile(cfg, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(Options{Dir: dir, Binary: "/usr/bin/true", Force: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != mine {
		t.Fatalf("--force rewrote orion.json.\n got: %s\nwant: %s", got, mine)
	}
	// And it must SAY it kept the file, so nobody assumes a repair also
	// refreshed the defaults.
	if !strings.Contains(strings.Join(res.Skipped, " "), "orion.json") {
		t.Errorf("keeping the config was not reported: %v", res.Skipped)
	}
	// The hooks are the part --force exists to fix, so they must be written.
	settings, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("hooks were not written: %v", err)
	}
	if !strings.Contains(string(settings), "/usr/bin/true") {
		t.Error("hooks were not repointed at the current binary")
	}
}
