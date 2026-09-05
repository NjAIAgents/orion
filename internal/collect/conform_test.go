package collect

// The plan-conformance pass, from the collect side (OR-158).
//
// internal/conform owns the verdict and is tested there. What is tested here
// is the wiring, and specifically the three properties that make this pass
// different from the done triage it sits beside: it reads the CONFIRMED plan
// artifacts and not the pending one, it records what happened whether or not
// a model was asked, and it cannot stop anything.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/done"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// conformWS builds a workspace whose job worktree holds the given
// repo-relative files, so conformEvidence reads a real checkout.
func conformWS(t *testing.T, branch string, files map[string]string) *workspace.Workspace {
	t.Helper()
	dir := t.TempDir()
	tree := filepath.Join(dir, "worktrees", strings.ReplaceAll(branch, "/", "-"))
	for rel, body := range files {
		p := filepath.Join(tree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	return &workspace.Workspace{Dir: dir, Task: workspace.Task{Slug: "ledger"}}
}

func aDiff() done.Diff {
	return done.Diff{Stat: "1 file changed", Patch: "diff --git a/ledger.go b/ledger.go"}
}

// Both confirmed artifacts are read, and each is named by its own path -- a
// divergence naming no document is one nobody can re-check.
func TestTheConfirmedPlanArtifactsAreWhatTheChangeIsComparedAgainst(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		"plans/ledger.plan.md":                     "the plan: index by issuer",
		config.ConfirmedDir + "/OR-158.md":         "- Status: confirmed\n\none index per issuer",
		config.PendingDir + "/OR-158.md":           "- Status: unconfirmed\n\nshard the ledger",
		"docs/recommendations/confirmed/OR-999.md": "somebody else's decision",
	})

	ev := conformEvidence("OR-158", cfg, aDiff(), ws, branch)

	if len(ev.Plan) != 2 {
		t.Fatalf("read %d plan sources, want the plan artifact and the confirmed record: %+v",
			len(ev.Plan), ev.Plan)
	}
	var text, paths string
	for _, s := range ev.Plan {
		text += s.Text
		paths += s.Path + " "
	}
	for _, want := range []string{"plans/ledger.plan.md", config.ConfirmedDir + "/OR-158.md"} {
		if !strings.Contains(paths, want) {
			t.Errorf("%s was not read; the sources are %s", want, paths)
		}
	}
	// THE PROPERTY internal/decide EXISTS FOR. A recommendation nobody
	// answered is not a plan, and holding a change to one would enforce a
	// decision that was never made.
	if strings.Contains(text, "shard the ledger") {
		t.Error("the PENDING recommendation was read as part of the confirmed plan")
	}
	if strings.Contains(text, "somebody else's") {
		t.Error("another ticket's confirmed record was read as this ticket's plan")
	}
}

// A ticket with no confirmed plan is not one that matches its plan. The
// reason is stated, and it names the paths that were looked for -- otherwise
// a person cannot tell "there is no plan" from "the pass is broken".
func TestATicketWithNoConfirmedPlanSaysWhereItLooked(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ev := conformEvidence("OR-158", cfg, aDiff(), conformWS(t, branch, nil), branch)

	if len(ev.Plan) != 0 {
		t.Fatalf("plan artifacts appeared from nowhere: %+v", ev.Plan)
	}
	for _, want := range []string{"plans/ledger.plan.md", config.ConfirmedDir + "/OR-158.md"} {
		if !strings.Contains(ev.NoPlan, want) {
			t.Errorf("the reason does not name %s, so nobody can check where it looked: %q",
				want, ev.NoPlan)
		}
	}
}

