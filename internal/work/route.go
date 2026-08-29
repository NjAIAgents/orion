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
// non-determinism to something that does not need a judgement.

import (
	"strings"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// route is the table itself: the first rule whose keywords appear in the
// ticket's issue type, components or labels wins. A slice, not a map, so the
// precedence (documentation before UI) is the order written here rather than
// Go's unspecified map iteration.
var routeRules = []struct {
	actor    string
	keywords []string
}{
	{events.ActorDocs, []string{"documentation", "docs", "doc"}},
	{events.ActorFrontend, []string{"ui", "frontend", "front-end"}},
}

// route picks which actor works a ticket, and says why.
//
// Default is the implementer. THE DEFAULT MUST NEVER BE SILENT: a route that
// falls through without saying so is exactly how the frontend actor stayed
// unreachable for as long as it did -- every ticket landing on the
// implementer looked correct from the outside, because nothing said a
// different choice was even considered.
func route(issue tracker.Issue) (actor, why string) {
	fields := make([]string, 0, 2+len(issue.Components)+len(issue.Labels))
	if issue.IssueType != "" {
		fields = append(fields, issue.IssueType)
	}
	fields = append(fields, issue.Components...)
	fields = append(fields, issue.Labels...)

	for _, rule := range routeRules {
		if field, ok := matches(fields, rule.keywords); ok {
			return rule.actor, "matched " + field
		}
	}
	return events.ActorImplementer, "no issue type, component or label matched a route; defaulting to the implementer"
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
