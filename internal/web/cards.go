package web

// Cards is where the event log becomes a grid (OR-57). model.go says what a
// card HOLDS and timing.go says when a run started and whether it is over;
// this file makes the decision the rest of the epic inherits -- WHAT A CARD IS
// ONE OF.
//
// ONE CARD PER (KEY, RUN), NOT PER KEY. A ticket is worked more than once: an
// implementer runs, CI fails, devops runs again on the same key. Grouping by
// key alone folds those into one card whose step count is the sum of two runs
// and whose "started" is the first one -- a card that describes no run that
// ever happened. Grouping by run alone loses the ticket, which is the axis the
// grid is drawn on and coloured by. So both, together, and a key with two runs
// gets two cards.
//
// A CARD FROM HERE IS THE PROGRESS HALF, NOT A FINISHED CARD. Key, and the
// session's timing, step count, activity and model come off the log. Title
// comes from the tracker, and Verb and Gate are an outcome judgement -- none of
// the three is in the event stream this reads, and each is its own ticket. They
// are left zero rather than guessed, because a run with a failed event rendered
// "working" by a default is worse than one rendered blank.
//
// NEWEST BY TIMESTAMP, NOT BY FILE POSITION, for the same reason Timing takes
// the minimum and maximum rather than the first and last line: a log is a file,
// and file order is not evidence of time order. Equal timestamps fall back to
// file order, which is what makes a log written without timestamps still yield
// its last word rather than an arbitrary one.

import (
	"sort"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// Scan groups a log into cards: one per (key, run) pair, ordered by key, then
// by when the run started, then by run id.
//
// A TOTAL ORDER, not the order the lines happened to arrive in, because the
// surface reading this draws a grid and a grid that reshuffles between two
// identical scans cannot be diffed -- the same rule sessions.Scan follows for
// the same reason.
//
// An event with no key is skipped. It is real -- supervisor lines are emitted
// before any ticket is claimed -- but there is no ticket to draw it on, and a
// card keyed on the empty string is a card nobody can click.
func Scan(evs []events.Event) []Card {
	type runID struct{ key, run string }

	groups := map[runID][]events.Event{}
	var ids []runID
	for _, e := range evs {
		if e.Key == "" {
			continue
		}
		id := runID{key: e.Key, run: e.Run}
		if _, seen := groups[id]; !seen {
			ids = append(ids, id)
		}
		groups[id] = append(groups[id], e)
	}

	sessions := make(map[runID]Session, len(groups))
	for id, g := range groups {
		sessions[id] = sessionOf(g)
	}

	sort.Slice(ids, func(a, b int) bool {
		x, y := ids[a], ids[b]
		if x.key != y.key {
			return x.key < y.key
		}
		sx, sy := sessions[x].Started, sessions[y].Started
		if !sx.Equal(sy) {
			return sx.Before(sy)
		}
		return x.run < y.run
	})

	out := make([]Card, 0, len(ids))
	for _, id := range ids {
		out = append(out, Card{Key: id.key, Session: sessions[id]})
	}
	return out
}

// sessionOf derives one run's session from that run's events: timing from
// OR-58's Timing, progress from the lines the agent itself wrote.
func sessionOf(evs []events.Event) Session {
	s := Timing(evs)

	var activityAt, modelAt time.Time
	for _, e := range evs {
		// A STEP IS A TOOL CALL. It is the finest-grained evidence the log has
		// that the agent did something rather than said something, which is why
		// internal/work emits one per call; counting every event instead would
		// make a chatty run look further along than a working one.
		if e.Kind == events.KindTool {
			s.Steps++
		}

		if (e.Kind == events.KindTool || e.Kind == events.KindSay) && !e.At.Before(activityAt) {
			// What it is doing right now: the newest thing it did or said. Not
			// the newest event of any kind -- a run-end or a CI verdict is
			// Orion reporting on the run, not the run's own words.
			s.Activity = e.Msg
			activityAt = e.At
		}

		// The model that is running NOW, which is the last one to say so. A
		// run that fell back after a capacity error carries both, and the one
		// that answers "what is on this ticket" is the later one. An event
		// with no model does not clear it: most kinds carry none, and silence
		// is not a change of model.
		if e.Model != "" && !e.At.Before(modelAt) {
			s.Model = e.Model
			modelAt = e.At
		}
	}
	return s
}
