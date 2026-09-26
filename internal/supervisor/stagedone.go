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
	"os/exec"
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
		// heal:false -- this is a read-only resume check, possibly asked many
		// times, and answering it must never have the side effect of writing
		// a commit (OR-441).
		if _, err := checkStageArtifact(ws.RepoDir(), cfg, stage, ws.Task.Slug, false); err != nil {
			// The scaffold stage commits on a feature branch, and the sandbox
			// goes back to the work branch before its pull request merges
			// (OR-482). Reading only the checked-out branch then called the
			// scaffold missing, and a resumed chain scaffolded again (OR-483).
			if strings.EqualFold(strings.TrimSpace(stage), "scaffold") &&
				artifactOnPrefixedBranch(ws.RepoDir(), cfg.VCS.BranchPrefix, rel) {
				return true
			}
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
		case "plan":
			// Same rule again, one stage later (OR-445): a plan with its own
			// Open questions left is not done, or the resume would skip it
			// only to stop at whichever stage reads the plan next.
			return discovery.Assess(filepath.Join(ws.RepoDir(), filepath.FromSlash(rel))).Ready()
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

// artifactOnPrefixedBranch reports whether any local branch under prefix
// carries a non-empty copy of rel. Local branches only: this answers "has the
// stage produced its work", not "has it reached the remote" -- that is the
// scaffold-publish step's job, and it checks for itself.
func artifactOnPrefixedBranch(repo, prefix, rel string) bool {
	if strings.TrimSpace(prefix) == "" {
		return false
	}
	out, err := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+prefix).Output()
	if err != nil {
		return false
	}
	for _, br := range strings.Fields(string(out)) {
		size, err := exec.Command("git", "-C", repo, "cat-file", "-s", br+":"+rel).Output()
		if err == nil && strings.TrimSpace(string(size)) != "0" {
			return true
		}
	}
	return false
}
