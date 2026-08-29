package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileYieldsDegradedDefaults(t *testing.T) {
	cfg := Load(t.TempDir())
	if !cfg.Degraded {
		t.Error("a missing orion.json must be visible, not silent")
	}
	if cfg.Limits.MaxToolCalls != Defaults().Limits.MaxToolCalls {
		t.Error("defaults must still apply so an unconfigured repo is not unguarded")
	}
}

func TestMalformedConfigFallsBackWithoutWideningControls(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orion.json"), []byte("{not json"), 0o644)
	cfg := Load(dir)
	if !cfg.Degraded {
		t.Fatal("a broken config must report itself")
	}
	if cfg.Limits.MaxToolCalls <= 0 {
		t.Fatal("a broken config must never yield an unlimited budget")
	}
}

// Zero is indistinguishable from absent in JSON, and "unlimited" is never
// a safe reading for a circuit breaker.
func TestZeroLimitsRestoreDefaults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"limits":{"max_tool_calls":0,"max_repeat_identical":0}}`), 0o644)
	cfg := Load(dir)
	d := Defaults()
	if cfg.Limits.MaxToolCalls != d.Limits.MaxToolCalls {
		t.Errorf("max_tool_calls = %d, want default %d", cfg.Limits.MaxToolCalls, d.Limits.MaxToolCalls)
	}
	if cfg.Limits.MaxRepeatIdentical != d.Limits.MaxRepeatIdentical {
		t.Error("zero must not disable loop detection")
	}
}

// OR-181: an unset or zero concurrency cap must fall back to a low default,
// never to unbounded fan-out -- the same "0 restores the default, never
// disables the control" rule every other limit here already follows.
func TestZeroConcurrencyCapRestoresALowDefault(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"limits":{"max_concurrent_children":0}}`), 0o644)
	cfg := Load(dir)
	d := Defaults()
	if cfg.Limits.MaxConcurrentChildren != d.Limits.MaxConcurrentChildren {
		t.Errorf("max_concurrent_children = %d, want default %d",
			cfg.Limits.MaxConcurrentChildren, d.Limits.MaxConcurrentChildren)
	}
	if cfg.Limits.MaxConcurrentChildren > 3 {
		t.Errorf("default concurrency cap is %d, want low (2 or 3) per OR-181",
			cfg.Limits.MaxConcurrentChildren)
	}
}

// A project that wants more (or less) concurrency can configure it -- the
// cap is not hardcoded.
func TestConcurrencyCapIsConfigurable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"limits":{"max_concurrent_children":5}}`), 0o644)
	if cfg := Load(dir); cfg.Limits.MaxConcurrentChildren != 5 {
		t.Errorf("max_concurrent_children = %d, want 5", cfg.Limits.MaxConcurrentChildren)
	}
}

func TestCommentKeysAreIgnored(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orion.json"), []byte(`{
	  "$schema": "x",
	  "_comment_limits": "the template documents itself inline",
	  "limits": {"max_tool_calls": 42}
	}`), 0o644)
	cfg := Load(dir)
	if cfg.Degraded {
		t.Fatalf("template comment keys must not break parsing: %s", cfg.DegradedReason)
	}
	if cfg.Limits.MaxToolCalls != 42 {
		t.Errorf("max_tool_calls = %d, want 42", cfg.Limits.MaxToolCalls)
	}
}

// A machine that has never run `orion config agents` must not be treated
// as broken -- every actor just keeps its shipped default, the same as a
// project that has never touched orion.json (OR-132).
func TestLoadAgentsOnAMissingFileIsAnEmptyRosterNotAnError(t *testing.T) {
	agents, err := LoadAgents(t.TempDir())
	if err != nil {
		t.Fatalf("a missing agents.json must not be an error: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("got %d agents, want none", len(agents))
	}
}

func TestLoadAgentsRejectsInvalidJSON(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(AgentsPath(home), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgents(home); err == nil {
		t.Fatal("invalid JSON must be reported, not silently treated as no overrides")
	}
}

func TestSaveAgentsThenLoadAgentsRoundTrips(t *testing.T) {
	home := t.TempDir()
	name := "Alex"
	want := map[string]Agent{
		"implementer": {Name: &name, Model: "opus", Effort: "high"},
	}
	if err := SaveAgents(home, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAgents(home)
	if err != nil {
		t.Fatal(err)
	}
	a := got["implementer"]
	if a.Name == nil || *a.Name != "Alex" || a.Model != "opus" || a.Effort != "high" {
		t.Errorf("round trip = %+v", a)
	}
}

// SaveAgents must create ORION_HOME if this is the very first thing written
// there -- the global config file must not require some other command to
// have run first just to create the directory.
func TestSaveAgentsCreatesTheHomeDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-yet-created")
	if err := SaveAgents(home, map[string]Agent{"qa": {Effort: "low"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(AgentsPath(home)); err != nil {
		t.Fatalf("agents.json was not written: %v", err)
	}
}

func TestFindRootWalksUp(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "orion.json"), []byte("{}"), 0o644)
	deep := filepath.Join(root, "a", "b", "c")
	os.MkdirAll(deep, 0o755)

	got, err := FindRoot(deep)
	if err != nil {
		t.Fatal(err)
	}
	// macOS temp dirs are symlinked via /private, so compare resolved paths.
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("FindRoot = %s, want %s", gotResolved, wantResolved)
	}
}

func TestFindRootFailsOutsideAnyProject(t *testing.T) {
	if _, err := FindRoot(t.TempDir()); err == nil {
		t.Error("no orion.json and no .git should be an error, not a silent root")
	}
}

// OR-179: an operator who never touches vcs.require_up_to_date must keep
// today's behaviour -- `orion protect` enforcing strict.
func TestRequireUpToDateDefaultsTrue(t *testing.T) {
	cfg := Load(t.TempDir())
	if !cfg.VCS.RequireUpToDate {
		t.Error("vcs.require_up_to_date must default true so nothing changes for an unconfigured repo")
	}
	if got := cfg.VCSRequireUpToDateSource(); got != "default; not set in orion.json" {
		t.Errorf("VCSRequireUpToDateSource() = %q, want the default source", got)
	}
}

// OR-179: an operator's explicit false must be honoured, not silently
// coerced back to true the way a bare zero-value limit is.
func TestRequireUpToDateExplicitFalseIsHonoured(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"vcs":{"require_up_to_date":false}}`), 0o644)
	cfg := Load(dir)
	if cfg.VCS.RequireUpToDate {
		t.Error("an explicit require_up_to_date:false must be honoured, not reverted to the default")
	}
	if got := cfg.VCSRequireUpToDateSource(); got != "orion.json (vcs.require_up_to_date)" {
		t.Errorf("VCSRequireUpToDateSource() = %q, want the explicit source", got)
	}
}

// An explicit true must not be reported as coming from the default, so the
// message `orion protect` prints stays honest about where the value came
// from either way.
func TestRequireUpToDateExplicitTrueReportsExplicitSource(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"vcs":{"require_up_to_date":true}}`), 0o644)
	cfg := Load(dir)
	if !cfg.VCS.RequireUpToDate {
		t.Error("require_up_to_date:true must stay true")
	}
	if got := cfg.VCSRequireUpToDateSource(); got != "orion.json (vcs.require_up_to_date)" {
		t.Errorf("VCSRequireUpToDateSource() = %q, want the explicit source", got)
	}
}
