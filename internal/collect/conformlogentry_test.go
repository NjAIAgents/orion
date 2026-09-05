package collect

// What the audit record itself has to carry, end to end through
// reviewConformance rather than through internal/conform's own Verdict tests
// (OR-158): reviewed, the plan it was judged against, the divergences in
// their own words, and a one-line summary a reader does not have to
// reconstruct from the rest of the fields. And, for the ticket with nothing
// to check, that the note names BOTH places that were looked for -- the task
// plan and the confirmed recommendation -- so a person can verify nothing is
// there rather than take the pass's word for it.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func conformLog(t *testing.T) (*events.Log, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	log, err := events.Open(path, events.Event{Key: "OR-158"})
	if err != nil {
		t.Fatal(err)
	}
	return log, path
}

func planConformEvent(t *testing.T, path string) events.Event {
	t.Helper()
	evs, err := events.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Actor == events.ActorPlanConform {
			return e
		}
	}
	t.Fatalf("no plan-conform entry in the log")
	return events.Event{}
}

// A reader of the event log should not have to cross-reference Detail against
// Msg to learn what happened: the entry names whether the question was put to
// a model, what it was judged against, what it found, and says so in one
// sentence too.
func TestLogEntryCarriesReviewedPlanDivergencesAndASummaryLine(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})

	cases := []struct {
		name       string
		reply      string
		wantReview bool
		wantDivs   int
		summary    string
	}{
		{"diverges", "DIVERGES: the plan says one index; the diff adds a composite one",
			true, 1, "diverges from the confirmed plan"},
		{"conforms", "CONFORMS", true, 0, "conforms to the confirmed plan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, path := conformLog(t)
			deps := Deps{Conform: func(*workspace.Workspace, string, string) (string, error) {
				return tc.reply, nil
			}}
			reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
				Options{}, deps, ws, log, &bytes.Buffer{})
			log.Close()

			e := planConformEvent(t, path)
			if reviewed, _ := e.Detail["reviewed"].(bool); reviewed != tc.wantReview {
				t.Errorf("reviewed = %v, want %v: %+v", e.Detail["reviewed"], tc.wantReview, e.Detail)
			}
			plan, _ := e.Detail["plan"].([]any)
			if len(plan) != 1 || plan[0] != config.ConfirmedDir+"/OR-158.md" {
				t.Errorf("plan = %v, want the confirmed artifact it was judged against", plan)
			}
			divs, _ := e.Detail["divergences"].([]any)
			if len(divs) != tc.wantDivs {
				t.Errorf("divergences = %v, want %d entries", divs, tc.wantDivs)
			}
			if !strings.Contains(e.Msg, tc.summary) {
				t.Errorf("Msg = %q, want it to carry the summary statement %q", e.Msg, tc.summary)
			}
		})
	}
}

// A ticket with no confirmed plan is recorded with a note naming BOTH places
// that were searched -- the task's own plan artifact and this ticket's
// confirmed recommendation -- so a person can go look and confirm neither
// exists, rather than trust that the pass looked in the right place.
func TestNoPlanEntryNamesBothPathsThatWereSearched(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, nil) // no plan artifact, no confirmed record

	log, path := conformLog(t)
	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{}, Deps{}, ws, log, &bytes.Buffer{})
	log.Close()

	e := planConformEvent(t, path)
	if reviewed, _ := e.Detail["reviewed"].(bool); reviewed {
		t.Errorf("a ticket with nothing to check was recorded as reviewed: %+v", e.Detail)
	}
	// ws.Task.Slug is "ledger" (conformWS), so the task plan the pass looked
	// for is plans/ledger.plan.md.
	for _, want := range []string{"plans/ledger.plan.md", config.ConfirmedDir + "/OR-158.md"} {
		if !strings.Contains(e.Msg, want) {
			t.Errorf("the no-plan record does not name %q, so nobody can verify nothing "+
				"is there: %q", want, e.Msg)
		}
	}
}
