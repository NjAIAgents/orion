package main

// OR-491: a project created by orion plan starts with Slack on when it has a
// channel, and with batch integration on.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

func projectConfig(t *testing.T, w *workspace.Workspace) (slackOn bool, batch bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.RepoDir(), "orion.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Slack   struct{ Enabled bool } `json:"slack"`
		Collect struct {
			BatchIntegration bool `json:"batch_integration"`
		} `json:"collect"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c.Slack.Enabled, c.Collect.BatchIntegration
}

func TestANewProjectWithAChannelHasSlackAndBatchingOn(t *testing.T) {
	w := chainWSWithRepo(t)
	w.Task.Slack = &workspace.SlackChannel{ID: "C1", Name: "orion-thing"}
	var out strings.Builder
	if err := toolkitStep(&stepIO{Out: &out}, w); err != nil {
		t.Fatal(err)
	}
	slackOn, batch := projectConfig(t, w)
	if !slackOn {
		t.Error("the project has a channel but slack.enabled is false")
	}
	if !batch {
		t.Error("a new project's orion.json does not turn on collect.batch_integration")
	}
}

func TestANewProjectWithoutAChannelSaysSoAndLeavesSlackOff(t *testing.T) {
	w := chainWSWithRepo(t)
	var out strings.Builder
	if err := toolkitStep(&stepIO{Out: &out}, w); err != nil {
		t.Fatal(err)
	}
	if slackOn, _ := projectConfig(t, w); slackOn {
		t.Error("slack turned on with no channel to post to")
	}
	if !strings.Contains(out.String(), "no project Slack channel") {
		t.Errorf("a missing channel is not said in the plan output:\n%s", out.String())
	}
}

// An orion.json that already exists is somebody's choice: batching is not
// switched on behind their back.
func TestAnExistingOrionJSONKeepsItsBatchSetting(t *testing.T) {
	w := chainWSWithRepo(t)
	path := filepath.Join(w.RepoDir(), "orion.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "collect": {"batch_integration": false}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := toolkitStep(&stepIO{Out: &out}, w); err != nil {
		t.Fatal(err)
	}
	if _, batch := projectConfig(t, w); batch {
		t.Error("batch integration was turned on in an orion.json that already existed")
	}
}
