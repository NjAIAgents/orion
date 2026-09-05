package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The point of OR-143 is that the triage subagent is CHEAP and separately
// attributed. If it inherits the fix run's model, or its cost lands under
// another actor, the split has added a second run and saved nothing.
func TestTriageOptionsRunAsItsOwnActorOnItsOwnModel(t *testing.T) {
	o := triageOptions("OR-1", "orion/or-1", "boom at main.go:12")

	if o.Actor != events.ActorLogTriage {
		t.Errorf("Actor = %q, want %q so its spend is its own row in the cost report",
			o.Actor, events.ActorLogTriage)
	}
	if o.Key != "OR-1" {
		t.Errorf("Key = %q, want the ticket so the cost lands on that ticket", o.Key)
	}
	if want := actors.Model(events.ActorLogTriage); o.Model != want {
		t.Errorf("Model = %q, want the roster's %q -- pinning it cheap is the whole cost win",
			o.Model, want)
	}
	if o.MaxMinutes != triageMaxMinutes || o.MaxTurns != triageMaxTurns {
		t.Errorf("bounds = %d min / %d turns, want the tight triage bounds %d/%d",
			o.MaxMinutes, o.MaxTurns, triageMaxMinutes, triageMaxTurns)
	}
	if !strings.Contains(o.Prompt, "orion/or-1") || !strings.Contains(o.Prompt, "boom at main.go:12") {
		t.Errorf("prompt must carry the branch and the log it is triaging, got: %q", o.Prompt)
	}
}

// triageWS builds the smallest workspace supervisor.Run will accept.
func triageWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	dir := filepath.Join(home, "projects", "t-1")
	for _, d := range []string{"repo", ".orion/logs", ".orion/state"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &workspace.Workspace{ID: "t-1", Dir: dir, RepoPath: filepath.Join(dir, "repo")}
}

// claudeSaying puts a `claude` on PATH that returns final as its result.
func claudeSaying(t *testing.T, final string, exit int) {
	t.Helper()
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","session_id":"s","result":"` + final + `","total_cost_usd":0.01,"is_error":false}'` + "\n" +
		"exit " + itoaTest(exit) + "\n"
	writeFakeBin(t, "claude", script)
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	return "1"
}

// The success path: the fix run must receive the REPORT, never the raw log.
// That substitution is the entire ticket -- the log is what was riding along
// on every turn of the fix run.
func TestTriageLogReturnsTheReportAndRecordsIt(t *testing.T) {
	ws := triageWS(t)
	claudeSaying(t, "gofmt gate failed on work_test.go", 0)

	raw := strings.Repeat("noise\n", 500) + "FAIL\n"
	got := triageLog(ws, "OR-1", "orion/or-1", raw)

	if got != "gofmt gate failed on work_test.go" {
		t.Fatalf("triageLog = %q, want the subagent's report, not the raw log", got)
	}

	evs, err := events.Read(events.Path(ws.Dir))
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, e := range evs {
		if e.Actor == events.ActorLogTriage && strings.Contains(e.Msg, "gofmt gate failed") {
			saw = true
		}
	}
	if !saw {
		t.Error("the report was never written to the event log; what a subagent returns " +
			"is all the parent ever sees of it, so an unrecorded answer is lost (OR-129)")
	}
}

// The fallback: a triage step that silently produced nothing must not leave
// the fix run with nothing to react to. A raw log is worse than a report and
// far better than an empty string.
func TestTriageLogFallsBackToTheRawLog(t *testing.T) {
	ws := triageWS(t)
	claudeSaying(t, "", 1)

	raw := "FAIL\tgithub.com/x/y\t0.1s\n"
	if got := triageLog(ws, "OR-1", "orion/or-1", raw); got != raw {
		t.Errorf("triageLog = %q, want the raw log unchanged when the subagent fails", got)
	}
}
