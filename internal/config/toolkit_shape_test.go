package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// OR-296: the toolkit block is shape-checked before the struct decode, and a
// bad block is dropped rather than allowed to fail the whole file.

// parseToolkit must reject a shape that expresses order -- an array where an
// object belongs -- before any struct decode happens, since decoding an
// array into Toolkit would fail with a type error naming no reason.
func TestParseToolkitDetectsBadShapeBeforeStructDecode(t *testing.T) {
	raw := json.RawMessage(`[{"repo":"https://example.com/a.git"}]`)
	_, err := parseToolkit(raw)
	if err == nil {
		t.Fatal("a toolkit list must be rejected by parseToolkit itself")
	}
	if !strings.Contains(err.Error(), "decisions/0001") {
		t.Errorf("error must cite the ordering decision, got: %v", err)
	}
}

// A bad toolkit block must be deleted from the raw map before the struct
// decode runs, so the invalid block never reaches json.Unmarshal alongside
// the rest of the file.
func TestBadToolkitBlockIsDroppedFromRawBeforeStructDecode(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":["not","an","object"],"limits":{"max_tool_calls":42}}`)
	// If the bad block had survived into the struct decode, either the whole
	// decode would fail (leaving Degraded true and defaults everywhere) or
	// Toolkit would carry partial garbage. Neither happened: the block was
	// dropped, so Toolkit is the untouched default and the rest of the file
	// decoded normally.
	if cfg.Toolkit.Repo != njagentsDefaultRepoForTest(t) {
		t.Errorf("toolkit.repo = %q, want the untouched default after a dropped block", cfg.Toolkit.Repo)
	}
	if cfg.Limits.MaxToolCalls != 42 {
		t.Errorf("max_tool_calls = %d, want 42 -- a dropped toolkit block must not affect siblings", cfg.Limits.MaxToolCalls)
	}
}

// Rest of config loads successfully even when the toolkit block itself is
// invalid: Load never returns an error, and Degraded stays false because the
// rest of the document parsed fine.
func TestRestOfConfigLoadsWhenToolkitBlockIsInvalid(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"deploy":"/ship-it"}},"vcs":{"default_branch":"main","work_branch":"feature/x"}}`)
	if cfg.Degraded {
		t.Error("an invalid toolkit block must not degrade the rest of the config")
	}
	if cfg.VCS.DefaultBranch != "main" || cfg.VCS.WorkBranch != "feature/x" {
		t.Errorf("vcs = %+v, want the configured values despite the bad toolkit block", cfg.VCS)
	}
}

// Config.Validate() must surface the held toolkit error rather than silently
// accepting a block Orion refused to read.
func TestValidateReturnsErrorWhenToolkitErrIsSet(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"deploy":"/ship-it"}}}`)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() must return the toolkit error, not nil")
	}
	if !strings.Contains(err.Error(), "deploy") {
		t.Errorf("Validate() error must be the toolkit error, got: %v", err)
	}
}

// An explicit toolkit.dir overrides the derived vendor path entirely: it is
// never replaced with a computed path, regardless of what repo is declared.
func TestExplicitToolkitDirOverridesDerivedPath(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"repo":"https://github.com/github/spec-kit.git","dir":"/opt/my-own-clone"}}`)
	if cfg.Toolkit.Dir != "/opt/my-own-clone" {
		t.Errorf("toolkit.dir = %q, want the explicit path left untouched", cfg.Toolkit.Dir)
	}
}

// njagentsDefaultRepoForTest avoids importing njagents just to name its
// constant a second time in this file; toolkit_test.go already imports it,
// but this file's assertion is about the drop behaviour, not the constant's
// value, so we read it back off a config that never declared a toolkit.
func njagentsDefaultRepoForTest(t *testing.T) string {
	t.Helper()
	return Defaults().Toolkit.Repo
}
