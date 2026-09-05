package collect

// Two properties of how the plan-conformance pass presents itself, as
// distinct from what it finds (OR-158): it must read as its own stage rather
// than as the done triage it runs beside, and a dry run must say what it
// would check without checking it.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The plan-conform pass and done triage read the same commit a moment apart,
// so the one thing that must not happen is a console reader being unable to
// tell which of the two produced a given line. Console identity comes from
// the actor's own display name (internal/ui renders it via actors.Display),
// and events.ActorPlanConform is its own actor precisely so this holds.
func TestPlanConformanceReadsDistinctlyFromDoneTriageOnTheConsole(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})
	deps := Deps{Conform: func(*workspace.Workspace, string, string) (string, error) {
		return "CONFORMS", nil
	}}

	var out bytes.Buffer
	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{}, deps, ws, nil, &out)

	got := out.String()
	planConform := actors.Display(events.ActorPlanConform)
	if !strings.Contains(got, planConform) {
		t.Errorf("console output never names the plan-conform actor %q:\n%s", planConform, got)
	}
	if doneTriage := actors.Display(events.ActorDoneTriage); strings.Contains(got, doneTriage) {
		t.Errorf("the plan-conform line reads as done triage (%q):\n%s", doneTriage, got)
	}
}

// A dry run reports what it would check and asks nothing -- no model call,
// no ticket comment, no audit record -- because "would check" and "checked"
// are different facts and a dry run must not leave the second one behind.
func TestDryRunSaysWhatWouldBeCheckedAndChangesNothing(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})
	jira := newTracker()
	deps := Deps{Jira: jira, Conform: func(*workspace.Workspace, string, string) (string, error) {
		t.Error("a dry run asked a model to compare the change against the plan")
		return "", nil
	}}

	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	log, err := events.Open(path, events.Event{Key: "OR-158"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{DryRun: true}, deps, ws, log, &out)
	log.Close()

	got := out.String()
	if !strings.Contains(got, "OR-158") || !strings.Contains(strings.ToLower(got), "would") {
		t.Errorf("a dry run does not say what it would check:\n%s", got)
	}
	if len(jira.comments["OR-158"]) != 0 {
		t.Errorf("a dry run left a comment on the ticket: %v", jira.comments["OR-158"])
	}

	evs, err := events.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Actor == events.ActorPlanConform {
			t.Errorf("a dry run wrote an audit record, but nothing was actually checked: %+v", e)
		}
	}
}
