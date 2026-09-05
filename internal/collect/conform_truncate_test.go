package collect

// Truncation on the evidence the conformance pass builds (OR-158). conform.go
// caps the plan text at maxPlan and carries the diff's own Truncated flag
// through unexamined; supervisorConformPrompt then has to fold BOTH into the
// one declaration the model reads, or a plan clause cut for budget reasons
// reads to the agent as one the change ignored rather than one it never saw.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/conform"
	"github.com/orion-sdlc/orion/internal/done"
)

// Plan text past maxPlan is cut to the budget and the cut is declared, for
// the same reason a truncated diff is: a clause the model cannot find because
// it was cut off is missing evidence, not a change that ignored it.
func TestConformEvidenceTruncatesAnOversizedPlanAndSaysSo(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	big := strings.Repeat("x", maxPlan+500)
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": big,
	})

	ev := conformEvidence("OR-158", cfg, aDiff(), ws, branch)

	if len(ev.Plan) != 1 {
		t.Fatalf("read %d plan sources, want the one oversized artifact: %+v", len(ev.Plan), ev.Plan)
	}
	s := ev.Plan[0]
	if !s.Truncated {
		t.Error("a plan artifact over maxPlan chars was not marked truncated")
	}
	if len(s.Text) != maxPlan {
		t.Errorf("plan text length = %d, want exactly maxPlan (%d)", len(s.Text), maxPlan)
	}
}

// The diff's own Truncated flag -- set upstream by done triage's readDiff
// once the patch passes ITS budget -- has to survive conformEvidence
// unchanged, or the conformance prompt would tell the model the diff is
// complete when it is not.
func TestConformEvidenceCarriesTheDiffsTruncatedFlagThrough(t *testing.T) {
	cfg := config.Config{}
	cfg.Paths.Plans = "plans"
	branch := "orion/or-158"
	ws := conformWS(t, branch, map[string]string{
		config.ConfirmedDir + "/OR-158.md": "one index per issuer",
	})
	d := done.Diff{Stat: "1 file changed", Patch: "diff --git a/ledger.go b/ledger.go", Truncated: true}

	ev := conformEvidence("OR-158", cfg, d, ws, branch)

	if !ev.Diff.Truncated {
		t.Error("a diff already truncated upstream lost its Truncated flag on its way into conform.Evidence")
	}
}

// The prompt has to declare truncation whichever side it came from: a plan
// artifact cut for budget, or a diff cut upstream. Missing either would leave
// the model treating cut evidence as the whole thing.
func TestSupervisorConformPromptDeclaresTruncationFromEitherSource(t *testing.T) {
	complete := conform.Evidence{
		Key:  "OR-158",
		Plan: []conform.Source{{Path: "plans/ledger.plan.md", Text: "index by issuer"}},
		Diff: conform.Diff{Stat: "1 file changed", Patch: "diff --git a/x b/x"},
	}
	if strings.Contains(supervisorConformPrompt(complete), "TRUNCATED") {
		t.Error("complete plan and diff were announced as truncated")
	}

	planTruncated := complete
	planTruncated.Plan = []conform.Source{{Path: "plans/ledger.plan.md", Text: "index by issuer", Truncated: true}}
	if !strings.Contains(supervisorConformPrompt(planTruncated), "TRUNCATED") {
		t.Error("a plan source cut for budget was not declared truncated in the prompt")
	}

	diffTruncated := complete
	diffTruncated.Diff.Truncated = true
	if !strings.Contains(supervisorConformPrompt(diffTruncated), "TRUNCATED") {
		t.Error("a diff cut upstream was not declared truncated in the prompt")
	}
}
