package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// `orion queue` on a project whose key is a JQL reserved word.
//
// OR-120: the key was interpolated bare, so `project = OR AND labels in ...`
// was rejected by Jira as a syntax error and the command was unusable on
// exactly the project Orion itself is tracked in.
func TestQueueJQLQuotesReservedProjectKeys(t *testing.T) {
	for _, key := range []string{"OR", "AND", "ORDER"} {
		t.Run(key, func(t *testing.T) {
			cfg := config.Config{}
			cfg.Tracker.ProjectKey = key
			cfg.Tracker.QueueLabel = "ORION"
			cfg.Tracker.QueueOrder = "priority DESC, Rank ASC"

			jql := queueJQL(cfg)

			if want := `project = "` + key + `"`; !strings.HasPrefix(jql, want) {
				t.Errorf("got %s, want it to start with %s", jql, want)
			}
			if strings.Contains(jql, "project = "+key) {
				t.Errorf("project key is interpolated unquoted: %s", jql)
			}
			if !strings.Contains(jql, `labels IN ("ORION", "orion-working", "orion-ci-wait", "orion-failed")`) {
				t.Errorf("labels clause lost or unquoted: %s", jql)
			}
			if !strings.HasSuffix(jql, " ORDER BY priority DESC, Rank ASC") {
				t.Errorf("order clause lost: %s", jql)
			}
		})
	}
}

// No project key means no project clause -- and no dangling AND.
func TestQueueJQLWithoutAProjectKey(t *testing.T) {
	cfg := config.Config{}
	cfg.Tracker.QueueLabel = "ORION"

	if got, want := queueJQL(cfg),
		`labels IN ("ORION", "orion-working", "orion-ci-wait", "orion-failed")`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
