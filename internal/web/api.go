// The snapshot endpoint (OR-62): what `GET /api/snapshot` returns.
//
// SCANNED PER REQUEST, never cached. A cached snapshot is a second copy of
// the truth the event log already holds, and the two disagree the moment one
// is stale -- the same argument model.go makes for why nothing here reads a
// clock. The cost of re-reading every workspace's log on every request is the
// price of a page that never lies about what just happened.
//
// sessions.Scan enumerates the WORKSPACES (registered or not); web.Scan turns
// one workspace's events into CARDS. Two different things named Scan in two
// packages, which is the collision flagged when this file was written -- see
// the session's own notes. Left as-is here rather than renamed in this
// change: renaming either one is a decision with its own blast radius across
// callers this ticket does not own.
package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/sessions"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func init() { HandleReadOnly("/api/snapshot", http.HandlerFunc(snapshotHandler)) }

// snapshotHandler serves the current Snapshot as JSON.
//
// A MISSING SOURCE IS NOT AN ERROR (OR-65's rule, applied here first). No
// registry and no projects directory is a fresh install, not a fault: the
// handler answers 200 with an empty session list rather than 500, because a
// browser opened against a machine that has never run anything is the most
// common case a fresh `orion web` will see.
//
// time.Now() IS CALLED HERE, AND ONLY HERE, in this whole package -- the one
// genuine exception to "nothing here reads a clock" (model.go). Liveness
// (Scan's live parameter, cards.go) is inherently relative to the instant
// asked, the same way ElapsedAt already takes an explicit now rather than
// reading one -- so the clock is read once, at the edge, and passed down as
// a value from here on, never called a second time deeper in the stack.
func snapshotHandler(w http.ResponseWriter, r *http.Request) {
	snap, err := buildSnapshot(workspace.Home(), time.Now())
	if err != nil {
		// The one failure sessions.Scan itself treats as real: a registry
		// entry bound to a workspace id that cannot be resolved safely
		// (validID). That is a config problem worth surfacing, not a page
		// this handler should paper over with an empty body.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Errors from Encode are write failures on a connection the client
	// already has trouble with -- a broken pipe, a closed tab. Nothing this
	// handler does now changes what already went to the wire.
	_ = json.NewEncoder(w).Encode(snap)
}

// buildSnapshot reads every known workspace's event log and assembles one
// Snapshot, sorted the way the grid should draw it.
//
// PER-REQUEST, PER-WORKSPACE. sessions.Scan is cheap -- a registry read and
// one directory listing -- and events.Read streams the log rather than
// holding the whole file, so re-reading N workspaces on every request is the
// deliberate cost, not an oversight to optimise away later.
//
// now IS THE ONE CLOCK READ THE WHOLE REQUEST GETS (see snapshotHandler):
// every card's live-or-stopped verdict is judged against the SAME instant,
// so two cards on one snapshot cannot disagree about what "now" was even
// though building the snapshot takes measurable time across N workspaces.
func buildSnapshot(home string, now time.Time) (Snapshot, error) {
	wss, err := sessions.Scan(home)
	if err != nil {
		return Snapshot{}, err
	}

	live, err := liveWorkKeys(home, now)
	if err != nil {
		// A session record this package cannot enumerate is not the fresh-
		// install case OR-65 protects (that is an absent sessions/ directory,
		// which Enumerate already reports as zero records, no error) -- it is
		// a real read fault, and guessing every card is live or every card is
		// stopped would silently invent an answer either way. Reported the
		// same as sessions.Scan's own failure mode, one line up.
		return Snapshot{}, err
	}

	var cards []Card
	var started time.Time
	var at time.Time
	for _, ws := range wss {
		evs, err := events.Read(events.Path(ws.Dir))
		if err != nil {
			// A workspace whose log cannot be read is exactly the case
			// model.go's own doc names for a missing file: zero cards, no
			// error. A directory left over from an aborted `orion init`, or
			// one mid-write, must not take the whole snapshot down with it.
			continue
		}
		for _, c := range Scan(evs, live) {
			cards = append(cards, c)
			if s := c.Session.Started; !s.IsZero() && (started.IsZero() || s.Before(started)) {
				started = s
			}
			if l := c.Session.Last; l.After(at) {
				at = l
			}
		}
	}

	// One order across every workspace, not per-workspace runs of Scan's own
	// per-workspace order: a reader watching the grid needs the same card to
	// land in the same place snapshot to snapshot, or the page reshuffles
	// under them on every poll -- the instability Scan's own doc warns
	// against, one level up.
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Key != cards[j].Key {
			return cards[i].Key < cards[j].Key
		}
		si, sj := cards[i].Session.Started, cards[j].Session.Started
		if !si.Equal(sj) {
			return si.Before(sj)
		}
		return cards[i].Session.Actor < cards[j].Session.Actor
	})

	return Snapshot{At: at, Started: started, Cards: cards}, nil
}
