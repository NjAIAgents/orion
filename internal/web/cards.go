// Package web derives what the `orion web` surface shows out of the event
// log, and nothing else. No server, no templates, no clock -- those arrive on
// their own tickets; this is the layer they read.
//
// EVERYTHING HERE COMES FROM events.jsonl. The same rule internal/dashboard
// states for its own numbers applies with more force here, because a card is
// the first thing a person looks at: a second source for "which runs are
// there" would eventually disagree with the log, and the log is what anyone
// falls back to when the picture looks wrong.
package web

import (
	"sort"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// Card is one run of one ticket -- the unit the run view puts on the grid.
//
// THE GROUPING IS (KEY, RUN), NOT KEY. A ticket can be worked more than once:
// a fix round, a retry after a red build, a second attempt on a branch that
// was reset. Keyed by ticket alone those collapse into one card whose step
// count is the sum of attempts and whose model is whichever one happened to
// go last -- a card that describes no run that ever took place. Keyed by run
// alone they lose the ticket they belong to, which is the only thing a person
// is looking for when they open the page.
type Card struct {
	Key string
	Run string
	// Steps is how many events the run produced. The event log is "what Orion
	// did" -- a claim, a branch, a question, an answer, a push -- so its lines
	// ARE the run's steps, and counting them invents nothing. It is a dozen
	// per run, not the thousands a transcript holds.
	Steps int
	// Latest is the newest event in the group: when this run last did
	// anything. Taken as a maximum rather than the last element, because a
	// card must not be dated by the order lines happen to sit in a file.
	Latest time.Time
	// Model is the model named by the most recent event that named one.
	//
	// Per event rather than per run because that is how the log records it:
	// one ticket is worked by several models, and the card reports the one
	// working now. Events that name none -- a CI verdict, a human escalation
	// -- leave the last known model standing rather than blanking the card,
	// since "no model" would read as a run with no agent when the truth is a
	// line that simply had no model to report.
	Model string
}

// Scan groups a log's events into one card per (key, run) pair, newest
// activity first.
//
// Pure, like aiops.Scan: give it the same events and it gives the same cards,
// so what the page shows can be reproduced from a log file rather than
// believed.
//
// An event that names no ticket or no run is attributable to no card and is
// SKIPPED. Supervisor-level lines -- a batch starting, a queue sweep -- are
// real and belong elsewhere; putting them on a card under an empty key would
// invent a run nobody started.
func Scan(evs []events.Event) []Card {
	type id struct{ key, run string }
	index := map[id]*Card{}
	modelAt := map[id]time.Time{}
	var order []id

	for _, e := range evs {
		if e.Key == "" || e.Run == "" {
			continue
		}
		i := id{e.Key, e.Run}
		c, ok := index[i]
		if !ok {
			c = &Card{Key: e.Key, Run: e.Run}
			index[i] = c
			order = append(order, i)
		}
		c.Steps++
		if e.At.After(c.Latest) {
			c.Latest = e.At
		}
		// A later event that names a model replaces an earlier one; an event
		// with no model changes nothing.
		if e.Model != "" && !e.At.Before(modelAt[i]) {
			c.Model = e.Model
			modelAt[i] = e.At
		}
	}

	out := make([]Card, 0, len(order))
	for _, i := range order {
		out = append(out, *index[i])
	}
	// Newest first: the grid leads with what is happening now. Ties break on
	// key then run so the order is total -- a card grid that reshuffles
	// between two identical scans is unreadable.
	sort.SliceStable(out, func(a, b int) bool {
		if !out[a].Latest.Equal(out[b].Latest) {
			return out[a].Latest.After(out[b].Latest)
		}
		if out[a].Key != out[b].Key {
			return out[a].Key < out[b].Key
		}
		return out[a].Run < out[b].Run
	})
	return out
}
