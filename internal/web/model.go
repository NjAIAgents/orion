// Package web is the model behind `orion web` (OR-55).
//
// It holds what a browser needs to draw a run and nothing else: no server, no
// event reader, no clock. The types come first, and deliberately, because
// every other ticket in the epic -- the reader that fills them, the handler
// that serves them, the page that draws them -- names these fields, and a
// field renamed after three consumers exist is renamed in four places.
//
// THE MOCKUPS ARE THE CONSUMER. docs/design/web/01-run-view.html draws a card
// grid: one card per ticket, each carrying who is on it, on what model, how
// far in, what it is doing right now, and how long it has taken. Each field
// below exists because that page shows it; a field the page does not show is
// not here yet.
//
// EVERY STRING BELOW IS UNTRUSTED (OR-276). Title is whatever a tracker
// accepted, Activity and Gate come off logs agents and other tooling write,
// and the board is the control plane once the write endpoints exist. So they
// are plain strings and stay plain strings: rendered through html/template's
// contextual escaping, never converted to template.HTML/JS/URL, which say
// "already escaped" about the one kind of value that never is. escaping_test.go
// holds both halves of that.
//
// NOTHING HERE READS A CLOCK OR A FILE. A snapshot is derived from the event
// log the same way internal/dashboard derives its view (OR-254): every
// timestamp came out of an event, so the same log always produces the same
// snapshot, and a test can state an expected duration rather than tolerate
// one. The moment Elapsed called time.Now, a paused run would keep ticking
// and two readers of one log would disagree.
package web

import "time"

// Snapshot is one run, at one instant, as a page would draw it.
type Snapshot struct {
	// At is the instant the snapshot describes -- the timestamp of the last
	// event read, not the wall clock, so the same log yields the same
	// snapshot on every replay.
	At time.Time
	// Started is when the run began: the mockup's "since 13:31:04".
	Started time.Time
	// Cards is the grid, in the order it should be drawn. Ordering is the
	// reader's decision, not this type's -- the page draws what it is given.
	Cards []Card
}

// Card is one ticket in the grid.
type Card struct {
	// Key is the tracker key ("OR-55"), and it is also what the page colours
	// by: internal/ui gives each ticket its own colour and the browser reuses
	// the same axis so the two surfaces never disagree.
	Key string
	// Title is the ticket's summary, as the tracker states it.
	Title string
	// Verb is the outcome word, one of internal/ui's five and no others: ok,
	// working, waiting, warning, failed. A plain string rather than an import
	// of internal/ui, which is a terminal package this model has no reason to
	// depend on; the vocabulary is the contract, not the constant.
	//
	// It is never empty and never a sixth word. Colour is never the sole
	// carrier of state on either surface, so this word is what a reader
	// actually reads.
	Verb string
	// Gate is why nothing is running, when nothing is: "pull request #482
	// opened, awaiting CI -- no agent is running". Empty when an agent is on
	// it. A waiting card with no gate is a card that cannot explain itself,
	// which is the whole complaint that produced the board.
	Gate string
	// Session is the agent on this ticket. The zero value means none is --
	// a ticket waiting on CI or on a person has a card but no session.
	Session Session
}

// Session is one agent run: who, on what, how far in.
type Session struct {
	// Actor is the role identifier the event log records ("implementer",
	// "qa", "ci") -- the stable key, matched against internal/events' Actor
	// constants.
	Actor string
	// Role is what a person reads -- an operator's chosen name joined to the
	// job title, whatever actors.Display returns for Actor at the time.
	//
	// Carried alongside Actor rather than looked up at draw time because
	// names are operator-configurable, and a page rendering a snapshot from
	// an older log must show the names that log was written under.
	//
	// No example is written here on purpose. Every shipped default name is
	// renameable, so a name frozen into a comment eventually points at
	// somebody this build no longer has -- which is the same rule
	// internal/actors enforces across the tree, and it enforces it on
	// comments too.
	Role string
	// Model is what actually ran, taken from the agent's own frames rather
	// than from what Orion asked for: --model is a request, and a fallback
	// after a capacity error is exactly when the difference matters.
	Model string
	// Steps is how many steps the agent has taken so far.
	Steps int
	// Activity is what it is doing right now, in its own words: "editing
	// internal/web/board.go". Empty when it is not saying.
	Activity string
	// Started is the first event of this session, Last the most recent one.
	//
	// Two timestamps rather than a start and a duration, because Last is the
	// only evidence a live view has that a session is still alive: an agent
	// that has said nothing for four minutes looks identical to one that
	// finished, unless the page can see when it last spoke.
	Started time.Time
	Last    time.Time
	// Done reports that the run ended. Distinct from the card's Verb, which
	// says how it ended and can describe a ticket with no session at all.
	Done bool
}

// Elapsed is the span the log recorded for this session: first event to last.
//
// DERIVED, not stored, so it cannot disagree with the timestamps beside it,
// and derived from Last rather than from now so a finished session and a
// replayed one report the same number. A session whose events arrived out of
// order would compute negative; it reports zero instead, because a card
// reading "-3m 12s" is a rendering bug reported as a measurement.
func (s Session) Elapsed() time.Duration {
	if s.Last.Before(s.Started) {
		return 0
	}
	return s.Last.Sub(s.Started)
}
