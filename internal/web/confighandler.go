// The config endpoint (OR-54): what GET /api/config returns.
//
// THE ROSTER ONLY, DELIBERATELY. Roster (OR-80, agents.go) already resolves
// actors.Roster against the global agents.json -- one file per machine, the
// same call `orion config agents --list` makes, so the page and the terminal
// listing cannot disagree. This file adds only the route and envelope.
//
// LIMITS FROM orion.json ARE NOT SERVED HERE. config.Load(root) reads one
// PROJECT's orion.json, but this surface spans every workspace under
// ORION_HOME with no single project selected -- there is no "the" orion.json
// to read the way there is one agents.json. Serving limits would mean either
// guessing a project (wrong the moment more than one exists) or inventing a
// project-selection query parameter no other endpoint in this package has.
// Left for the ticket that actually threads a project id through requests,
// rather than guessed at here.
package web

import (
	"encoding/json"
	"net/http"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func init() { HandleReadOnly("/api/config", http.HandlerFunc(configHandler)) }

// ConfigView is the config page's model: the roster (OR-80), resolved fresh
// from the same source the CLI itself reads.
type ConfigView struct {
	Roster []actors.RosterEntry
}

// configHandler serves the current ConfigView as JSON.
func configHandler(w http.ResponseWriter, r *http.Request) {
	roster, err := Roster(workspace.Home())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ConfigView{Roster: roster})
}
