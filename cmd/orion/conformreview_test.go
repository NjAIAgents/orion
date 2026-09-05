package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

// The plan-conformance pass runs beside done triage on the same green run,
// so the one thing that must not happen is the two being indistinguishable
// afterwards. Its own actor is what puts its spend on its own row of the
// ticket's cost report and its findings under its own name in the event log;
// borrowing done triage's would merge two verdicts into one that names
// neither (OR-158).
func TestPlanConformanceRunsAsItsOwnActorOnItsOwnModel(t *testing.T) {
	o := conformOptions("OR-1", "the prompt")

	if o.Actor != events.ActorPlanConform {
		t.Errorf("actor = %q, want %q, or its spend and its findings are attributed "+
			"to the pass sitting next to it", o.Actor, events.ActorPlanConform)
	}
	if o.Actor == events.ActorDoneTriage {
		t.Error("plan conformance is running as done triage; the two answer different " +
			"questions and only one of them may hand work back")
	}
	if o.Key != "OR-1" {
		t.Errorf("key = %q, want the ticket it read", o.Key)
	}
	if want := actors.Model(events.ActorPlanConform); o.Model != want {
		t.Errorf("model = %q, want the roster's %q", o.Model, want)
	}
	if o.Prompt != "the prompt" {
		t.Errorf("prompt = %q, want the one it was given", o.Prompt)
	}
}

// Bounded like the reading pass it is, not like a run that changes something.
// It is handed the plan and the diff in its prompt and asked one question.
func TestPlanConformanceIsBoundedTightly(t *testing.T) {
	o := conformOptions("OR-1", "p")
	if o.MaxMinutes != conformMaxMinutes || o.MaxTurns != conformMaxTurns {
		t.Errorf("bounds = %d min / %d turns, want %d/%d",
			o.MaxMinutes, o.MaxTurns, conformMaxMinutes, conformMaxTurns)
	}
	if o.MaxTurns > 20 {
		t.Errorf("%d turns is an exploration budget, not a reading one", o.MaxTurns)
	}
}

// The stage name reaches the log, so a reader can tell this run apart from
// the done triage that ran on the same commit a moment earlier.
func TestPlanConformanceNamesItsStage(t *testing.T) {
	o := conformOptions("OR-1", "p")
	if !strings.Contains(o.Stage, "conform") {
		t.Errorf("stage = %q, which does not say what this run is", o.Stage)
	}
	if o.Stage == doneOptions("OR-1", "p").Stage {
		t.Error("both passes report under the same stage name")
	}
}
