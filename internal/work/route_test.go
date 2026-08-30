package work

// OR-171: there was no route from a ticket's labels, components or issue
// type to the actor that works it -- every ticket, of every kind, landed on
// the implementer. These tests are the ones the ticket asks for by name: a
// labelled ticket picks its actor, an unlabelled one defaults to the
// implementer, and no actor this file knows how to route to is unreachable.

import (
	"strings"
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
			got, why := Route(c.issue)
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
	got, why := Route(tracker.Issue{Labels: []string{"ui", "documentation"}})
	if got != events.ActorDocs {
		t.Fatalf("route(ui+documentation) = %q, want %q (documentation must win)", got, events.ActorDocs)
	}
	if why == "" {
		t.Error("a matched route must still say why")
	}
}

// Precedence is the WRITTEN order, for every pair in the table and not only
// for docs-before-UI. The table grew from two rules to four in OR-191, and a
// property that held for the one pair anybody wrote a test for would say
// nothing about the other five.
func TestPrecedenceIsTheWrittenOrderForEveryPair(t *testing.T) {
	rules := Rules()
	for i, first := range rules {
		for _, second := range rules[i+1:] {
			// Second marker first in the label list, so a route that
			// resolved by the ticket's field order rather than the table's
			// would return the wrong actor.
			issue := tracker.Issue{Labels: []string{second.Keywords[0], first.Keywords[0]}}
			if got, _ := Route(issue); got != first.Actor {
				t.Errorf("a ticket carrying %q and %q routed to %q; %s is written first "+
					"and must win", second.Keywords[0], first.Keywords[0], got, first.Actor)
			}
		}
	}
}

// An unlabelled ticket picks the implementer -- and says so, rather than
// falling through in silence. A silent default is how the frontend actor
// went unreached for as long as it did.
func TestRouteUnlabelledTicketDefaultsToTheImplementer(t *testing.T) {
	got, why := Route(tracker.Issue{Key: "FCIA-6", Summary: "fix the rounding bug"})
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
	got, _ := Route(tracker.Issue{Components: []string{"docsite-infra"}})
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
		if len(rule.Keywords) == 0 {
			t.Fatalf("%s has no keywords to route on, so nothing can ever reach it", rule.Actor)
		}
		got, _ := Route(tracker.Issue{Labels: []string{rule.Keywords[0]}})
		if got != rule.Actor {
			t.Errorf("a ticket labelled %q routed to %q, not %q: the rule is unreachable",
				rule.Keywords[0], got, rule.Actor)
		}
		if a := actors.Get(rule.Actor); a.Name == "" || a.Designation == "" {
			t.Errorf("%s is routable but not in the roster: %+v", rule.Actor, a)
		}
	}
	// The default itself must resolve to a real actor too.
	if a := actors.Get(events.ActorImplementer); a.Name == "" || a.Designation == "" {
		t.Errorf("the default actor %s is not in the roster: %+v", events.ActorImplementer, a)
	}
}

// OR-191: every marker the published table names reaches its actor from ALL
// THREE places a ticket can carry it. Routing reads the issue type, the
// components and the labels, and a planner told "set the marker" picks
// whichever of the three its tracker makes easy -- so a keyword that worked
// as a label and not as a component would be a contract that is true only
// where it happened to be tested.
func TestEveryKeywordRoutesFromTypeComponentAndLabel(t *testing.T) {
	for _, rule := range Rules() {
		for _, kw := range rule.Keywords {
			carriers := map[string]tracker.Issue{
				"issue type": {IssueType: kw},
				"component":  {Components: []string{kw}},
				"label":      {Labels: []string{kw}},
			}
			for where, issue := range carriers {
				got, why := Route(issue)
				if got != rule.Actor {
					t.Errorf("%q as an %s routed to %q, want %q", kw, where, got, rule.Actor)
				}
				if why == "" {
					t.Errorf("%q as an %s matched but said nothing about why", kw, where)
				}
			}
		}
	}
}

// The table is only worth publishing if it is bigger than the default plus a
// rounding error. Two rules against an eleven-actor roster is what made
// "plan accordingly" meaningless (OR-191), and a later cleanup that trimmed
// it back would restore that quietly.
func TestArchitectAndPMAreReachableFromATicket(t *testing.T) {
	for _, c := range []struct {
		marker string
		want   string
	}{
		{"architecture", events.ActorArchitect},
		{"adr", events.ActorArchitect},
		{"product", events.ActorPM},
		{"requirements", events.ActorPM},
	} {
		if got, _ := Route(tracker.Issue{Labels: []string{c.marker}}); got != c.want {
			t.Errorf("a ticket labelled %q routed to %q, want %q", c.marker, got, c.want)
		}
	}
}

