package collect

// What batching actually saves, measured rather than argued (OR-250).
//
// Batch integration was justified, built, shipped and enabled without one
// measurement of the thing it exists to improve. The number it printed --
// "the batch cost N CI run(s)" -- is the cost model ADR 0015 argued in, and
// it is not what an operator feels. What they feel is minutes between "the
// agents finished" and "the work is on the work branch".
//
// So: elapsed, and a BASELINE to read it against. 18 minutes means nothing
// alone. Against a per-branch median of 40 for the same tickets it is the
// whole argument; against 15 it says the feature is not earning its
// complexity and should go back off. The comparison is the deliverable, not
// the timer.
//
// THE BASELINE IS HISTORY, NOT A SIMULATION. Every per-branch landing this
// repository has ever done is already in events.jsonl as a push followed by a
// merge for one key. Their median is what the old path cost, measured on this
// project's own runs, on this machine, with this CI. A modelled counterfactual
// would be a number this file invented, and an invented number that flatters
// the feature is worse than no number.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// baseline is the per-branch cost this repository actually paid, before.
type baseline struct {
	// Median is the middle push-to-merge time across past per-branch
	// landings. Zero when there is not enough history.
	Median time.Duration
	// Samples is how many landings it was drawn from. Reported, because a
	// median of two is a coincidence and a reader deserves to know which
	// they are looking at.
	Samples int
}

// minBaselineSamples is the fewest landings worth calling a median.
//
// Three, which is not statistics -- it is the point below which a number
// misleads more than it informs. Two landings have no middle, and one is an
// anecdote wearing a median's clothes. Under this the report says it has no
// baseline yet rather than printing a figure nobody should act on.
const minBaselineSamples = 3

// batchBaseline is the median ELAPSED of this project's past batches.
//
// A different quantity from perBranchBaseline below, and the one the live
// region's CI bar fills against: that measures what the per-branch path costs
// per ticket, this measures how long a batch's own run takes. Filling a
// batch's bar against the per-branch number would compare a shared CI run to
// a single branch's whole life, which is off by roughly the member count.
//
// Read from the batch notes each finished batch already writes (OR-258), so
// this needs no new record -- and, like the per-branch baseline, it refuses
// to answer below minBaselineSamples rather than call two runs a median
// (OR-250).
func batchBaseline(path string) baseline {
	all, err := events.Read(path)
	if err != nil {
		return baseline{}
	}
	var spans []time.Duration
	for _, e := range all {
		n, ok := events.ParseBatchNote(e.Msg)
		// A batch that RAN CI, or it is not a sample of a CI run (OR-320). A
		// pass that assembled nothing still writes its note -- "0 run(s) in
		// 1s" -- and eight of those outvoted every real run, so the median
		// read 1s and the rule said "running long" from the first second.
		// Its elapsed measures assembly, not the run, and is the wrong
		// quantity whatever its value.
		if !ok || n.Elapsed <= 0 || n.Runs == 0 {
			continue
		}
		spans = append(spans, n.Elapsed)
	}
	if len(spans) < minBaselineSamples {
		return baseline{Samples: len(spans)}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i] < spans[j] })
	return baseline{Median: spans[len(spans)/2], Samples: len(spans)}
}

// perBranchBaseline reads the event log and reports what a per-branch landing
// used to cost.
//
// PUSH TO MERGE, deliberately, because that is the span the batch replaces.
// It starts when a branch is ready to land and ends when it is on the work
// branch, and it therefore includes exactly what batching changes: the CI
// run, any rebase the landing needed, and the re-run that followed. It does
// NOT include the agent's own time, which batching does not touch and which
// would swamp the comparison.
//
// Batch landings are excluded by construction: they emit no per-key push, so
// a key that merged as part of a batch contributes no sample. Without that,
// each batch would poison the baseline it is being measured against.
func perBranchBaseline(path string) baseline {
	all, err := events.Read(path)
	if err != nil {
		return baseline{}
	}
	// The FIRST push per key and the LAST merge. A branch that was pushed,
	// rebased and pushed again cost all of it: taking the last push would
	// quietly delete the rebase cycle from the baseline, which is the exact
	// cost batching claims to remove.
	pushed := map[string]time.Time{}
	var spans []time.Duration
	for _, e := range all {
		if e.Key == "" {
			continue
		}
		switch e.Kind {
		case events.KindPush:
			if _, seen := pushed[e.Key]; !seen {
				pushed[e.Key] = e.At
			}
		case events.KindMerge:
			at, seen := pushed[e.Key]
			if !seen || !e.At.After(at) {
				continue
			}
			spans = append(spans, e.At.Sub(at))
			delete(pushed, e.Key) // a later re-use of the key starts over
		}
	}
	if len(spans) < minBaselineSamples {
		return baseline{Samples: len(spans)}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i] < spans[j] })
	return baseline{Median: spans[len(spans)/2], Samples: len(spans)}
}

// costLine is what the batch reports when it is done.
//
// ELAPSED FIRST, then the runs, then the comparison. The mockup's rule is
// that cost is the headline rather than the footnote, and of the two numbers
// elapsed is the one a person came to the terminal with a question about.
//
// The comparison is stated as a multiple of the whole set, not per branch:
// "4 branches the old way was ~40m" answers "should I have waited", where a
// per-branch median answers a question nobody asked.
func costLine(runs, members int, elapsed time.Duration, b baseline) string {
	s := fmt.Sprintf("%s for %s, in %s",
		plural(runs, "CI run"), plural(members, "branch"), round(elapsed))

	if b.Median == 0 {
		// Said, not omitted. A missing comparison that simply is not printed
		// reads as "there was nothing to compare with", when the truth is
		// "not enough has landed here yet to know".
		return s + fmt.Sprintf(" (no baseline yet: %d past landing(s), %d needed)",
			b.Samples, minBaselineSamples)
	}
	was := time.Duration(members) * b.Median
	switch {
	case was > elapsed:
		return s + fmt.Sprintf(" · the per-branch path took ~%s for this many (median of %d)",
			round(was), b.Samples)
	default:
		// Reported the same way when the batch LOST. A measurement that only
		// speaks up when it flatters the feature is not a measurement.
		return s + fmt.Sprintf(" · SLOWER than the per-branch path's ~%s (median of %d)",
			round(was), b.Samples)
	}
}

// round trims a duration to something a person reads at a glance. Seconds
// below a minute, whole minutes above: nobody needs 18m03.472s.
func round(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	// "branch" needs -es, and a bare -s produced "2 branchs" on every batch
	// this repository has ever run. The sibilant endings are the only ones
	// these units use; a general pluraliser would be more code than the two
	// words that reach here deserve.
	suffix := "s"
	for _, end := range []string{"ch", "sh", "s", "x", "z"} {
		if strings.HasSuffix(unit, end) {
			suffix = "es"
			break
		}
	}
	return fmt.Sprintf("%d %s%s", n, unit, suffix)
}
