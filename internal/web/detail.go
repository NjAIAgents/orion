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
	// Stage is the name of whichever Stage span was open when this step
	// happened -- attributed by position in the same forward scan that
	// builds Stages, the same timestamp-ordered attribution Stage.Cost
	// already uses. Empty for a step that happened before the run's first
	// stage crossing (or a run with no stage crossings recorded at all).
	Stage string
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
	Name string
	// Actor is who this stage handed off TO -- the one running it. By is who
	// handed off FROM: "orion" for the very first crossing (routing ->
	// implementing, By: events.ActorOrion in internal/work/work.go), an
	// actor id for every crossing after (the previous stage's own actor
	// handing the next one off). Both are needed to show the orchestrator
	// as its own node ahead of the pipeline, not folded into "whoever ran
	// the first stage" -- orion never runs a stage, it only ever hands one
	// off.
	Actor string
	By    string
	At    time.Time
	// Done is false for the run's current (last) stage, when the run has
	// not yet crossed out of it -- a stage with no known end, not one that
	// silently reports a zero-length span.
	Done bool
	Cost float64
	// Children is every distinct session that reported usage inside this
	// stage's span, oldest first -- a fan-out (QA's authoring pass, several
	// agents at once) produces more than one, since every job in a fan
	// shares Actor/Key/Model/Stage but each opens its own CLI session
	// (internal/supervisor.Fan's own doc comment). A stage with exactly one
	// session still gets one Child here rather than folding it away, so a
	// reader never has to wonder whether "no children" means "no data" or
	// "not a fan".
	Children []Child
}

// Child is one session's contribution inside a stage: what internal/work's
// fan-out gave it (About, e.g. "4 case(s) · column derivation" -- the one
// field that tells five otherwise-identical QA authors apart), what it
// cost, and whether it failed. Grouped by session id (KindUsage's
// session_id detail key), the only per-fan-child identifier the log
// carries.
type Child struct {
	Session string
	About   string
	Cost    float64
	Failed  bool
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
			step := Step{At: e.At, Kind: e.Kind, Actor: e.Actor, Model: e.Model, Text: e.Msg}
			if openStage != nil {
				step.Stage = openStage.Name
			}
			d.Steps = append(d.Steps, step)
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
				by, _ := e.Detail["by"].(string)
				d.Stages = append(d.Stages, Stage{Name: to, Actor: e.Actor, By: by, At: e.At})
				openStage = &d.Stages[len(d.Stages)-1]
			}
		case events.KindUsage:
			cost, ok := e.Detail["cost_usd"].(float64)
			if !ok {
				break
			}
			d.Cost += cost
			if openStage == nil {
				break
			}
			openStage.Cost += cost
			session, _ := e.Detail["session_id"].(string)
			if session == "" {
				break
			}
			about, _ := e.Detail["about"].(string)
			failed, _ := e.Detail["exit"].(float64) // json.Unmarshal decodes every number as float64
			if c := findChild(openStage.Children, session); c != nil {
				c.Cost += cost
			} else {
				openStage.Children = append(openStage.Children, Child{
					Session: session, About: about, Cost: cost, Failed: failed != 0,
				})
			}
		}
	}
	return d
}

// findChild returns a pointer to the child carrying session, or nil. Used
// only within the same loop iteration that might append to the slice it
// searches, never held across one -- the same rule openStage/openAsk
// already follow, for the same reason (a later append can reallocate the
// backing array and invalidate an older pointer into it).
func findChild(children []Child, session string) *Child {
	for i := range children {
		if children[i].Session == session {
			return &children[i]
		}
	}
	return nil
}
