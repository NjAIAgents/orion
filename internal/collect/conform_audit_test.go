package collect

// Finer-grained assertions on the audit record left by the plan-conformance
// pass (OR-158). conform_test.go already checks that every outcome reaches
// the log and that the summary line tells the outcomes apart; this file
// checks the Detail map itself -- reviewed, the plan sources, and the
// divergence text -- and that a ticket with no plan and one with no model
// leave DIFFERENT reasons, not merely the same "not reviewed" headline.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// findConform returns the plan-conform event, or fails the test.
func findConform(t *testing.T, evs []events.Event) events.Event {
	t.Helper()
	for _, e := range evs {
		if e.Actor == events.ActorPlanConform {
			return e
		}
	}
	t.Fatalf("no plan-conform event in %+v", evs)
	return events.Event{}
}

func openConformLog(t *testing.T) (*events.Log, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	log, err := events.Open(path, events.Event{Key: "OR-158"})
	if err != nil {
		t.Fatal(err)
	}
	return log, path
}

// A divergence's audit record must carry reviewed=true, the plan artifact it
// was judged against, and the divergence in its own words -- an auditor
// reading this later has no other way to tell "checked and found something"
// from "checked and found nothing" or "never checked".
func TestADivergenceRecordsReviewedPlanAndDivergenceText(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})
	log, path := openConformLog(t)
	deps := Deps{Conform: func(*workspace.Workspace, string, string) (string, error) {
		return "DIVERGES: the diff adds a composite index the plan never mentioned", nil
	}}

	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{}, deps, ws, log, &bytes.Buffer{})
	log.Close()

	evs, err := events.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	e := findConform(t, evs)

	if reviewed, _ := e.Detail["reviewed"].(bool); !reviewed {
		t.Errorf("reviewed = %v, want true: %+v", e.Detail["reviewed"], e.Detail)
	}
	plan, _ := e.Detail["plan"].([]any)
	if len(plan) != 1 || plan[0] != config.ConfirmedDir+"/OR-158.md" {
		t.Errorf("plan sources = %v, want the confirmed artifact it was judged against", plan)
	}
	divs, _ := e.Detail["divergences"].([]any)
	if len(divs) != 1 || !strings.Contains(divs[0].(string), "composite index") {
		t.Errorf("divergences = %v, want the divergence in its own words", divs)
	}
}

// A conforming change is also reviewed=true and names the plan it matched --
// the only thing that must be empty is the divergence list.
func TestAConformingChangeRecordsReviewedPlanAndNoDivergences(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})
	log, path := openConformLog(t)
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
	e := findConform(t, evs)

	if reviewed, _ := e.Detail["reviewed"].(bool); !reviewed {
		t.Errorf("reviewed = %v, want true: %+v", e.Detail["reviewed"], e.Detail)
	}
	plan, _ := e.Detail["plan"].([]any)
	if len(plan) != 1 || plan[0] != config.ConfirmedDir+"/OR-158.md" {
		t.Errorf("plan sources = %v, want the confirmed artifact it was judged against", plan)
	}
	if divs, _ := e.Detail["divergences"].([]any); len(divs) != 0 {
		t.Errorf("divergences = %v, want none for a conforming change", divs)
	}
}

// A ticket with no confirmed plan and one with no model configured both read
// "not reviewed", but for different reasons -- and the record has to keep
// those reasons apart, or an auditor cannot tell a ticket nobody planned from
// one this instance simply had no model wired up for.
func TestNoPlanAndNoModelLeaveDifferentNotReviewedReasons(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"

	noPlanLog, noPlanPath := openConformLog(t)
	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{}, Deps{}, conformWS(t, branch, nil), noPlanLog, &bytes.Buffer{})
	noPlanLog.Close()

	withPlan := map[string]string{config.ConfirmedDir + "/OR-158.md": "one index per issuer"}
	noModelLog, noModelPath := openConformLog(t)
	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{}, Deps{}, conformWS(t, branch, withPlan), noModelLog, &bytes.Buffer{})
	noModelLog.Close()

	noPlanEvs, err := events.Read(noPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	noModelEvs, err := events.Read(noModelPath)
	if err != nil {
		t.Fatal(err)
	}

	noPlan := findConform(t, noPlanEvs)
	noModel := findConform(t, noModelEvs)

	for _, e := range []events.Event{noPlan, noModel} {
		if reviewed, _ := e.Detail["reviewed"].(bool); reviewed {
			t.Errorf("a pass that never asked a model was recorded as reviewed: %+v", e.Detail)
		}
	}
	if noPlan.Msg == noModel.Msg {
		t.Errorf("a ticket with no plan and a ticket with no model configured left the "+
			"same record, so an auditor cannot tell them apart: %q", noPlan.Msg)
	}
	if !strings.Contains(noPlan.Msg, "nothing was agreed") &&
		!strings.Contains(noPlan.Msg, config.ConfirmedDir) {
		t.Errorf("the no-plan record does not say a plan was missing: %q", noPlan.Msg)
	}
	if !strings.Contains(noModel.Msg, "no model") {
		t.Errorf("the no-model record does not say a model was missing: %q", noModel.Msg)
	}
}
