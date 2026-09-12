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
//
// Answer/Refused summarise the LAST turn (below): the outcome the
// implementer actually resumed on. A page that only needs "was this
// answered" reads these two fields and never has to walk Turns itself.
type Ask struct {
	At       time.Time
	Question string
	Answer   string // empty if refused or still open
	Refused  bool
	// Turns is the routing and advisor exchange this ask actually went
	// through, oldest first: internal/work's consult() routes to one
	// advisor, and on that advisor's escalate verdict, retries the other
	// advisor exactly once (never a search through every role) -- so this
	// is one or two advisor turns, plus the router's own note ahead of
	// them. Nil for an ask this log version predates (KindNote/KindEscalate
	// were not always scanned), never fabricated to make one up.
	Turns []Turn
}

// Turn is one step in routing an ask to an answer: who did it, and what
// they said. Kind is events.KindNote (the router's pick), KindEscalate (an
// advisor forwarding to the other role), KindAnswer, or KindRefuse -- the
// same closed vocabulary the log already uses, not a second one invented
// for this page.
type Turn struct {
	At    time.Time
	Kind  string
	Actor string
	Model string
	Text  string
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

// Stage is one span this run spent inside a single stage: the crossing IN
// (a KindStage event whose To names this stage) to the crossing OUT (the
// next KindStage event, or "still here" if there is none yet). Cost is
// every KindUsage event's cost_usd whose own timestamp falls inside that
// span -- usage is recorded per actor run, not tagged with a stage name, so
// attributing it to a stage means matching it by when it happened, the
// same way Timing attributes activity to a session by timestamp rather
// than by an explicit link that does not exist in the log.
type Stage struct {
	Name  string
	Actor string // the actor id this stage handed off TO
	At    time.Time
	// Done is false for the run's current (last) stage, when the run has
	// not yet crossed out of it -- a stage with no known end, not one that
	// silently reports a zero-length span.
	Done bool
	Cost float64
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
	// Stages is the pipeline this run has crossed, oldest first. Nil when
	// the run has not handed off from wherever it started -- see stageOf's
	// own comment in cards.go for the same rule applied to one card.
	Stages []Stage
	// Cost is every KindUsage event's cost_usd, summed. Zero when the run
	// reported none, not "unknown" -- a run whose agents genuinely spent
	// nothing (or predates usage reporting) has nothing to add up.
	Cost float64
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
	var openStage *Stage
	for _, e := range mine {
		switch e.Kind {
		case events.KindTool, events.KindSay:
			d.Steps = append(d.Steps, Step{
				At: e.At, Kind: e.Kind, Actor: e.Actor, Model: e.Model, Text: e.Msg,
			})
		case events.KindAsk:
			d.Asks = append(d.Asks, Ask{At: e.At, Question: e.Msg})
			openAsk = &d.Asks[len(d.Asks)-1]
		// NOTE and ESCALATE are mid-exchange turns, not terminal ones --
		// they must not close openAsk, only record what happened before the
		// eventual KindAnswer/KindRefuse below does.
		case events.KindNote:
			if openAsk != nil && e.Actor == events.ActorRouter {
				openAsk.Turns = append(openAsk.Turns, Turn{
					At: e.At, Kind: e.Kind, Actor: e.Actor, Model: e.Model, Text: e.Msg,
				})
			}
		case events.KindEscalate:
			if openAsk != nil {
				openAsk.Turns = append(openAsk.Turns, Turn{
					At: e.At, Kind: e.Kind, Actor: e.Actor, Model: e.Model, Text: e.Msg,
				})
			}
		case events.KindAnswer:
			if openAsk != nil {
				openAsk.Answer = e.Msg
				openAsk.Turns = append(openAsk.Turns, Turn{
					At: e.At, Kind: e.Kind, Actor: e.Actor, Model: e.Model, Text: e.Msg,
				})
				openAsk = nil
			}
		case events.KindRefuse:
			if openAsk != nil {
				openAsk.Refused = true
				openAsk.Turns = append(openAsk.Turns, Turn{
					At: e.At, Kind: e.Kind, Actor: e.Actor, Model: e.Model, Text: e.Msg,
				})
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
		case events.KindStage:
			if openStage != nil {
				openStage.Done = true
			}
			if to, ok := e.Detail["to"].(string); ok {
				d.Stages = append(d.Stages, Stage{Name: to, Actor: e.Actor, At: e.At})
				openStage = &d.Stages[len(d.Stages)-1]
			}
		case events.KindUsage:
			if cost, ok := e.Detail["cost_usd"].(float64); ok {
				d.Cost += cost
				if openStage != nil {
					openStage.Cost += cost
				}
			}
		}
	}
	return d
}
