package work

// Which actor works a ticket.
//
// Before this file, nothing read a ticket's labels, components or issue type
// -- runOne hardcoded events.ActorImplementer at the run site, on resume, and
// in the QA fix loop, so every ticket of every kind was the backend
// developer's. events.ActorFrontend was defined and configurable and had
// never worked a ticket; a "documentation" label had nowhere to route to at
// all. See OR-171.
//
// Deliberately NOT a model call. internal/advise already owns the router for
// free-text questions the implementer stops on -- a label-to-actor lookup is
// a deterministic map, and an LLM would add cost, latency and
// non-determinism to something that does not need a judgement. Expanding the
// table does not change that: the temptation is to infer the right actor
// from the summary when no marker is present, which is the same mistake with
// a friendlier face. An unmarked ticket defaults, and the fix belongs at
// planning time (OR-191).
//
// THE TABLE IS A PUBLISHED CONTRACT, not a private constant. `orion routes`
// prints it, and the decompose stage tells the planner to read it, because
// the routing this file performs is only as good as the markers something
// else wrote. Two rules that nothing knew how to set is how every OR ticket
// came to default to the implementer while the log correctly announced the
// default on every single run (OR-191). A planner that keeps its own copy of
// the vocabulary keeps a copy that drifts, so there is exactly one.

