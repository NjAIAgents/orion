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
