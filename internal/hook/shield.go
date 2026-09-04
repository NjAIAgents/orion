package hook

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/match"
)

// FixModeMarker is written by `orion fix start` and removed by
// `orion fix end`. Its presence means a bug fix is in progress and the
// failing test that defines "fixed" is off limits.
const FixModeMarker = "fix-mode"

// Shield guards file writes. Three controls, each backing a policy that
// a skill can only advise on:
//
//  1. Protected paths: CI config, managed settings and orion.json itself.
//     An agent that can edit its own guardrails has none.
//  2. Test protection during a fix: the playbook's rule that an agent
//     fixing code must not be able to weaken the check on that code.
//  3. Plan gate: no implementation before an approved plan exists.
//
// Wired to PreToolUse on Edit, Write, MultiEdit and NotebookEdit.
func Shield(in Input, cfg config.Config) Decision {
	if in.HookEventName != "PreToolUse" {
		return Allow("")
	}
	target := in.FilePath()
	if target == "" {
		return Allow("")
	}

	rel := relToRoot(target, cfg.Root)

	// 1. Protected paths.
	if match.MatchAny(cfg.Paths.Protected, rel) {
		return Block("shield: %s is a protected path.\n"+
			"  Orion cannot edit its own controls, CI configuration or managed settings.\n"+
			"  If this change is genuinely needed, a human edits it and reviews the diff.",
			rel)
	}

	// 2. Test files during a bug fix.
	if cfg.Gates.ProtectTestsDuringFix && fixModeActive(cfg) {
		if match.MatchAny(cfg.Paths.TestGlobs, rel) {
			return Block("shield: %s is a test file and a fix is in progress.\n"+
				"  The failing test defines what \"fixed\" means. Changing it moves the goalposts.\n"+
				"  Fix the code so the test passes as written.\n"+
				"  If the test itself is genuinely wrong, stop and say so; a human decides that.",
				rel)
		}
	}

	// 3. Plan gate. Artifacts themselves are always writable, otherwise
	// there would be no way to produce the plan the gate demands.
	if cfg.Gates.RequirePlanBeforeEdit && !isArtifact(rel, cfg) && !planExists(cfg) {
		return Block("shield: no approved plan found in %s/.\n"+
			"  Nothing gets implemented before a written plan exists.\n"+
			"  Run /orion:plan to produce and commit one, then implement against it.",
			cfg.Paths.Plans)
	}

	return Allow("")
}

func relToRoot(target, root string) string {
	abs := target
	if !filepath.IsAbs(abs) && root != "" {
		abs = filepath.Join(root, target)
	}
	if root != "" {
		if r, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(target)
}

func fixModeActive(cfg config.Config) bool {
	_, err := os.Stat(filepath.Join(cfg.StateDir(), FixModeMarker))
	return err == nil
}

// isArtifact reports whether the path is part of the artifact chain,
// which must stay writable for the chain to advance.
func isArtifact(rel string, cfg config.Config) bool {
	for _, dir := range []string{cfg.Paths.Intent, cfg.Paths.Specs, cfg.Paths.Plans, cfg.Paths.Evals} {
		if dir == "" {
			continue
		}
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	// CLAUDE.md is institutional knowledge and is meant to be corrected
	// the moment a mistake repeats. Gating it behind a plan would kill
	// the two-strike rule.
	return rel == "CLAUDE.md"
}

// planExists reports whether the configured plans directory holds a plan.
//
// Any plan, not a named one: the shield sees a file path, never a task, so
// the question it can answer is whether this project has produced a plan at
// all. The directory and the suffix both come from config -- the same helper
// the plan stage's prompt names the file with -- so the gate cannot end up
// looking for something the prompt never asked for.
func planExists(cfg config.Config) bool {
	dir := filepath.Join(cfg.Root, cfg.Paths.Plans)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), config.PlanExt) {
			return true
		}
	}
	return false
}
