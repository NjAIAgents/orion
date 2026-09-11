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
// comes from the tracker (OR-77, not yet consumed here).
//
// VERB WAS ONCE LEFT ZERO ON PURPOSE: a run with a failed event rendered
// "working" by a default is worse than one rendered blank, and until OR-52
// nothing distinguished "still running" from "abandoned two weeks ago" --
// the newest event's own Kind cannot tell you that a process that would have
// written the next one no longer exists. OR-52's session records close that
// gap: Live (below) is real evidence, not a guess, so Verb is derived now.
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
	"github.com/orion-sdlc/orion/internal/session"
	"github.com/orion-sdlc/orion/internal/ui"
)

// Scan groups a log into cards: one per (key, run) pair, ordered by key, then
// by when the run started, then by run id.
//
// live names every ticket key a currently-beating KindWork session claims to
// be working (session.Record.Projects, for that Kind, is exactly opts.Keys
// from internal/work.Run) -- the set this function needs to tell "still
// running" from "stopped without finishing". A KindWatch session's own
// Projects is project-scoped, not ticket-scoped, and says nothing about
// which ticket inside it is live, so it plays no part in this check; the
// watcher merely being alive does not make any one ticket's run current.
//
// A TOTAL ORDER, not the order the lines happened to arrive in, because the
// surface reading this draws a grid and a grid that reshuffles between two
// identical scans cannot be diffed -- the same rule sessions.Scan follows for
// the same reason.
//
// An event with no key is skipped. It is real -- supervisor lines are emitted
// before any ticket is claimed -- but there is no ticket to draw it on, and a
// card keyed on the empty string is a card nobody can click.
func Scan(evs []events.Event, live map[string]bool) []Card {
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
	verbs := make(map[runID]string, len(groups))
	for id, g := range groups {
		sessions[id] = sessionOf(g)
		verbs[id] = verbOf(g, sessions[id], live[id.key])
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
		out = append(out, Card{Key: id.key, Run: id.run, Verb: verbs[id], Session: sessions[id]})
	}
	return out
}

// GraceAfterFinish is how long a finished run stays on the run page before
// it drops to history-only -- long enough to read its result, short enough
// that the page stays a live view rather than an archive (OR-435).
const GraceAfterFinish = 2 * time.Minute

// CurrentBatch narrows every card Scan ever produced down to the run page's
// actual subject: what is happening right now, not everything that has ever
// been logged.
//
// Scan has no time boundary and no reason to have one -- it groups whatever
// events it is given, and buildSnapshot hands it EVERY event in EVERY
// workspace's log, so its output spans the log's entire history. Without
// this filter the run page (docs/design/web/01-run-view.html's "Running 5
// agents ... since 13:31:04") showed 95 cards going back weeks: release
// tags, months-old tickets, everything -- because nothing between Scan and
// the page ever asked "is this part of what's running NOW".
//
// A card survives if EITHER its key is currently live (an active
// session.KindWork session is beating for it -- the same live map Scan's
// own verb coloring already reads), OR it just finished: Done and its
// Session.Last is within GraceAfterFinish of now. The second clause is what
// keeps a just-completed card visible for a beat, matching the mockup's "2
// done" cards, rather than a card vanishing the instant its session ends.
//
// A ticket genuinely waiting on CI or a person (the mockup's OR-279/OR-290)
// has no session-level signal yet distinguishing it from a run that simply
// stopped -- that gap is OR-436, not this ticket. CurrentBatch only narrows
// by liveness and the finish grace window; it cannot keep a card this
// package has no way to recognise as "waiting" in the first place.
func CurrentBatch(cards []Card, live map[string]bool, now time.Time) []Card {
	out := make([]Card, 0, len(cards))
	for _, c := range cards {
		if live[c.Key] {
			out = append(out, c)
			continue
		}
		if c.Session.Done && !c.Session.Last.IsZero() && now.Sub(c.Session.Last) <= GraceAfterFinish {
			out = append(out, c)
		}
	}
	return out
}

// verbOf is one run's outcome word: one of internal/ui's five, matching
// exactly what the terminal would show for the same events -- the browser
// and the console must not disagree about what a run looks like, which is
// the rule OR-69's log panel already follows for individual lines.
//
// FAILED IS STICKY; EVERYTHING ELSE IS NEWEST-WINS. internal/dashboard sets
// this precedent already (its own state map holds "fixing" from a
// KindFailed/KindBlocked seen ANYWHERE in the run, not just the latest
// line): a run that hit a real failure and then wrote a housekeeping
// run-end has not un-failed. VerbFor gives run-end no case of its own, so it
// falls to the same "ok" bucket as commit/push/merge -- exactly right for a
// run that never failed, and exactly wrong as a way to erase one that did.
// So failed/blocked is checked across the WHOLE run first; only once that
// comes back clean does newest-timestamp decide among the rest.
//
// A run with NO run-end is either genuinely still going (live is true, the
// composition rule OR-52's own doc states: session present and beating ->
// that process is alive) or it stopped without finishing (live is false --
// killed, crashed, laptop closed) and OR-52 says exactly what that state is
// called: STOPPED, read here as VerbFail, because a card silently reading
// "working" for a run nothing is running is the misleading state this
// exists to fix.
func verbOf(evs []events.Event, s Session, live bool) string {
	if !s.Done && !live {
		return ui.VerbFail
	}
	for _, e := range evs {
		if ui.VerbFor(e.Kind) == ui.VerbFail {
			return ui.VerbFail
		}
	}

	verb := ui.VerbOK
	var at time.Time
	for _, e := range evs {
		if e.At.Before(at) {
			continue
		}
		verb, at = ui.VerbFor(e.Kind), e.At
	}
	return verb
}

// liveWorkKeys is every ticket key any currently-beating KindWork session
// claims, across every session record under home -- what Scan's live
// parameter is built from at the one call site (buildSnapshot) that reads
// from disk rather than from a fixture.
func liveWorkKeys(home string, now time.Time) (map[string]bool, error) {
	records, err := session.Enumerate(home, now)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool)
	for _, r := range records {
		if r.Kind != session.KindWork {
			continue
		}
		for _, k := range r.Projects {
			keys[k] = true
		}
	}
	return keys, nil
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
