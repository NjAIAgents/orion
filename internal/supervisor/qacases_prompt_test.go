package supervisor

import (
	"strings"
	"testing"
)

// The derive subagent can only report on what it was given, and it must be
// told it is READ ONLY: it runs inside the worktree the QA run is about to
// write test files into, so an agent that started writing tests itself would
// be editing a tree another run is about to edit (OR-182).
func TestQACasesPromptCarriesTheCriteriaAndTheDiffAndForbidsEditing(t *testing.T) {
	p := QACasesPrompt("OR-1", "round the total",
		"AC: totals are rounded to 2 decimal places",
		"--- a/total.go\n+++ b/total.go\n+ return math.Round(x*100) / 100")

	for _, want := range []string{"OR-1", "round the total", "2 decimal places", "math.Round"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q; the derive step cannot list cases for what it was not given", want)
		}
	}

	lower := strings.ToLower(p)
	if !strings.Contains(lower, "do not edit") {
		t.Error("prompt must forbid editing outright: it shares a worktree with the QA run")
	}
	if !strings.Contains(lower, "and nothing else") {
		t.Error("prompt must ask for the case list alone -- a short answer out of wide " +
			"reading is the entire reason this runs in its own context")
	}
}

// QA is handed the derived cases INSTEAD of the acceptance criteria -- that
// omission is the saving the ticket is about, since the criteria would
// otherwise be re-sent on every turn of the test authoring. Which means a
// case that points back at the ticket points at something its reader cannot
// see, so the derive prompt has to say so.
func TestQACasesPromptRequiresSelfContainedCases(t *testing.T) {
	p := QACasesPrompt("OR-1", "s", "d", "diff")
	if !strings.Contains(p, "stands on its own") {
		t.Error("prompt must require self-contained cases: whoever writes the tests " +
			"gets the list and not the ticket text it came from")
	}
}

// The whole point of the split. With cases derived, the QA run carries the
// list and NOT the acceptance criteria -- the criteria were read once, in a
// context that was thrown away, instead of riding along on every turn here.
func TestQAPromptCarriesTheDerivedCasesInsteadOfTheCriteria(t *testing.T) {
	const criteria = "AC: totals are rounded to 2 decimal places"
	const cases = "- a total of 1.005 rounds to 1.01\n- a negative total rounds the same way"

	p := QAPrompt("OR-1", "round the total", criteria, cases, QATools{})

	if !strings.Contains(p, "1.005 rounds to 1.01") {
		t.Errorf("the derived cases never reached the QA run:\n%s", p)
	}
	if strings.Contains(p, criteria) {
		t.Errorf("the acceptance criteria rode along with the derived cases; carrying both "+
			"pays for the reading this split exists to stop paying for:\n%s", p)
	}
	if strings.Contains(p, "Derive the test cases from the ticket") {
		t.Error("QA was told to derive cases it was already given")
	}
}

// The fallback, and it matters more than the saving: a derive step that
// produced nothing must leave QA exactly as it was before this existed --
// reading the criteria and deriving its own cases. A ticket with no tests is
// a worse outcome than a ticket whose tests cost a little more.
func TestQAPromptFallsBackToTheCriteriaWhenNoCasesWereDerived(t *testing.T) {
	const criteria = "AC: totals are rounded to 2 decimal places"

	p := QAPrompt("OR-1", "round the total", criteria, "   ", QATools{})

	if !strings.Contains(p, criteria) {
		t.Errorf("without derived cases the QA run must still get the criteria:\n%s", p)
	}
	if !strings.Contains(p, "Derive the test cases from the ticket") {
		t.Errorf("without derived cases QA must still be told to derive them:\n%s", p)
	}
}
