package collect

// The event reviewConformance actually emits has to name plan-conform, not
// done triage, as its actor and carry plan-conform's own model (OR-158) --
// conform_audit_test.go already checks the Detail map this event carries;
// this checks the Actor and Model columns that put its spend and findings on
// their own row of the cost report and the event log, separate from the done
// triage pass sitting beside it.

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func TestTheConformEventIsAttributedToItsOwnActorAndModel(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	log, err := events.Open(path, events.Event{Key: "OR-158"})
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Conform: func(*workspace.Workspace, string, string) (string, error) {
		return "CONFORMS", nil
	}}

	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{}, deps, ws, log, &bytes.Buffer{})
	log.Close()

	evs, err := events.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	var found *events.Event
	for i, e := range evs {
		if e.Actor == events.ActorPlanConform {
			found = &evs[i]
		}
	}
	if found == nil {
		t.Fatalf("no plan-conform event in %+v", evs)
	}
	if found.Actor == events.ActorDoneTriage {
		t.Error("the conformance event was attributed to done triage")
	}
	if want := actors.Model(events.ActorPlanConform); found.Model != want {
		t.Errorf("event model = %q, want the plan-conform roster entry %q", found.Model, want)
	}

	for _, e := range evs {
		if e.Actor == events.ActorDoneTriage {
			t.Errorf("this pass, which never runs done triage, still produced a "+
				"done-triage event: %+v -- the two rows would merge into one that names neither", e)
		}
	}
}