// THE PROPERTY THE PASS RESTS ON. A divergence is reported and the run goes
// on: no label is moved, no ticket is handed back, and reviewConformance
// returns nothing a caller could branch on.
func TestADivergenceIsReportedAndBlocksNothing(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})

	jira := newTracker()
	deps := Deps{Jira: jira, Conform: func(*workspace.Workspace, string, string) (string, error) {
		return "DIVERGES: the plan says one index per issuer; the diff adds a composite one", nil
	}}

	var out bytes.Buffer
	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{}, deps, ws, nil, &out)

	if len(jira.added["OR-158"]) > 0 || len(jira.removed["OR-158"]) > 0 {
		t.Errorf("the pass moved a label (+%v -%v) -- a divergence must not gate anything",
			jira.added["OR-158"], jira.removed["OR-158"])
	}
	if jira.transitions["OR-158"] != "" {
		t.Errorf("the pass transitioned the ticket to %q", jira.transitions["OR-158"])
	}
	if len(jira.comments["OR-158"]) != 1 {
		t.Fatalf("a divergence produced %d comments, want exactly one",
			len(jira.comments["OR-158"]))
	}
	c := jira.comments["OR-158"][0]
	for _, want := range []string{"composite", "Nothing is blocked", config.ConfirmedDir} {
		if !strings.Contains(c, want) {
			t.Errorf("the comment does not carry %q:\n%s", want, c)
		}
	}
	if !strings.Contains(out.String(), "nothing is blocked") {
		t.Errorf("the console never says the run continues:\n%s", out.String())
	}
}

// A change that matches its plan is the ordinary case and gets no comment. A
// tracker with a note on every ticket is one people stop reading, which would
// cost the divergence report the attention it exists to get.
func TestAConformingChangeIsNotCommentedOn(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})
	jira := newTracker()
	deps := Deps{Jira: jira, Conform: func(*workspace.Workspace, string, string) (string, error) {
		return "CONFORMS", nil
	}}

	var out bytes.Buffer
	reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
		Options{}, deps, ws, nil, &out)

	if len(jira.comments["OR-158"]) != 0 {
		t.Errorf("a conforming change was commented on: %v", jira.comments["OR-158"])
	}
}

// The audit record is written whether or not anything was asked, because
// "checked and matched" and "never checked" are the two facts an auditor is
// telling apart later (ADR 0004: the event log is the audit trail).
func TestEveryOutcomeReachesTheAuditTrail(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	withPlan := map[string]string{config.ConfirmedDir + "/OR-158.md": "one index per issuer"}

	cases := []struct {
		name  string
		files map[string]string
		ask   func(*workspace.Workspace, string, string) (string, error)
		want  string
	}{
		{"a divergence", withPlan, func(*workspace.Workspace, string, string) (string, error) {
			return "DIVERGES: the diff adds a composite index", nil
		}, "diverges"},
		{"a match", withPlan, func(*workspace.Workspace, string, string) (string, error) {
			return "CONFORMS", nil
		}, "conforms"},
		{"no plan to check against", nil, func(*workspace.Workspace, string, string) (string, error) {
			t.Error("a model was asked about a ticket with no confirmed plan")
			return "", nil
		}, "not reviewed"},
		{"no model configured", withPlan, nil, "not reviewed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log, err := events.Open(filepath.Join(dir, "events.jsonl"),
				events.Event{Key: "OR-158"})
			if err != nil {
				t.Fatal(err)
			}
			var deps Deps
			if tc.ask != nil {
				deps.Conform = tc.ask
			}
			reviewConformance("OR-158", PR{URL: "http://pr/1"}, aDiff(), cfg, branch,
				Options{}, deps, conformWS(t, branch, tc.files), log, &bytes.Buffer{})
			log.Close()

			evs, err := events.Read(filepath.Join(dir, "events.jsonl"))
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
				t.Fatalf("nothing reached the audit trail: %+v", evs)
			}
			if !strings.Contains(found.Msg, tc.want) {
				t.Errorf("the record says %q, want it to say %q", found.Msg, tc.want)
			}
			// Which artifacts it was judged against, so the finding can be
			// re-checked later against the same documents.
			if _, ok := found.Detail["plan"]; !ok {
				t.Errorf("the record does not name the plan it was judged against: %+v", found.Detail)
			}
		})
	}
}
