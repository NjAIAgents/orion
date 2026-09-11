// The detail endpoint (OR-53): what GET /api/detail?key=..&run=.. returns.
//
// SCANNED PER REQUEST, same rule api.go states for the snapshot: a click
// into one ticket re-reads that workspace's log rather than trusting
// whatever the last snapshot poll happened to hold, so opening the detail
// panel always shows what actually happened, not a stale copy.
package web

import (
	"encoding/json"
	"net/http"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/sessions"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func init() { HandleReadOnly("/api/detail", http.HandlerFunc(detailHandler)) }

// detailHandler serves one run's Detail as JSON.
//
// key AND run are both required: a key alone is ambiguous the moment a
// ticket has been worked twice (cards.go's own reasoning for one card per
// run, not per key) -- so a request naming only a key would have to guess
// which run the caller meant, and guessing wrong shows the wrong story
// under a ticket a reader trusts.
func detailHandler(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	run := r.URL.Query().Get("run")
	if key == "" || run == "" {
		http.Error(w, "detail: both key and run are required", http.StatusBadRequest)
		return
	}

	d, err := buildDetail(workspace.Home(), key, run)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}

// buildDetail finds the workspace holding key's events and scans it for the
// named run.
//
// A KEY UNKNOWN TO EVERY WORKSPACE IS NOT AN ERROR, same posture as
// snapshotHandler's missing-registry case: it returns a zero-value Detail
// (200, empty body), not a 404, because "this ticket has no recorded run
// under that id" is a real, common state (a stale link, a run that never
// started) rather than a fault worth a 500 or the caller having to
// special-case a 404 differently from "genuinely nothing happened here".
func buildDetail(home, key, run string) (Detail, error) {
	wss, err := sessions.Scan(home)
	if err != nil {
		return Detail{}, err
	}

	for _, ws := range wss {
		evs, err := events.Read(events.Path(ws.Dir))
		if err != nil {
			continue
		}
		for _, e := range evs {
			if e.Key == key {
				// This workspace's log carries the key at all; ScanDetail
				// itself filters to the exact (key, run) pair, so a
				// workspace whose log merely MENTIONS the key but has no
				// matching run still returns a correctly-empty Detail
				// rather than this loop guessing wrong and stopping early.
				return ScanDetail(evs, key, run), nil
			}
		}
	}
	return Detail{Key: key, Run: run}, nil
}