// Not being routable is a DECISION with a reason, not an oversight. Adding an
// actor to the roster must force that decision rather than silently landing
// it in the unreachable set, which is the state OR-171 found the frontend
// developer in.
func TestEveryActorIsRoutableOrHasARecordedReasonNotToBe(t *testing.T) {
	accounted := map[string]string{DefaultActor: "the default"}
	for _, r := range Rules() {
		accounted[r.Actor] = "routable"
	}
	for _, o := range OtherPaths() {
		if strings.TrimSpace(o.Why) == "" {
			t.Errorf("%s is excluded from the table with no reason recorded", o.Actor)
		}
		if _, dup := accounted[o.Actor]; dup {
			t.Errorf("%s is both routable and listed as reached another way", o.Actor)
		}
		accounted[o.Actor] = "reached another way"
		if got, _ := Route(tracker.Issue{Labels: []string{o.Actor}}); got == o.Actor {
			t.Errorf("%s is documented as not routable but a %q label reaches it anyway",
				o.Actor, o.Actor)
		}
	}
	for _, id := range actors.ConfigurableIDs() {
		// Orion is the supervisor running the ticket, not an agent that
		// works one; it is the only configurable identifier that is neither.
		if id == events.ActorOrion {
			continue
		}
		if _, ok := accounted[id]; !ok {
			t.Errorf("%s is in the roster but neither routable nor recorded in otherPaths: "+
				"decide which, so the exclusion is a choice rather than an omission", id)
		}
	}
}

// Rules is a published contract, so a caller must not be able to edit the
// table it prints. `orion routes` reads this on every invocation.
func TestRulesIsACopy(t *testing.T) {
	got := Rules()
	if len(got) == 0 {
		t.Fatal("the published table is empty")
	}
	got[0].Actor = "tampered"
	got[0].Keywords[0] = "tampered"
	if again := Rules(); again[0].Actor == "tampered" || again[0].Keywords[0] == "tampered" {
		t.Error("Rules hands out the live table: a caller can rewrite routing by editing it")
	}
}

// OR-191: `orion queue` reports where the work would go before it runs, so a
// queue that is entirely default is distinguishable from a routed one at the
// point the distinction is still worth something.
func TestDistributionCountsEachActorInTableOrder(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "OR-1"},
		{Key: "OR-2", Labels: []string{"ui"}},
		{Key: "OR-3", Labels: []string{"documentation"}},
		{Key: "OR-4"},
		{Key: "OR-5", Components: []string{"ui"}},
	}
	got := Distribution(issues)
	want := []Tally{
		{events.ActorDocs, 1},
		{events.ActorFrontend, 2},
		{DefaultActor, 2},
	}
	if len(got) != len(want) {
		t.Fatalf("Distribution = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Distribution[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// The default's share is reported even when it is all of it -- that is
	// the case the report exists for.
	all := Distribution([]tracker.Issue{{Key: "OR-6"}, {Key: "OR-7"}})
	if len(all) != 1 || all[0] != (Tally{DefaultActor, 2}) {
		t.Errorf("an all-default queue reported %+v, want a single tally of 2", all)
	}
	if n := Distribution(nil); len(n) != 0 {
		t.Errorf("an empty queue reported %+v, want nothing", n)
	}
}

// The tone of the default's announcement, fixed in OR-191. It is a normal
// outcome on the happy path and must not read as a failure of the run -- but
// it must still be said, which is OR-171's rule and outranks the wording.
func TestTheDefaultAnnouncesWithoutReadingAsAMiss(t *testing.T) {
	_, why := Route(tracker.Issue{Key: "OR-191", Summary: "fix the rounding bug"})
	if why == "" {
		t.Fatal("the default must say why it defaulted")
	}
	for _, banned := range []string{"no issue type, component or label matched", "matched a route"} {
		if strings.Contains(why, banned) {
			t.Errorf("the default still phrases a normal outcome as a miss: %q", why)
		}
	}
	if !strings.Contains(why, "marker") {
		t.Errorf("the default should name what the ticket is missing, got %q", why)
	}
}
