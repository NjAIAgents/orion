// Package web is what `orion web` serves: the run view, as data.
//
// This file is the vocabulary and nothing else. It declares the three shapes
// the surface is built out of -- a Snapshot of the whole board, a Card per
// ticket, a Session per agent run -- and it reads nothing, opens nothing and
// serves nothing. Every other ticket in the epic (the reader that fills these
// from events.jsonl, the handler that serves them, the page that renders
// them) inherits these names, so they are settled once, here, before three
// packages each invent their own word for "how long has this been running".
//
// EVERYTHING HERE IS DERIVED FROM events.jsonl, same rule internal/dashboard
// works under: the log already records who acted, on what model, at what
// time. A second source for any of it would eventually disagree with the log,
// and the log is what people trust when they are debugging at midnight.
//
// The shape comes from the mockups in docs/design/web, which in turn lift
// their vocabulary from internal/ui so the browser and the console never
// disagree about what a run looks like.
//
// NO I/O IN THIS FILE, deliberately. A type declaration that cannot read a
// file cannot acquire an opinion about where the log lives, which is what
// keeps the reader, the server and the renderer able to disagree about that
// without renegotiating the types.
package web

import "time"

// Snapshot is the whole run view at one instant -- what a request gets, and
// what a re-poll replaces wholesale.
//
// A snapshot, not a stream of deltas: the log is the append-only record and
// the browser holds no state worth reconciling against it. Re-reading is
// cheap and cannot drift; patching is neither.
type Snapshot struct {
	// At is when this snapshot was taken. It is the clock every elapsed on
	// every running Session below was measured against, so a card that says
	// "4m 12s" and a page footer that says when it was read agree by
	// construction rather than by two calls to time.Now.
	At time.Time
	// Cards is one entry per ticket the view knows about, in the order the
	// view should show them. Ordering is the reader's call, not the
	// renderer's -- the renderer has no basis for one.
	Cards []Card
}

// Card is one ticket on the board: the unit the grid lays out.
type Card struct {
	// Key is the tracker key, OR-55. The identity of the card, and what the
	// per-ticket colour axis is keyed on.
	Key string
	// Title is the ticket's summary, as the tracker has it.
	Title string
	// Status is one of internal/ui's five verbs -- ok, working, waiting,
	// warning, failed -- and no others. A plain string rather than the
	// constants: importing the terminal renderer into the web model to
	// borrow five words would tie a browser page to a package that exists to
	// paint ANSI.
	Status string
	// Sessions is every agent run this ticket has had, oldest first. A
	// ticket is worked by several -- one implements, another routes the
	// question it stops on, a third answers it -- and the card shows the
	// last, but the history is what makes "3 steps" attributable to somebody
	// rather than to "the agent".
	Sessions []Session
}

// Session is one agent's run against one ticket: the thing that has a model,
// spends money, and either finished or did not.
type Session struct {
	// Actor is the STABLE role identifier as internal/events persists it --
	// "implementer", "qa", "ci". Never a display name: names live in
	// internal/actors and are applied at render time, so a team that renames
	// the developer migrates no data and no snapshot.
	Actor string
	// Model is which model ran, as recorded. Empty means the event carried
	// none and the actor's default applies -- a display fallback the
	// renderer resolves, not a claim this snapshot should invent.
	Model string
	// Steps is how many observable things the agent has done so far: the
	// tool calls and messages the supervisor already reduces to activity.
	// The number under the card.
	Steps int
	// Activity is the most recent of those, in the agent's own words --
	// "editing internal/web/board.go", "searching code". One line, present
	// tense, and only meaningful while Done is false.
	Activity string
	// Started is when the session opened.
	Started time.Time
	// Last is when it was last heard from. On a finished session that is
	// when it ended; on a running one it is how stale the Activity above is,
	// which is the difference between an agent working and an agent hung.
	Last time.Time
	// Elapsed is how long the session has been running.
	//
	// Carried rather than derived, because the two cases have different
	// endpoints and only the reader knows which applies: a finished session
	// ran Started..Last, a running one is still running at Snapshot.At. A
	// renderer subtracting Started from Last would freeze every live card at
	// the moment of its last log line.
	Elapsed time.Duration
	// Done reports that the session ended -- for any reason, well or badly.
	// Whether it ended WELL is the ticket's Status, one level up; a failed
	// run is still a finished one, and folding the two together is how a
	// board ends up showing a crashed agent as still working.
	Done bool
}
