package cost

// How long a run by this actor USUALLY takes.
//
// The live run display (OR-240) draws a progress bar against this, and the
// whole honesty of that bar rests on what the number is: a MEDIAN over runs
// this project has actually completed, not an estimate, not a target, and
// never a prediction of when the current run will finish. Nothing in Orion
// knows how many turns an agent has left.
//
// The median rather than the mean, and that is the important choice. Run
// durations have a long right tail -- the implementer's p90 is 21 minutes
// against an 11-minute median -- so a mean is dragged upward by the handful
// of runs that went badly and would make every ordinary run look early.
//
// Read from the usage HISTORY rather than the event log, because the event
// log rotates and the oldest rows are exactly the ones a median wants. See
// history.go for why the same fact lives in two files.

import (
	"sort"
	"strings"
	"time"
)

// medianMinRuns is how many completed runs it takes before a median is
// offered at all.
//
// Three, because a "median" of one run is that run and a bar drawn against it
// is a bar drawn against noise. Below the floor the caller gets no median,
// the row shows no bar, and that reads as "not applicable" -- which is the
// truth for a project Orion has barely worked yet.
const medianMinRuns = 3

// MedianSeconds returns the median wall-clock duration of this actor's
// completed runs, over the given projects.
//
// An empty projects list means every project. Scoping matters more than it
// looks: the same implementer against a small library and against a large
// service are different distributions, and a bar built from the wrong one
// says "running long" on every run of the slower repository.
//
// Only runs that STARTED and did not fail are counted. A run that never
// opened a session took no time worth measuring, and a failed run's duration
// is however long it took to break -- neither says anything about how long
// the work takes.
func MedianSeconds(rows []HistoryRow, projects []string, actor string) (time.Duration, bool) {
	want := map[string]bool{}
	for _, p := range projects {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			want[p] = true
		}
	}

	var secs []float64
	for _, r := range rows {
		if !strings.EqualFold(r.Actor, actor) {
			continue
		}
		if len(want) > 0 && !want[strings.ToUpper(r.Project)] {
			continue
		}
		if r.Exit != 0 || r.NeverStarted || r.Seconds <= 0 {
			continue
		}
		secs = append(secs, r.Seconds)
	}
	if len(secs) < medianMinRuns {
		return 0, false
	}
	sort.Float64s(secs)
	mid := len(secs) / 2
	m := secs[mid]
	if len(secs)%2 == 0 {
		m = (secs[mid-1] + secs[mid]) / 2
	}
	return time.Duration(m * float64(time.Second)), true
}
