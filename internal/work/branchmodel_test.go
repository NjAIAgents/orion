package work

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// A run under a collapsed branch model bases its branch on the release
// branch and merges back into it, so there is nothing about it to salvage:
// refuse before an agent is spawned rather than after the merge lands.
func TestARunIsRefusedWhenWorkBranchIsTheReleaseBranch(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"main","branch_prefix":"orion/"},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"}}`)
	j := &fakeJira{}
	var out strings.Builder
	agentRan := false

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				agentRan = true
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push:   func(dir, branch string) error { return nil },
			OpenPR: func(dir, branch, title, body, base string) (string, error) { return "https://pr/1", nil },
		})

	if agentRan {
		t.Fatal("no agent may run against a branch model that merges into the release branch")
	}
	if len(res) != 1 || res[0].Err == nil {
		t.Fatalf("the run must fail, got %+v", res)
	}
	if !strings.Contains(res[0].Err.Error(), "work_branch") {
		t.Errorf("the failure must name the setting: %v", res[0].Err)
	}
	if !strings.Contains(out.String(), "orion init") {
		t.Errorf("the operator must be told the remedy:\n%s", out.String())
	}
}

// The named opt-in still runs -- and says on every run what it gave up.
func TestTheOverrideRunsButAnnouncesTheWaivedProtection(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"main","branch_prefix":"orion/",
	                            "allow_release_branch_merges":true},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"}}`)
	j := &fakeJira{}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home, DryRun: true},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
		})

	if !strings.Contains(out.String(), "allow_release_branch_merges") ||
		!strings.Contains(out.String(), "no human promotion step") {
		t.Errorf("the waived protection must be stated on every run:\n%s", out.String())
	}
}
