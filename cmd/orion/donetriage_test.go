package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

// The done-triage pass runs on every ticket whose checks go green, so its
// spend has to land on that ticket's own cost report rather than being
// absorbed into whatever else was running. Actor and Key are what make that
// happen -- the supervisor writes the usage line from them -- and an
// unattributed run is money nothing can account for.
func TestDoneTriageRunsAsItsOwnActorOnItsOwnModel(t *testing.T) {
	o := doneOptions("OR-1", "the prompt")

	if o.Actor != events.ActorDoneTriage {
		t.Errorf("actor = %q, want %q, or its spend is attributed to somebody else",
			o.Actor, events.ActorDoneTriage)
	}
	if o.Key != "OR-1" {
		t.Errorf("key = %q, want the ticket it read", o.Key)
	}
	if want := actors.Model(events.ActorDoneTriage); o.Model != want {
		t.Errorf("model = %q, want the roster's %q", o.Model, want)
	}
	if o.Prompt != "the prompt" {
		t.Errorf("prompt = %q, want the one it was given", o.Prompt)
	}
}

// Bounded far tighter than a run that changes something. It is handed the
// ticket and the diff in its prompt and asked one question with a one-line
// answer; a run still going after ten turns has stopped being a second
// reading and started exploring the repository.
func TestDoneTriageIsBoundedTightly(t *testing.T) {
	o := doneOptions("OR-1", "p")
	if o.MaxMinutes != doneMaxMinutes || o.MaxTurns != doneMaxTurns {
		t.Errorf("bounds = %d min / %d turns, want %d/%d",
			o.MaxMinutes, o.MaxTurns, doneMaxMinutes, doneMaxTurns)
	}
	if o.MaxTurns > 20 {
		t.Errorf("%d turns is an exploration budget, not a reading one", o.MaxTurns)
	}
}

// The stage name reaches the log, so a reader can tell this run apart from
// the fix loop and the QA stage that also spend on a ticket.
func TestDoneTriageNamesItsStage(t *testing.T) {
	if o := doneOptions("OR-1", "p"); !strings.Contains(o.Stage, "done") {
		t.Errorf("stage = %q, which does not say what this run is", o.Stage)
	}
}
