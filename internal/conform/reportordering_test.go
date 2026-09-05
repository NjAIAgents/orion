package conform

// The report is read top to bottom, so its ORDER is part of its contract
// (OR-158): a reader meets what the change was judged against before meeting
// what was found wrong with it, so a finding can be traced straight back to
// its source document instead of being read as an accusation with nothing
// behind it.

import (
	"strings"
	"testing"
)

// The plan paths -- what this verdict was judged against -- appear before any
// divergence text. A reader who meets a finding before the document it came
// from has no way to check it without scrolling back up.
func TestReportNamesThePlanBeforeAnyDivergence(t *testing.T) {
	v := Verdict{
		Reviewed: true,
		Plan:     []string{"plans/ledger.plan.md", "docs/recommendations/confirmed/OR-158.md"},
		Divergences: []Divergence{
			{What: "the plan says one index per issuer; the diff adds a composite one"},
		},
		Note: "the change departs from the confirmed plan",
	}
	report := v.Report()

	planAt := strings.Index(report, "plans/ledger.plan.md")
	divAt := strings.Index(report, "composite one")
	if planAt < 0 {
		t.Fatalf("the report never names the plan it was judged against:\n%s", report)
	}
	if divAt < 0 {
		t.Fatalf("the report never names the divergence it found:\n%s", report)
	}
	if planAt > divAt {
		t.Errorf("the divergence appears before the plan it can be checked against:\n%s", report)
	}
}

// The same holds with more than one plan source: every path a finding could
// be checked against is stated before the finding, not interleaved with it.
func TestReportListsAllPlanPathsBeforeDivergences(t *testing.T) {
	v := Verdict{
		Reviewed: true,
		Plan:     []string{"plans/ledger.plan.md", "docs/recommendations/confirmed/OR-158.md"},
		Divergences: []Divergence{
			{What: "the diff adds a composite index the plan never mentioned"},
		},
	}
	report := v.Report()

	lastPlanAt := strings.LastIndex(report, "docs/recommendations/confirmed/OR-158.md")
	divAt := strings.Index(report, "composite index")
	if lastPlanAt < 0 || divAt < 0 {
		t.Fatalf("expected both plan paths and the divergence in the report:\n%s", report)
	}
	if lastPlanAt > divAt {
		t.Errorf("a plan path appears after the divergence it would let a reader check:\n%s", report)
	}
}
