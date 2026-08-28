package collect

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/cost"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// reportCost posts what a ticket cost, and prints the same thing.
//
// Both sinks from ONE render call. The terminal is where the person who
// started the run is looking at the moment it lands; the ticket is where
// anyone deciding whether to keep offloading this class of work looks months
// later. Two formatters for one set of numbers drift, and then the two
// surfaces disagree about the price with nothing to say which is right.
//
// Everything here is best effort. The merge has already happened by the time
// this runs, so turning a tracker hiccup into a failed collect would report a
// successful merge as a failure -- the worse error by a distance.
func reportCost(key string, ws *workspace.Workspace, deps Deps, w io.Writer) {
	if ws == nil || deps.Jira == nil {
		return
	}
	// Posted once. `orion collect` is a poll, and a poll is run twice by
	// definition -- by a watcher tick, by a person re-running it after a
	// failure downstream. A second identical cost comment on a closed ticket
	// teaches people the comments are noise.
	if costReported(ws.Dir, key) {
		return
	}

	text := cost.Render(cost.Aggregate(cost.ReadAll(events.Path(ws.Dir)), key))
	fmt.Fprintf(w, "\n%s\n", strings.TrimRight(text, "\n"))

	if err := deps.Jira.Comment(key, actors.Comment(events.ActorOrion, text)); err != nil {
		// Deliberately NOT marked as reported: the next pass should try
		// again. A marker written on a failed post would mean the one thing
		// this feature promises -- the number is on the ticket -- silently
		// never happens.
		ui.Warn(w, "%s: merged, but the cost report could not be posted: %v", key, err)
		return
	}
	markCostReported(ws.Dir, key)
}

// The marker is one empty file per ticket rather than a field on any existing
// state, because the state this would otherwise live in (the fix history) is
// CLEARED on merge, a few lines before the report is posted.
func costMarker(wsDir, key string) string {
	return filepath.Join(wsDir, ".orion", "cost-reported", strings.ToUpper(key))
}

func costReported(wsDir, key string) bool {
	_, err := os.Stat(costMarker(wsDir, key))
	return err == nil
}

func markCostReported(wsDir, key string) {
	p := costMarker(wsDir, key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, nil, 0o600)
}
