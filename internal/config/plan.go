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

// IntentPath is the repo-relative path of one task's intent artifact.
//
// Here for the same reason PlanPath is, and after the same failure. The
// intent stage's prompt said "writes docs/intent/<slug>.md" without ever
// saying what the slug WAS, so an agent invented a descriptive filename --
// docs/intent/cloudhealth-replacement-aws-cost-tool.md -- while the artifact
// check looked for docs/intent/cloudlens.md and reported that the stage had
// written nothing. It had written a good file at a name nobody would read.
//
// One definition, so the prompt states the path the check demands.
func (c Config) IntentPath(slug string) string {
	return path.Join(c.Paths.Intent, slug+".md")
}

// FeatureDir is the repo-relative directory a project's feature artifacts
// live in under spec-kit: spec.md, plan.md, tasks.md and the rest of what
// its commands write.
//
// ONE SPELLING. The prompt that tells the agent where to write, the artifact
// gate that checks it wrote there, the discovery gate that reads the spec's
// open questions, the environment that tells spec-kit's own scripts which
// feature is active, and the decompose step that reads tasks.md all need
// this path; four spellings of it is how the spec gate came to look in the
// wrong place while the prompt named the right one.
//
// 001 is pinned: the chain plans one feature per tracker project, and the
// slug is the one canonical name (docs/decisions/0009). spec-kit would
// otherwise derive its own short name from the description and number it
// itself -- a second name for the same work, recorded in a gitignored file
// (docs/decisions/0022). A second feature on an existing project needs a
// counter and an entry point; both are out of scope
// (docs/decisions/0023).
func (c Config) FeatureDir(slug string) string {
	return path.Join(c.Paths.Specs, "001-"+slug)
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
