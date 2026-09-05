package main

// Stronger assertions on the plan-conformance run's identity and bounds
// (OR-158) than conformreview_test.go's own: that it reads its model from
// ITS roster entry even when done triage's differs, rather than merely
// matching a call to the same lookup function, and that its bounds are the
// exact 5-minute / 10-turn reading budget rather than some value that
// happens to be under an exploration threshold.

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// The model has to come from the roster's OWN plan-conform entry. Configuring
// done triage's model to something else must not leak into this pass, and
// configuring this pass's model must not leak into done triage's -- otherwise
// the two rows of the cost report that are supposed to name two different
// actors would in fact be paying for whichever one was configured last.
func TestPlanConformanceModelComesFromItsOwnRosterEntryNotDoneTriages(t *testing.T) {
	t.Cleanup(actors.Reset)
	if err := actors.Configure(map[string]config.Agent{
		"plan-conform": {Model: "haiku"},
		"done-triage":  {Model: "opus"},
	}); err != nil {
		t.Fatal(err)
	}

	conformModel := conformOptions("OR-1", "p").Model
	doneModel := doneOptions("OR-1", "p").Model

	if conformModel != "haiku" {
		t.Errorf("plan-conform model = %q, want the roster's plan-conform entry (haiku)", conformModel)
	}
	if doneModel != "opus" {
		t.Errorf("done-triage model = %q, want the roster's done-triage entry (opus), "+
			"not the plan-conform one it sits beside", doneModel)
	}
	if conformModel == doneModel {
		t.Fatalf("both passes ran on %q even though they were configured with different models", conformModel)
	}
}

// The actor attribution has to survive the same configuration swap: this
// pass's Actor is always plan-conform, whatever either roster entry's model
// is set to, because it is the identifier -- not the model -- that separates
// its row of the cost report and its findings in the event log from done
// triage's.
func TestPlanConformanceActorIdentityIsUnaffectedByModelConfiguration(t *testing.T) {
	t.Cleanup(actors.Reset)
	if err := actors.Configure(map[string]config.Agent{
		"plan-conform": {Model: "haiku"},
		"done-triage":  {Model: "haiku"},
	}); err != nil {
		t.Fatal(err)
	}

	o := conformOptions("OR-1", "p")
	if o.Actor != events.ActorPlanConform {
		t.Errorf("actor = %q, want %q", o.Actor, events.ActorPlanConform)
	}
	if o.Actor == events.ActorDoneTriage {
		t.Error("plan conformance is attributed as done triage")
	}
}

// The exact reading budget: 5 minutes, 10 turns. Not merely "under some
// exploration threshold" -- the brief for this pass names these two numbers,
// and a change to either is a change worth this test failing over.
func TestPlanConformanceBoundsAreExactlyFiveMinutesTenTurns(t *testing.T) {
	o := conformOptions("OR-1", "p")
	if o.MaxMinutes != 5 {
		t.Errorf("MaxMinutes = %d, want 5", o.MaxMinutes)
	}
	if o.MaxTurns != 10 {
		t.Errorf("MaxTurns = %d, want 10", o.MaxTurns)
	}
}
