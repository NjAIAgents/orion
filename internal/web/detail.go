package web

// Detail is one run's full history: what a card's click-through shows
// (OR-53). Where Card is the grid's progress-so-far view, Detail is
// everything the event log recorded for one (key, run) pair, ordered.
//
// ABSENT, NOT EMPTY (the ticket's own acceptance criterion): a run with no
// pull request has a nil PR, not a PR field the page has to know means
// nothing. A run with no advisor exchange has an empty Asks slice, not one
// element of zero values. The page decides what "absent" looks like; this
// type only decides whether there is anything to show.
//
// EVERY STRING HERE IS UNTRUSTED, the same rule model.go states for Card:
// Steps' Activity, an Ask's Question, a Decision's Text all come off logs an
// agent or other tooling wrote, and render through html/template's
// contextual escaping like every other field in this package.

import (
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// Step is one thing the agent did: a tool call or a line of narration, in
// the order the log recorded it.
type Step struct {
	At    time.Time
	Kind  string // events.KindTool or events.KindSay
	Actor string
	Model string
	Text  string
}

// Ask is one question-to-answer exchange: the implementer asked, an advisor
// answered or refused, in that order. Refuse and Answer are mutually
// exclusive -- a question either got answered or it did not, never both.
type Ask struct {
	At       time.Time
	Question string
	Answer   string // empty if refused or still open
	Refused  bool
}

// Decision is one recorded choice, as the event log's own message states
// it -- the log carries this as one formatted line (work.go's
// "%s -- grounded in %s; recorded in %s"), not as structured fields, so
// Detail exposes exactly that line rather than parsing it apart and risking
// a rendering that quietly stops matching what was actually written.
type Decision struct {
	At   time.Time
	Text string
}

// PullRequest is what the pr and ci events recorded about this run's PR.
// CI is empty until a ci event arrives -- a PR with no verdict yet, not a
// PR whose CI silently reads as passing.
type PullRequest struct {
	URL string
	CI  string
}

// Detail is everything the log recorded for one (key, run) pair.
type Detail struct {
	Key     string
	Run     string
	Session Session

	// Steps is the ordered tool/say history. Nil, not empty, when the run
	// never got that far -- see the package doc comment on "absent".
	Steps []Step
	// Asks is the ordered ask/answer/refuse exchange. Nil when the
	// implementer never asked anything.
	Asks []Ask
	// Decisions is every recorded decision, oldest first. Nil when none was
	// recorded.
	Decisions []Decision
	// PR is nil until a pr event arrives for this run.
	PR *PullRequest
}

// ScanDetail builds one run's Detail from its own events -- the same
// (key, run) grouping Scan uses for cards, applied to a single run rather
// than every run in the log.
//
// evs need not be pre-filtered to this key and run: ScanDetail filters
// itself, the same posture Scan takes, so a caller can hand it the whole
// log without pre-grouping.
func ScanDetail(evs []events.Event, key, run string) Detail {
	var mine []events.Event
	for _, e := range evs {
		if e.Key == key && e.Run == run {
			mine = append(mine, e)
		}
	}

	d := Detail{Key: key, Run: run, Session: sessionOf(mine)}

	var openAsk *Ask
	for _, e := range mine {
		switch e.Kind {
		case events.KindTool, events.KindSay:
			d.Steps = append(d.Steps, Step{
				At: e.At, Kind: e.Kind, Actor: e.Actor, Model: e.Model, Text: e.Msg,
			})
		case events.KindAsk:
			d.Asks = append(d.Asks, Ask{At: e.At, Question: e.Msg})
			openAsk = &d.Asks[len(d.Asks)-1]
		case events.KindAnswer:
			if openAsk != nil {
				openAsk.Answer = e.Msg
				openAsk = nil
			}
		case events.KindRefuse:
			if openAsk != nil {
				openAsk.Refused = true
				openAsk = nil
			}
		case events.KindDecision:
			d.Decisions = append(d.Decisions, Decision{At: e.At, Text: e.Msg})
		case events.KindPR:
			d.PR = &PullRequest{URL: e.Msg}
		case events.KindCI:
			if d.PR != nil {
				d.PR.CI = e.Msg
			}
		}
	}
	return d
}
