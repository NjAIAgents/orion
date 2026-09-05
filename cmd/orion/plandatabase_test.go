package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/dbaplan"
	"github.com/orion-sdlc/orion/internal/events"
)

// A selected actor with nothing to invoke it is a name on a list. The
// database architect's planning step is not in the fixed chain -- a project
// that stores nothing should not pay for it -- so the roster is where anybody
// finds out it exists (OR-154).
func TestPlanRosterNamesTheCommandThatRunsTheDatabaseStep(t *testing.T) {
	t.Cleanup(actors.Reset)
	var buf bytes.Buffer
	printPlanRoster(&buf, "payments-api", "Take card payments, with a database behind it.")
	got := buf.String()

	want := "orion run payments-api --stage " + dbaplan.Stage
	if !strings.Contains(got, want) {
		t.Errorf("the roster selects the database architect and never says how to run it;\n"+
			"want %q in:\n%s", want, got)
	}
}

// The command is printed for the actor that has a step, and not for one that
// does not -- an actor announced with a command that does nothing is worse
// than one announced with none.
func TestPlanRosterNamesNoCommandForAnActorWithoutAStep(t *testing.T) {
	t.Cleanup(actors.Reset)
	var buf bytes.Buffer
	printPlanRoster(&buf, "payments-api", "Take card payments from the web app.")
	if strings.Contains(buf.String(), "--stage") {
		t.Errorf("a run that selected nobody printed a stage command:\n%s", buf.String())
	}
}

// The step is the database architect's, and the stage name is the one
// `orion run` routes on. A rename on one side alone leaves the roster
// pointing at a stage the runner refuses by name.
func TestTheDatabaseStepBelongsToTheDatabaseArchitect(t *testing.T) {
	if planActorStages[events.ActorDBA] != dbaplan.Stage {
		t.Errorf("the database step is announced as %q, not %q",
			planActorStages[events.ActorDBA], dbaplan.Stage)
	}
	for _, s := range planStages {
		if s.Stage == dbaplan.Stage {
			t.Errorf("the database step is in the fixed chain, so every project pays " +
				"for it whether or not it stores anything")
		}
	}
}
