package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
)

// The ticket's own acceptance criterion: "Events.jsonl shows individual
// explore result events for each question, each with its own answer/paths
// /error, not collapsed into one batch event." A question that FAILS is a
// question too -- runExplore's per-question loop only calls logExplore when
// q.Err == nil, so a failed question in a batch leaves no trace in
// events.jsonl at all: not its own event, not folded into another one.
// Nothing (see logExploreDispatchRecordsTheFanOut) records which of the
// dispatched questions is the one that came back empty.
func TestEventsRecordEachFailedQuestionToo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	wsDir := filepath.Join(home, "projects", "t-1")
	if err := os.MkdirAll(filepath.Join(wsDir, ".orion"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, ".orion", "task.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORION_WORKSPACE", "t-1")

	repo := t.TempDir()
	if err := runGit(t, repo, "init"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(t, repo, "commit", "--allow-empty", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	claudePerQuestion(t, map[string]string{
		"event log": "appended by events.Log\\nPATHS: internal/events/events.go",
	})

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	oldStdout := os.Stdout
	os.Stdout = devNull
	t.Cleanup(func() { os.Stdout = oldStdout })

	runExplore([]string{"--repo", repo,
		"where is the event log written?",
		"a question nothing will answer",
	})
	os.Stdout = oldStdout

	raw, err := events.Read(events.Path(wsDir))
	if err != nil {
		t.Fatal(err)
	}
	var sawFailure bool
	for _, e := range raw {
		if e.Actor != events.ActorExplore {
			continue
		}
		if q, _ := e.Detail["question"].(string); q == "a question nothing will answer" {
			sawFailure = true
			if e.Detail["error"] == nil {
				t.Errorf("event for the failed question has no error detail: %+v", e.Detail)
			}
		}
	}
	if !sawFailure {
		t.Error("the failed question left no event at all in events.jsonl -- a batch's failure " +
			"is invisible afterwards, indistinguishable from a question that was never asked")
	}
}
