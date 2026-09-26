package ciscaffold

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// specWith is a spec carrying an Agentic design section whose eval plan is
// the given JSON -- the shape /agentic-design writes.
func specWith(plan string) string {
	return "# Spec\n\nSome requirements.\n\n## Agentic design\n\n### Loop\n\nBounded.\n\n" +
		"### Eval plan\n\nCases below.\n\n```json\n" + plan + "\n```\n\n### Harness\n\nFakes.\n\n" +
		"## Out of scope\n\n```json\n{\"not\": \"this one\"}\n```\n"
}

const goodPlan = `{
  "gate": {"pass_rate": 0.9, "min_cases": 20},
  "harness": {"command": "cat", "replays_against": "echo, for the test"},
  "cases": [
    {"name": "names-the-cause", "prompt": "disk full on db-1", "must_contain": ["disk"],
     "discriminates": "an agent that blames the network"},
    {"name": "no-ticket-spam", "prompt": "one error", "must_not_contain": ["opened 5 tickets"],
     "discriminates": "an agent that opens a ticket per log line"},
    {"name": "placeholder", "prompt": "anything", "discriminates": "nothing, really"},
    {"name": "Bad Name", "prompt": "x", "must_contain": ["x"], "discriminates": "y"}
  ]
}`

func TestParseFindsThePlanInsideTheAgenticDesignSection(t *testing.T) {
	p, err := ParseEvalPlan([]byte(specWith(goodPlan)))
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || len(p.Cases) != 4 || p.Harness.Command != "cat" || p.Gate.MinCases != 20 {
		t.Fatalf("parsed the wrong block or lost fields: %+v", p)
	}
}

func TestNoSectionIsNotAnError(t *testing.T) {
	for _, spec := range []string{
		"# Spec\n\nA REST service.\n",
		"# Spec\n\n## Agentic design\n\n### Loop\n\nNo eval plan yet.\n",
		// An eval plan outside the section is somebody else's JSON.
		"# Spec\n\n### Eval plan\n\n```json\n{\"cases\": []}\n```\n",
	} {
		p, err := ParseEvalPlan([]byte(spec))
		if p != nil || err != nil {
			t.Errorf("want nil, nil for %q; got %+v, %v", spec, p, err)
		}
	}
}

func TestMalformedPlanIsReported(t *testing.T) {
	if _, err := ParseEvalPlan([]byte(specWith(`{"cases": [`))); err == nil {
		t.Error("broken JSON parsed as a plan")
	}
	spec := "## Agentic design\n\n### Eval plan\n\nno fence here\n"
	if _, err := ParseEvalPlan([]byte(spec)); err == nil {
		t.Error("an Eval plan heading with no json block was accepted silently")
	}
}

func TestEnsureWritesUsableCasesAndRefusesTheRest(t *testing.T) {
	dir := t.TempDir()
	p, _ := ParseEvalPlan([]byte(specWith(goodPlan)))
	res, err := EnsureEvals(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CasesCreated) != 2 {
		t.Fatalf("want the 2 usable cases, got %v (skipped %v)", res.CasesCreated, res.Skipped)
	}
	if len(res.Skipped) != 2 {
		t.Errorf("a case that checks nothing and a badly named one must be refused, got %v", res.Skipped)
	}
	for _, f := range []string{"evals/gate.json", "evals/run.sh", "evals/check.sh", ".github/workflows/agent-evals.yml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	var c EvalCase
	b, _ := os.ReadFile(filepath.Join(dir, "evals", "cases", "no-ticket-spam.json"))
	if err := json.Unmarshal(b, &c); err != nil || c.MustNotContain[0] != "opened 5 tickets" {
		t.Errorf("case file lost its fields: %s", b)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "minimum of 20") {
		t.Errorf("the gap to min_cases is not reported: %v", res.Notes)
	}
}

// Re-running must never put a starter case back over one someone edited.
func TestEnsureLeavesExistingFilesAlone(t *testing.T) {
	dir := t.TempDir()
	p, _ := ParseEvalPlan([]byte(specWith(goodPlan)))
	if _, err := EnsureEvals(dir, p); err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(dir, "evals", "cases", "names-the-cause.json")
	if err := os.WriteFile(edited, []byte(`{"edited": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := EnsureEvals(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Any() {
		t.Errorf("second run wrote again: %+v", res)
	}
	if b, _ := os.ReadFile(edited); string(b) != `{"edited": true}` {
		t.Errorf("an edited case was overwritten: %s", b)
	}
}

// The scaffolded harness actually runs: with `cat` as the agent, the answer
// is the prompt, so "names-the-cause" passes ("disk" is in its prompt) and
// "no-ticket-spam" passes (its prompt never says "opened 5 tickets").
func TestScaffoldedHarnessRuns(t *testing.T) {
	for _, bin := range []string{"bash", "jq", "awk"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	dir := t.TempDir()
	p, _ := ParseEvalPlan([]byte(specWith(goodPlan)))
	if _, err := EnsureEvals(dir, p); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", filepath.Join(dir, "evals", "run.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("harness failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "pass rate: 2/2") {
		t.Errorf("unexpected harness output:\n%s", out)
	}
}

// An undecided harness command fails the suite rather than skipping it.
func TestUndecidedHarnessFailsLoudly(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	dir := t.TempDir()
	p, _ := ParseEvalPlan([]byte(specWith(strings.Replace(goodPlan, `"command": "cat"`, `"command": "TBD"`, 1))))
	res, err := EnsureEvals(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "not decided") {
		t.Errorf("TBD harness not reported: %v", res.Notes)
	}
	out, err := exec.Command("bash", filepath.Join(dir, "evals", "run.sh")).CombinedOutput()
	if err == nil {
		t.Fatalf("a TBD harness reported success:\n%s", out)
	}
}
