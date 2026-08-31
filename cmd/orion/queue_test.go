package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/tracker"
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
			// Built from Managed() rather than spelled out. The set grows --
			// orion-ready arrived with OR-253 -- and a literal here fails on
			// the ADDITION of a label rather than on the thing this test is
			// about, which is that every managed label is present and each
			// one is quoted.
			if !strings.Contains(jql, wantLabelsClause()) {
				t.Errorf("labels clause lost or unquoted: %s", jql)
			}
			if !strings.HasSuffix(jql, " ORDER BY priority DESC, Rank ASC") {
				t.Errorf("order clause lost: %s", jql)
			}
		})
	}
}

// wantLabelsClause is the labels clause the queue must build, derived from the
// one list that owns which labels Orion manages.
//
// Derived rather than duplicated: a second hand-written copy of that list is
// a copy that goes stale silently, and the failure it produces names the
// wrong thing -- a test about quoting failing because a label was added
// somewhere else entirely.
func wantLabelsClause() string {
	quoted := make([]string, 0, 5)
	for _, l := range tracker.Managed("ORION") {
		quoted = append(quoted, `"`+l+`"`)
	}
	return "labels IN (" + strings.Join(quoted, ", ") + ")"
}

// No project key means no project clause -- and no dangling AND.
func TestQueueJQLWithoutAProjectKey(t *testing.T) {
	cfg := config.Config{}
	cfg.Tracker.QueueLabel = "ORION"

	if got, want := queueJQL(cfg), wantLabelsClause(); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
