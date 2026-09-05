package config

import "path"

// PlanExt is the suffix a plan artifact's filename carries.
//
// A constant rather than two matching string literals because the two sides
// of it never meet: the plan stage's prompt ASKS for a file with this suffix
// (internal/supervisor), and the plan gate RECOGNISES a plan by it
// (internal/hook.planExists). Change one alone and the gate stops seeing the
// file the prompt just told the agent to write -- a run that did exactly what
// it was asked, and is then refused every edit, with nothing in either place
// looking wrong on its own.
const PlanExt = ".plan.md"

// PlanPath is the repo-relative path of one task's plan artifact.
//
// The single definition of where a plan lives, so a project that moves
// paths.plans cannot move it for the prompt and not for the gate.
//
// Slash-joined rather than filepath-joined because both callers want the
// repository's spelling: this goes into prompt text a person reads and an
// agent types into a repository, where the separator is a forward slash on
// every platform.
func (c Config) PlanPath(slug string) string {
	return path.Join(c.Paths.Plans, slug+PlanExt)
}

// Where a recommendation lives before and after somebody confirms it,
// relative to the repository root. internal/decide owns the MEANING of the
// two states and re-exports these; the strings live here because this
// package is the leaf every other one may read.
//
// That is not a filing preference. internal/decide reads a Slack approval
// through internal/collect, so anything internal/collect needs from it --
// and the plan-conformance pass needs exactly these two paths (OR-158) --
// would be an import cycle. A path is not the part of internal/decide that
// carries the reasoning, so it is the part that moves.
//
// Deliberately NOT configurable, and not fields on Paths. The distinction
// between the two directories is the whole mechanism: only the confirmed one
// is in an advisor's scope or in the implementer's prompt, so a project that
// could point them at the same place could launder a recommendation nobody
// agreed to into a premise every later stage reads as settled.
const (
	PendingDir   = "docs/recommendations/pending"
	ConfirmedDir = "docs/recommendations/confirmed"
)
