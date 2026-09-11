// The history endpoint (OR-54): what GET /api/history returns.
//
// PAST RUNS FROM task.json's OWN Runs FIELD, never re-derived from the
// event log. workspace.RunRec already carries exactly what the ticket asks
// for -- stage, start time, duration, exit code, stop reason -- because the
// supervisor writes one every time a run ends (internal/supervisor). A
// second derivation from events.jsonl would be the same duplication api.go
// and agents.go both refuse elsewhere in this package: two sources for one
// question, disagreeing the moment either drifts.
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/orion-sdlc/orion/internal/sessions"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func init() { HandleReadOnly("/api/history", http.HandlerFunc(historyHandler)) }

// HistoryRow is one workspace's history: its runs, or why they could not be
// read.
//
// UNREADABLE IS ITS OWN STATE, NOT AN OMISSION (the ticket's own acceptance
// criterion): a workspace whose task.json cannot be parsed appears with
// Unreadable set and Runs nil, rather than silently missing from the list --
// a missing row and a broken one look identical to a reader, and only one
// of them is safe to ignore.
type HistoryRow struct {
	Key         string
	WorkspaceID string
	Runs        []workspace.RunRec
	Unreadable  bool
}

// historyHandler serves every known workspace's run history as JSON, newest
// run first within each workspace.
func historyHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := buildHistory(workspace.Home())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

// buildHistory reads every workspace sessions.Scan knows about and returns
// one HistoryRow per workspace, each with its runs newest-first.
func buildHistory(home string) ([]HistoryRow, error) {
	wss, err := sessions.Scan(home)
	if err != nil {
		return nil, err
	}

	rows := make([]HistoryRow, 0, len(wss))
	for _, ws := range wss {
		row := HistoryRow{Key: ws.Key, WorkspaceID: ws.ID}

		b, err := os.ReadFile(taskPath(ws.Dir))
		if err != nil {
			row.Unreadable = true
			rows = append(rows, row)
			continue
		}
		var t workspace.Task
		if err := json.Unmarshal(b, &t); err != nil {
			row.Unreadable = true
			rows = append(rows, row)
			continue
		}

		row.Runs = make([]workspace.RunRec, len(t.Runs))
		copy(row.Runs, t.Runs)
		// Newest first: task.json appends in the order runs happened, and a
		// reader wants the most recent attempt at the top, matching every
		// other newest-first listing in this package.
		sort.SliceStable(row.Runs, func(i, j int) bool {
			return row.Runs[i].StartedAt.After(row.Runs[j].StartedAt)
		})
		rows = append(rows, row)
	}
	return rows, nil
}

// taskPath mirrors workspace.Workspace.TaskPath's own construction
// (MetaDir()/task.json) without needing a *Workspace built through
// workspace.Open's id-resolution path -- buildHistory already has the
// directory straight from sessions.Scan, and Open would just re-derive the
// same path from an id this function does not have.
func taskPath(wsDir string) string {
	return filepath.Join(wsDir, ".orion", "task.json")
}
