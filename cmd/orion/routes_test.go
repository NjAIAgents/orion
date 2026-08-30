package main

// OR-191: the routing vocabulary is a published contract. These tests hold
// `orion routes` to actually publishing it -- every rule, every keyword, and
// the actors deliberately left out -- and hold `orion queue` to reporting
// where the work in front of it would go.
//
// Assertions are on job titles and keywords, never on an actor's NAME: a
// name belongs to internal/actors and writing one here is exactly the leak
// TestNoDefaultNameAppearsOutsideTheRegistry exists to catch.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/work"
)

// Every rule and every one of its keywords is printed. A table that lists
// the actors but abbreviates the vocabulary is not a contract a planner can
// apply -- it would have to guess which spellings are accepted, which is
// where a second, drifting copy comes from.
func TestRoutesPrintsEveryRuleAndEveryKeyword(t *testing.T) {
	actors.Reset()
	var buf bytes.Buffer
	printRoutes(&buf)
	out := buf.String()

	for _, r := range work.Rules() {
		if d := actors.Get(r.Actor).Designation; !strings.Contains(out, d) {
			t.Errorf("routable actor %q (%s) is missing from the table", r.Actor, d)
		}
		for _, kw := range r.Keywords {
			if !strings.Contains(out, kw) {
				t.Errorf("keyword %q of %s is not printed: a planner cannot set a marker "+
					"the contract does not name", kw, r.Actor)
			}
		}
	}
	if d := actors.Get(work.DefaultActor).Designation; !strings.Contains(out, d) {
		t.Errorf("the default actor (%s) is missing: an unmarked ticket's destination "+
			"is part of the contract", d)
	}
	if !strings.Contains(out, "default") {
		t.Error("nothing in the output says which entry is the default")
	}
}

// The other half of the decision. An actor absent from the output is
// indistinguishable from one nobody has thought about, which is the state
// OR-171 found the frontend developer in.
func TestRoutesPrintsWhyTheRestAreNotRoutable(t *testing.T) {
	actors.Reset()
	var buf bytes.Buffer
	printRoutes(&buf)
	out := buf.String()

	for _, o := range work.OtherPaths() {
		if d := actors.Get(o.Actor).Designation; !strings.Contains(out, d) {
			t.Errorf("%q (%s) is excluded from routing and the output never says so",
				o.Actor, d)
		}
		if !strings.Contains(out, o.Why) {
			t.Errorf("%q is listed without the path that reaches it instead", o.Actor)
		}
	}
}

// The two properties OR-171 fixed and OR-191 must not undo: matching is by
// equality, and the marker is set when the ticket is created rather than
// inferred later. Both are stated where the planner reads them.
func TestRoutesStatesEqualityMatchingAndWhereTheMarkerIsSet(t *testing.T) {
	actors.Reset()
	var buf bytes.Buffer
	printRoutes(&buf)
	out := buf.String()

	if !strings.Contains(out, "docsite-infra") {
		t.Error("the output does not state that matching is equality, not containment")
	}
	if !strings.Contains(out, "created") {
		t.Error("the output does not say the marker is set at ticket creation, which is " +
			"the only place it can be set")
	}
}

// `orion queue` reports the split before the run, not after the bill.
func TestRoutingSummaryReportsTheSplit(t *testing.T) {
	actors.Reset()
	summary, hint := routingSummary([]tracker.Issue{
		{Key: "OR-1"},
		{Key: "OR-2", Labels: []string{"documentation"}},
		{Key: "OR-3", Components: []string{"ui"}},
	})
	for _, want := range []string{
		"1 " + actors.Display(work.DefaultActor),
		"1 " + actors.Display(events.ActorDocs),
		"1 " + actors.Display(events.ActorFrontend),
		"(default)",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("routing summary %q is missing %q", summary, want)
		}
	}
	if hint != "" {
		t.Errorf("a routed queue got the all-default hint: %q", hint)
	}
}

// The case the report exists for: 100% default is either correct or a
// planning failure, and the hint names the command that tells them apart.
// It is a hint and not a warning -- all-default is right often enough that
// crying wolf would train the reader to skip the line.
func TestRoutingSummaryHintsWhenEveryTicketDefaults(t *testing.T) {
	actors.Reset()
	summary, hint := routingSummary([]tracker.Issue{
		{Key: "OR-1"}, {Key: "OR-2"}, {Key: "OR-3"},
	})
	if !strings.Contains(summary, "3 "+actors.Display(work.DefaultActor)) {
		t.Errorf("an all-default queue reported %q", summary)
	}
	if !strings.Contains(hint, "orion routes") {
		t.Errorf("the all-default hint does not point at the published table: %q", hint)
	}
	for _, shouty := range []string{"WARNING", "failed", "error"} {
		if strings.Contains(hint, shouty) {
			t.Errorf("the hint reads as a failure (%q): all-default is often correct", shouty)
		}
	}
}

// Nothing queued means nothing to report. A "routing:" line over an empty
// queue is noise on the one run where the reader already knows the answer.
func TestRoutingSummaryIsSilentOnAnEmptyQueue(t *testing.T) {
	if summary, hint := routingSummary(nil); summary != "" || hint != "" {
		t.Errorf("empty queue reported %q / %q", summary, hint)
	}
}
