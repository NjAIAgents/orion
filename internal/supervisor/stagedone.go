package supervisor

// StageDone answers "is this stage's work already there", which is what a
// resumed `orion plan` asks of every step before deciding whether to run it.
//
// DERIVED FROM THE ARTIFACT, NOT FROM A FLAG. A stage that owes a file is
// done when that file passes the same artifact gate a fresh run of the stage
// would have to pass: present, non-empty, tracked, and not declaring itself
// blocked. A recorded "done" bit could say yes about a file somebody has
// since deleted or never committed; the file cannot. Intent additionally
// has to have no open questions, because the next stage's discovery gate
// would refuse it anyway, and a resume that skipped intent only to stop at
// spec's gate would be a resume that lied about where it was.
//
// A stage that owes no file -- scaffold, decompose, the work stages -- is
// done when its last recorded run completed. That is the one place a record
// stands in for an artifact, and it is the record the supervisor already
// writes for every run, not a new one.

import (
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/discovery"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// StageDone reports whether the stage's work is already there.
//
// An unknown stage is never done: it has no artifact to check and no run to
// find, and "done" is the answer that skips work, so it is the one that must
// not be given by accident.
func StageDone(ws *workspace.Workspace, stage string) bool {
	if !config.KnownStage(stage) {
		return false
	}
	cfg := config.Load(ws.RepoDir())
	if rel := stageArtifact(cfg, stage, ws.Task.Slug); rel != "" {
		if checkStageArtifact(ws.RepoDir(), cfg, stage, ws.Task.Slug) != nil {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(stage)) {
		case "intent":
			return discovery.Assess(filepath.Join(ws.RepoDir(), filepath.FromSlash(rel))).Ready()
		case "spec", "design":
			// The same rule the next stage's gate applies: a spec with a
			// marker left in it is not done, or the resume would skip it
			// only to stop at plan.
			return discovery.AssessSpec(filepath.Join(ws.RepoDir(), filepath.FromSlash(rel))).Ready()
		}
		return true
	}
	// Last run of this stage, because a stage that failed and was then
	// re-run successfully is done, and one that succeeded and was then
	// re-run and failed is not.
	for i := len(ws.Task.Runs) - 1; i >= 0; i-- {
		r := ws.Task.Runs[i]
		if strings.EqualFold(r.Stage, stage) {
			return r.ExitCode == 0 && r.Reason == "completed"
		}
	}
	return false
}