import (
	"strings"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// Rule is one row of the table: an actor, and the exact markers that reach
// it.
type Rule struct {
	Actor    string
	Keywords []string
}

// route is the table itself: the first rule whose keywords appear in the
// ticket's issue type, components or labels wins. A slice, not a map, so the
// precedence (documentation before UI) is the order written here rather than
// Go's unspecified map iteration.
//
// WHICH ACTORS ARE ROUTABLE IS A DECISION, and the short table is the
// evidence for it: with docs and frontend alone, a planner tagging perfectly
// could express two destinations besides the default, so telling one to
// "route accordingly" would have meant "occasionally write the word docs".
//
// Architect and product manager are added here because they are the only
// roster entries a ticket could not reach at all. Both exist today solely as
// ADVISORS -- internal/advise hands them a free-text question raised inside
// somebody else's run and takes back an answer. That is a different job from
// working a ticket end to end, so a route to them is a first way in, not a
// second one. Every other actor already has a path and keeps it: see
// otherPaths, and OR-176 for what a second way to invoke one thing costs.
//
// Keyword sets do not overlap, so precedence only decides a ticket carrying
// two markers. "design" is deliberately absent from the architect's set: it
// reads as UI design at least as often as system design, and a keyword that
// routes a ticket to the wrong actor half the time is worse than no keyword.
var routeRules = []Rule{
	{events.ActorDocs, []string{"documentation", "docs", "doc"}},
	{events.ActorFrontend, []string{"ui", "frontend", "front-end"}},
	{events.ActorArchitect, []string{"architecture", "architect", "adr"}},
	{events.ActorPM, []string{"product", "pm", "requirements"}},
}

// Rules is the published table, in precedence order.
//
// Copied on the way out. A published contract that a caller can reach in and
// edit is not a contract, and the one caller that most wants to read it --
// the command that prints it for a planner -- is the one that must not be
// able to.
func Rules() []Rule {
	out := make([]Rule, len(routeRules))
	for i, r := range routeRules {
		out[i] = Rule{Actor: r.Actor, Keywords: append([]string(nil), r.Keywords...)}
	}
	return out
}

// DefaultActor is the actor a ticket carrying no marker goes to.
const DefaultActor = events.ActorImplementer

// OtherPath is an actor deliberately absent from the table, and the path
// that reaches it instead.
type OtherPath struct {
	Actor string
	Why   string
}

// otherPaths is the other half of the decision, and it is recorded rather
// than left implicit because "not in the table" and "nobody got round to it"
// look identical from outside.
//
// Every one of these is already invoked by something. Adding a label that
// invoked it again would create a SECOND way to reach one thing, which is
// how OR-176 happened: the two ways drift, and the failure surfaces as a
// stage that ran twice or not at all.
var otherPaths = []OtherPath{
	{events.ActorRouter, "routes the free-text question an implementer stops on, inside a run"},
	{events.ActorQA, "runs after every implementation, on whatever actor worked the ticket"},
	{events.ActorCaseDerive, "derives QA's cases from the ticket, inside the QA stage"},
	{events.ActorDevOps, "repairs a red build, from the CI verdict"},
	{events.ActorLogTriage, "reads the failing CI log that the repair run then carries"},
	{events.ActorDescriber, "writes the pull request body, on every ticket"},
	{events.ActorExplore, "answers one question about the repository, inside a run"},
	// Deliberately not routable, and not automatic either. It reads a
	// FINISHED run's event log, so there is no ticket to route to it: a
	// person or a cron runs `orion aiops <KEY>` afterwards. Making it a route
	// would spend money judging every ticket, and the whole design of that
	// pass is that most runs need no agent at all (OR-168).
	{events.ActorAIOps, "reads a finished run's event log when `orion aiops` is run, after the fact"},
}

// OtherPaths lists the actors that are not routable, and why not.
func OtherPaths() []OtherPath { return append([]OtherPath(nil), otherPaths...) }

// Route picks which actor works a ticket, and says why.
//
// Default is the implementer. THE DEFAULT MUST NEVER BE SILENT: a route that
// falls through without saying so is exactly how the frontend actor stayed
// unreachable for as long as it did -- every ticket landing on the
// implementer looked correct from the outside, because nothing said a
// different choice was even considered.
//
// It is announced as an outcome, not as a miss. On the happy path a ticket
// with no marker is a backend ticket and the implementer is the right answer;
// phrasing that as "nothing matched a route" reads as a failure of the run
// rather than a description of the ticket (OR-191).
func Route(issue tracker.Issue) (actor, why string) {
	fields := make([]string, 0, 2+len(issue.Components)+len(issue.Labels))
	if issue.IssueType != "" {
		fields = append(fields, issue.IssueType)
	}
	fields = append(fields, issue.Components...)
	fields = append(fields, issue.Labels...)

	for _, rule := range routeRules {
		if field, ok := matches(fields, rule.Keywords); ok {
			return rule.Actor, "matched " + field
		}
	}
	return DefaultActor, "defaulting to the implementer; no routing marker on this ticket"
}

// Tally is one actor's share of a set of tickets.
type Tally struct {
	Actor string
	N     int
}

// Distribution reports where a set of tickets would route, in table order,
// with the default's share included even when it is all of them.
//
// Reported BEFORE the work starts, which is the whole point. A queue that is
// entirely default is either correct -- these really are backend tickets --
// or a planning failure that nothing wrote a marker for, and until the split
// is printed those two look the same until the run is over and paid for
// (OR-191).
func Distribution(issues []tracker.Issue) []Tally {
	n := map[string]int{}
	for _, i := range issues {
		actor, _ := Route(i)
		n[actor]++
	}
	// Table order, then the default, so the same queue always prints the
	// same way. Sorting by count would reorder the line every time a ticket
	// moved between states.
	var out []Tally
	for _, r := range routeRules {
		if c := n[r.Actor]; c > 0 {
			out = append(out, Tally{r.Actor, c})
		}
	}
	if c := n[DefaultActor]; c > 0 {
		out = append(out, Tally{DefaultActor, c})
	}
	return out
}

// matches reports the first field that equals one of the keywords,
// case-insensitively. Equality, not substring: a component named
// "docsite-infra" is not a documentation ticket, and matching on containment
// would route it as one.
func matches(fields, keywords []string) (string, bool) {
	for _, f := range fields {
		for _, k := range keywords {
			if strings.EqualFold(strings.TrimSpace(f), k) {
				return f, true
			}
		}
	}
	return "", false
}
