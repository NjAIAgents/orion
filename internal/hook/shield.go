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

// planExists reports whether this project has produced a plan.
//
// Any plan, not a named one: the shield sees a file path, never a task, so
// the question it can answer is whether this project has produced a plan at
// all.
//
// TWO LAYOUTS, THE SAME QUESTION. A built-in plan stage writes
// plans/<slug>.plan.md. A DELEGATED one writes into spec-kit's feature
// directory as specs/NNN-<slug>/plan.md, and leaves at most a pointer in
// plans/ -- so a gate that knew only the first layout said "no approved
// plan" to a project holding a finished plan, and refused every write of
// the stage that came next. FOUND ON A REAL PROJECT: scaffold spent six
// minutes being refused its own README and reverse-engineering this
// function out of the orion binary with `strings`.
func planExists(cfg config.Config) bool {
	if anyPlanIn(filepath.Join(cfg.Root, cfg.Paths.Plans), config.PlanExt) {
		return true
	}
	// The delegated layout: any specs/*/plan.md.
	specs := filepath.Join(cfg.Root, cfg.Paths.Specs)
	entries, err := os.ReadDir(specs)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if st, err := os.Stat(filepath.Join(specs, e.Name(), "plan.md")); err == nil && st.Size() > 0 {
			return true
		}
	}
	return false
}

func anyPlanIn(dir, ext string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ext) {
			return true
		}
	}
	return false
}
