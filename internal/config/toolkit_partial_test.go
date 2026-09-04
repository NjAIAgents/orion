package config

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/toolkit"
)

// A toolkit block with only repo set must fill ref/dir/stages exactly as an
// absent block would -- repo alone declaring a toolkit must not also require
// spelling out the rest.
func TestToolkitWithOnlyRepoFillsTheRestFromDefaults(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"repo":"https://example.com/kit.git"}}`)
	if cfg.Toolkit.Repo != "https://example.com/kit.git" {
		t.Errorf("repo = %q", cfg.Toolkit.Repo)
	}
	if cfg.Toolkit.Ref != "" {
		t.Errorf("ref = %q, want default empty", cfg.Toolkit.Ref)
	}
	if cfg.Toolkit.Dir != "" {
		t.Errorf("dir = %q, want default empty", cfg.Toolkit.Dir)
	}
	if len(cfg.Toolkit.Stages) != 0 {
		t.Errorf("stages = %v, want empty", cfg.Toolkit.Stages)
	}
}

// A toolkit block with only stages set must fill repo from the nj-agents
// default and leave ref/dir empty, the same as an absent block.
func TestToolkitWithOnlyStagesFillsTheRestFromDefaults(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"review":"/analyze"}}}`)
	if cfg.Toolkit.Repo != toolkit.RepoURL {
		t.Errorf("repo = %q, want the nj-agents default %q", cfg.Toolkit.Repo, toolkit.RepoURL)
	}
	if cfg.Toolkit.Ref != "" {
		t.Errorf("ref = %q, want default empty", cfg.Toolkit.Ref)
	}
	if cfg.Toolkit.Dir != "" {
		t.Errorf("dir = %q, want default empty", cfg.Toolkit.Dir)
	}
	if cfg.Toolkit.Stage("review") != "/analyze" {
		t.Errorf("review = %q", cfg.Toolkit.Stage("review"))
	}
}

// A config with no toolkit block at all must still load: Load never returns
// an error, and Validate must not report one either.
func TestNoToolkitBlockLoadsWithoutError(t *testing.T) {
	cfg := loadJSON(t, `{"version":1}`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("absent toolkit block must not fail validation: %v", err)
	}
}

// A stage name is trimmed before it is looked up, so a value copied out of a
// prompt or a config generator with stray whitespace still resolves.
func TestStageNameWhitespaceIsTrimmed(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"review":"/analyze"}}}`)
	for _, name := range []string{" review", "review ", "  review  "} {
		if got := cfg.Toolkit.Stage(name); got != "/analyze" {
			t.Errorf("stage %q = %q, want %q", name, got, "/analyze")
		}
	}
}

// Stage lookups are case-insensitive: the declared key and the queried name
// need not agree on casing.
func TestStageNameCaseIsNormalized(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"spec":"/plan"}}}`)
	for _, name := range []string{"spec", "Spec", "SPEC"} {
		if got := cfg.Toolkit.Stage(name); got != "/plan" {
			t.Errorf("stage %q = %q, want %q", name, got, "/plan")
		}
	}
}

// Config blocks that predate the toolkit field -- limits and vcs alone --
// must produce the same effective toolkit as before this change existed: the
// nj-agents default repo, and none of the toolkit fields disturbed by a
// sibling block being present.
func TestPreExistingConfigFixturesStillLoadTheirOldEffectiveValues(t *testing.T) {
	cfg := loadJSON(t, `{"limits":{"max_tool_calls":123},"vcs":{"default_branch":"main","work_branch":"develop"}}`)
	if cfg.Limits.MaxToolCalls != 123 {
		t.Errorf("max_tool_calls = %d, want 123", cfg.Limits.MaxToolCalls)
	}
	if cfg.VCS.DefaultBranch != "main" || cfg.VCS.WorkBranch != "develop" {
		t.Errorf("vcs = %+v", cfg.VCS)
	}
	if cfg.Toolkit.Repo != toolkit.RepoURL {
		t.Errorf("toolkit.repo = %q, want the untouched nj-agents default %q", cfg.Toolkit.Repo, toolkit.RepoURL)
	}
	if cfg.Toolkit.Dir != "" || cfg.Toolkit.Ref != "" || len(cfg.Toolkit.Stages) != 0 {
		t.Errorf("toolkit = %+v, want the untouched zero value", cfg.Toolkit)
	}
	if cfg.ToolkitWarning != "" {
		t.Errorf("toolkit warning = %q, want none", cfg.ToolkitWarning)
	}
}
