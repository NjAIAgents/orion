package web

// The board's columns behind `orion web` (OR-273): one per state of the
// queue's label state machine, in the order a ticket travels them.
//
// THIS FILE DECLARES NO STATE. Not a name, not a label, not a count. The
// column set is whatever internal/tracker says the state machine is, so a
// state added there -- a sixth one, or a rename of an existing one -- becomes
// a column here with no edit to this package.
//
// A list written out here would read correctly the day it was written and go
// silently wrong the day the machine changed, which is the duplication OR-54
// names for config and the same failure `orion queue` still has: its group
// list is spelled out at the call site and has been missing the newest state
// since the day that state was added. This seam is inherited by every later
// view, so it derives instead, and no state is spelled out above -- the test
// TestNoQueueStateIsDeclaredInTheWebPackage reads this file too.

import "github.com/orion-sdlc/orion/internal/tracker"

// Columns is the board, left to right, for a project whose queue label is
// queueLabel.
//
// It returns tracker.QueueState rather than a web-shaped copy of it, for the
// reason Roster returns actors.RosterEntry: a type of its own here would have
// to be filled field by field from that one, and the day a state gains a
// field the page keeps drawing the fields it knew about.
//
// Done is not a column. Done is the absence of every one of these labels --
// the state machine says so, and a page that invented a sixth column would be
// stating something the tracker does not record.
func Columns(queueLabel string) []tracker.QueueState {
	return tracker.QueueStates(queueLabel)
}
