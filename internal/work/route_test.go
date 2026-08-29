package work

// OR-171: there was no route from a ticket's labels, components or issue
// type to the actor that works it -- every ticket, of every kind, landed on
// the implementer. These tests are the ones the ticket asks for by name: a
// labelled ticket picks its actor, an unlabelled one defaults to the
// implementer, and no actor this file knows how to route to is unreachable.

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// A labelled ticket picks its actor.
func TestRouteLabelledTicketPicksItsActor(t *testing.T) {
	cases := []struct {
		name  string
		issue tracker.Issue
		want  string
	}{
		{"documentation label", tracker.Issue{Labels: []string{"documentation"}}, events.ActorDocs},
		{"docs label, mixed case", tracker.Issue{Labels: []string{"Docs"}}, events.ActorDocs},
		{"documentation issue type", tracker.Issue{IssueType: "Documentation"}, events.ActorDocs},
		{"ui label", tracker.Issue{Labels: []string{"ui"}}, events.ActorFrontend},
		{"frontend component", tracker.Issue{Components: []string{"frontend"}}, events.ActorFrontend},
		{"other labels alongside the routing one", tracker.Issue{
			Labels: []string{"tech-debt", "ui", "urgent"}}, events.ActorFrontend},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := route(c.issue)
			if got != c.want {
				t.Fatalf("route(%+v) = %q, want %q", c.issue, got, c.want)
			}
			if why == "" {
				t.Error("a matched route must still say why, not just what")
			}
		})
	}
}

// A ticket carrying both a documentation and a UI signal must resolve to
// exactly one actor, not silently pick whichever the map happens to iterate
// to last. routeRules is a slice specifically so this order is deterministic
// (see route.go's comment); this pins that the written order -- docs before
// UI -- is what actually runs, not just what the source says it intends.
func TestRouteDocumentationTakesPrecedenceOverUI(t *testing.T) {
	got, why := route(tracker.Issue{Labels: []string{"ui", "documentation"}})
	if got != events.ActorDocs {
		t.Fatalf("route(ui+documentation) = %q, want %q (documentation must win)", got, events.ActorDocs)
	}
	if why == "" {
		t.Error("a matched route must still say why")
	}
}

// An unlabelled ticket picks the implementer -- and says so, rather than
// falling through in silence. A silent default is how the frontend actor
// went unreached for as long as it did.
func TestRouteUnlabelledTicketDefaultsToTheImplementer(t *testing.T) {
	got, why := route(tracker.Issue{Key: "FCIA-6", Summary: "fix the rounding bug"})
	if got != events.ActorImplementer {
		t.Fatalf("route(unlabelled) = %q, want the implementer", got)
	}
	if why == "" {
		t.Fatal("the default must say why it defaulted, or the fallthrough is silent again")
	}
}

// A component or label that merely CONTAINS a keyword must not match: a
// component named "docsite-infra" is infrastructure, not a documentation
// ticket, and substring matching would route it as one.
func TestRouteDoesNotMatchOnSubstring(t *testing.T) {
	got, _ := route(tracker.Issue{Components: []string{"docsite-infra"}})
	if got != events.ActorImplementer {
		t.Fatalf("route(docsite-infra) = %q, a substring match routed a component that only contains the word", got)
	}
}

// The property that would have caught the frontend case at build time: every
// actor this file's routing table can pick is actually reachable by feeding
// it a matching ticket, and every one of them is a real, registered actor --
// not a typo that would render as a bare identifier forever.
func TestNoRoutableActorIsUnreachable(t *testing.T) {
	if len(routeRules) == 0 {
		t.Fatal("the routing table is empty")
	}
	for _, rule := range routeRules {
		if len(rule.keywords) == 0 {
			t.Fatalf("%s has no keywords to route on, so nothing can ever reach it", rule.actor)
		}
		got, _ := route(tracker.Issue{Labels: []string{rule.keywords[0]}})
		if got != rule.actor {
			t.Errorf("a ticket labelled %q routed to %q, not %q: the rule is unreachable",
				rule.keywords[0], got, rule.actor)
		}
		if a := actors.Get(rule.actor); a.Name == "" || a.Designation == "" {
			t.Errorf("%s is routable but not in the roster: %+v", rule.actor, a)
		}
	}
	// The default itself must resolve to a real actor too.
	if a := actors.Get(events.ActorImplementer); a.Name == "" || a.Designation == "" {
		t.Errorf("the default actor %s is not in the roster: %+v", events.ActorImplementer, a)
	}
}
