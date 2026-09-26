package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

func mkHistoryWorkspace(t *testing.T, home, id, key string, task *workspace.Task) string {
	t.Helper()
	dir := mkTestWorkspace(t, home, id)
	if task == nil {
		return dir
	}
	metaDir := filepath.Join(dir, ".orion")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	task.ID = id
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "task.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Given the history page, Then past runs are listed with stage, start time,
// duration, exit code and stop reason from task.json's Runs, newest first.
func TestHistoryHandlerListsRunsNewestFirst(t *testing.T) {
	home := t.TempDir()
	mkHistoryWorkspace(t, home, "ws-a", "OR-1", &workspace.Task{
		Runs: []workspace.RunRec{
			{Stage: "implement", StartedAt: t0(1), Seconds: 120, ExitCode: 0, Reason: "done"},
			{Stage: "fix", StartedAt: t0(2), Seconds: 30, ExitCode: 1, Reason: "ci failed"},
		},
	})
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	historyHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var rows []HistoryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if len(rows[0].Runs) != 2 {
		t.Fatalf("Runs = %d, want 2", len(rows[0].Runs))
	}
	if rows[0].Runs[0].Stage != "fix" {
		t.Errorf("Runs[0].Stage = %q, want %q (newest first)", rows[0].Runs[0].Stage, "fix")
	}
	if rows[0].Runs[1].Stage != "implement" {
		t.Errorf("Runs[1].Stage = %q, want %q", rows[0].Runs[1].Stage, "implement")
	}
}

// Given a workspace with an unreadable task.json, Then it is listed as
// unreadable rather than omitted.
func TestHistoryHandlerListsAnUnreadableWorkspaceRatherThanOmittingIt(t *testing.T) {
	home := t.TempDir()
	dir := mkTestWorkspace(t, home, "ws-broken")
	metaDir := filepath.Join(dir, ".orion")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "task.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	historyHandler(rec, req)

	var rows []HistoryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (the broken workspace must still appear)", len(rows))
	}
	if !rows[0].Unreadable {
		t.Error("Unreadable = false, want true")
	}
	if rows[0].Runs != nil {
		t.Errorf("Runs = %+v, want nil for an unreadable workspace", rows[0].Runs)
	}
}

// A workspace directory that exists but has no task.json at all (mid-init,
// or an aborted clone) is unreadable too, not a crash.
func TestHistoryHandlerListsAMissingTaskJSONAsUnreadable(t *testing.T) {
	home := t.TempDir()
	mkTestWorkspace(t, home, "ws-empty") // directory only, no .orion/task.json
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	historyHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var rows []HistoryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Unreadable {
		t.Errorf("rows = %+v, want one unreadable row", rows)
	}
}

// A fresh install with no projects at all serves 200 with an empty list,
// the same posture every other endpoint in this package takes.
func TestHistoryHandlerOnAFreshInstallIs200(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	historyHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on a fresh install", rec.Code)
	}
}
