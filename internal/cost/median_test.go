package cost

import (
	"testing"
	"time"
)

func row(actor, project string, seconds float64, exit int) HistoryRow {
	return HistoryRow{Actor: actor, Project: project, Seconds: seconds, Exit: exit}
}

// The median, not the mean. Run durations have a long right tail -- the
// implementer's p90 is roughly twice its median -- so a mean is dragged upward
// by the runs that went badly and would make every ordinary run look early.
func TestMedianIgnoresTheTail(t *testing.T) {
	rows := []HistoryRow{
		row("implementer", "OR", 600, 0),
		row("implementer", "OR", 660, 0),
		row("implementer", "OR", 700, 0),
		row("implementer", "OR", 5000, 0), // one run that went very long
	}
	got, ok := MedianSeconds(rows, []string{"OR"}, "implementer")
	if !ok {
		t.Fatal("four completed runs should yield a median")
	}
	if want := 680 * time.Second; got != want {
		t.Errorf("median = %v, want %v (the mean would be %v)", got, want, 1740*time.Second)
	}
}

// Only runs that STARTED and did not fail are counted. A failed run's
// duration is however long it took to break, and a run that never opened a
// session took no time worth measuring; neither says anything about how long
// the work takes.
func TestMedianCountsOnlyCompletedRuns(t *testing.T) {
	failed := HistoryRow{Actor: "implementer", Project: "OR", Seconds: 1, Exit: 1}
	never := HistoryRow{Actor: "implementer", Project: "OR", Seconds: 2, NeverStarted: true}
	unknown := HistoryRow{Actor: "implementer", Project: "OR", Seconds: 0}
	rows := []HistoryRow{
		failed, never, unknown,
		row("implementer", "OR", 300, 0),
		row("implementer", "OR", 300, 0),
		row("implementer", "OR", 300, 0),
	}
	got, ok := MedianSeconds(rows, nil, "implementer")
	if !ok || got != 5*time.Minute {
		t.Errorf("median = %v (ok=%v), want 5m from the three completed runs only", got, ok)
	}
}

// Scoped to the project. The same actor against a small library and against a
// large service are different distributions, and a bar built from the wrong
// one says "running long" on every run of the slower repository.
func TestMedianIsScopedToTheProjects(t *testing.T) {
	rows := []HistoryRow{
		row("implementer", "OR", 60, 0),
		row("implementer", "OR", 60, 0),
		row("implementer", "OR", 60, 0),
		row("implementer", "FCIA", 6000, 0),
		row("implementer", "FCIA", 6000, 0),
		row("implementer", "FCIA", 6000, 0),
	}
	if got, _ := MedianSeconds(rows, []string{"OR"}, "implementer"); got != time.Minute {
		t.Errorf("OR median = %v, want 1m", got)
	}
	if got, _ := MedianSeconds(rows, []string{"fcia"}, "implementer"); got != 100*time.Minute {
		t.Errorf("FCIA median = %v, want 100m -- and the scope must be case-insensitive", got)
	}
	// No project scope means every project, which is what a watcher over the
	// whole registry asks for.
	if _, ok := MedianSeconds(rows, nil, "implementer"); !ok {
		t.Error("an unscoped lookup should see every project's runs")
	}
}

// And it is per ACTOR: the implementer's median must never be applied to QA's
// row, or the bar is measuring the wrong thing entirely.
func TestMedianIsPerActor(t *testing.T) {
	rows := []HistoryRow{
		row("implementer", "OR", 600, 0),
		row("implementer", "OR", 600, 0),
		row("implementer", "OR", 600, 0),
		row("qa", "OR", 120, 0),
	}
	if got, ok := MedianSeconds(rows, nil, "qa"); ok {
		t.Errorf("qa has one run, which is not a median: got %v", got)
	}
}

// Below the floor there is no median at all, and the caller draws no bar. A
// "median" of one run is that run, and a bar against it is a bar against
// noise -- which is precisely the confident-but-wrong number this display
// must never show.
func TestMedianRefusesTooFewRuns(t *testing.T) {
	for n := 0; n < medianMinRuns; n++ {
		var rows []HistoryRow
		for i := 0; i < n; i++ {
			rows = append(rows, row("implementer", "OR", 600, 0))
		}
		if got, ok := MedianSeconds(rows, nil, "implementer"); ok {
			t.Errorf("%d run(s) yielded a median of %v; want none", n, got)
		}
	}
	var rows []HistoryRow
	for i := 0; i < medianMinRuns; i++ {
		rows = append(rows, row("implementer", "OR", 600, 0))
	}
	if _, ok := MedianSeconds(rows, nil, "implementer"); !ok {
		t.Errorf("%d runs should be enough", medianMinRuns)
	}
}
