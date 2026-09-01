// Package dashboard answers one question: is the coding queue outrunning the
// integration queue? (OR-254)
//
// Agents scale horizontally. Integration does not -- it is one operation at a
// time by construction (OR-253), roughly a CI run plus a human. So the number
// that matters is not how fast agents code, it is whether coding is producing
// work faster than integration can absorb it. Nothing reported that.
//
// EVERYTHING HERE IS DERIVED FROM events.jsonl, and nothing is estimated. The
// log already records every transition this needs: a ticket claimed, a stage
// crossed, a branch pushed, a batch tested, a merge landed. A dashboard that
// invented a second source for any of it would eventually disagree with the
// log, and the log is the thing people trust when they are debugging at
// midnight.
package dashboard

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// View is the whole answer, computed once.
type View struct {
	Coding      Coding
	Integration Integration
	Throughput  Throughput
}

// Coding is the parallel half.
type Coding struct {
	// Active is tickets with an agent on them right now: claimed, and not yet
	// pushed, failed or merged.
	Active []string
	// Ready is finished work waiting for the integration queue. THIS IS THE
	// BACKPRESSURE SIGNAL: it grows when coding outruns integration, and it
	// grows before anything else looks wrong.
	Ready []string
	// Fixing is tickets a batch sent back, being repaired.
	Fixing []string
}

// Integration is the serial half.
type Integration struct {
	// Batches is how many have completed, and Elapsed their total time, so an
	// average can be stated rather than guessed.
	Batches int
	Elapsed time.Duration
	// RunsSpent is CI runs the batches actually consumed. RunsAvoided is what
	// the per-branch path would have spent on the same members -- one run per
	// member -- minus that. The number the whole design is justified by.
	RunsSpent   int
	MembersSeen int
}

// Avg is the mean integration time, or zero when nothing has integrated.
func (i Integration) Avg() time.Duration {
	if i.Batches == 0 {
		return 0
	}
	return i.Elapsed / time.Duration(i.Batches)
}

// RunsAvoided is what batching saved, and it can be NEGATIVE.
//
// Reported signed rather than floored at zero, because a batch that cost more
// than the path it replaced is the single most important thing this could
// tell an operator, and a metric that can only show a saving is advertising.
func (i Integration) RunsAvoided() int { return i.MembersSeen - i.RunsSpent }

// Throughput is the gap between work finished and work landed.
type Throughput struct {
	Completed int // agents finished, whether or not it landed
	Merged    int
}

// Read builds the view from one event log.
func Read(path string) (View, error) {
	evs, err := events.Read(path)
	if err != nil {
		return View{}, err
	}
	return From(evs), nil
}

// From computes the view from events already in hand.
//
// Split from Read so the arithmetic -- which is where the mistakes live -- is
// testable without a file, the same reason batch.go separates its search from
// its git.
func From(evs []events.Event) View {
	// state per key, last write wins: the log is append-only and in order, so
	// the final mention of a key is its current state.
	state := map[string]string{}
	var completed, merged int
	var in Integration

	for _, e := range evs {
		switch e.Kind {
		case events.KindClaimed:
			state[e.Key] = "active"
		case events.KindPush:
			// Pushed and nothing running: ready for the integration queue.
			state[e.Key] = "ready"
			completed++
		case events.KindMerge:
			state[e.Key] = "merged"
			merged++
		case events.KindFailed, events.KindBlocked:
			state[e.Key] = "fixing"
		case events.KindNote:
			in.absorb(e)
			// A batch note names what it landed, and that is a merge for
			// every key in it (OR-258). Read as WELL as the per-key merge
			// events runBatch now emits, not instead: the events are the
			// primary record and the note is the belt, so a log written by a
			// build that emitted one but not the other still retires its
			// members. Without either, a batch-landed ticket sits in `ready`
			// forever and the backpressure signal is pinned high.
			if n, ok := events.ParseBatchNote(e.Msg); ok {
				for _, key := range n.Landed {
					state[key] = "merged"
					merged++
				}
			}
		}
	}

	v := View{
		Integration: in,
		Throughput:  Throughput{Completed: completed, Merged: merged},
	}
	for key, s := range state {
		switch s {
		case "active":
			v.Coding.Active = append(v.Coding.Active, key)
		case "ready":
			v.Coding.Ready = append(v.Coding.Ready, key)
		case "fixing":
			v.Coding.Fixing = append(v.Coding.Fixing, key)
		}
	}
	sort.Strings(v.Coding.Active)
	sort.Strings(v.Coding.Ready)
	sort.Strings(v.Coding.Fixing)
	return v
}

// absorb reads a batch's own record out of the note runBatch writes.
//
// PARSED FROM THE MESSAGE, which is fragile, and the alternative was worse:
// a second event kind for batch results would be a second place the batch has
// to remember to report, and the first time somebody added a field to one and
// not the other the dashboard would quietly diverge from the log. When the
// event carries structured Detail this should read that instead -- the
// mechanism exists, this is simply not the change that introduces it.
// absorb reads one batch note.
//
// Through events.ParseBatchNote, which is the same code that WROTE the note
// (OR-258). It used to be a Sscanf for "%d run(s) in %fm", which parsed
// "3m0s" by luck and failed outright on "45s" or "1h2m0s" -- so the dashboard
// silently emptied itself for any batch that did not take a whole number of
// minutes, and reported "no batch has integrated yet" on a repository that
// had run several.
//
// Member counting was its own bug: strings.Count(msg, "OR-") hard-codes one
// project's key prefix, so on any other tracker every batch was measured as
// having no members and the runs-saved figure divided by nothing.
func (i *Integration) absorb(e events.Event) {
	n, ok := events.ParseBatchNote(e.Msg)
	if !ok {
		return
	}
	i.Batches++
	i.RunsSpent += n.Runs
	i.MembersSeen += len(n.Members())
	i.Elapsed += n.Elapsed
}

// Render writes the view.
//
// PLAIN TEXT, and identical off a terminal. Every other Orion surface degrades
// rather than losing information, and a dashboard is the last place to put a
// number behind a capability check.
func Render(w io.Writer, v View) {
	fmt.Fprintf(w, "%s\n", ui.Heading(w, "coding"))
	fmt.Fprintf(w, "  active agents   %d\n", len(v.Coding.Active))
	fmt.Fprintf(w, "  ready           %d%s\n", len(v.Coding.Ready), names(v.Coding.Ready))
	fmt.Fprintf(w, "  fixing          %d%s\n", len(v.Coding.Fixing), names(v.Coding.Fixing))

	fmt.Fprintf(w, "\n%s\n", ui.Heading(w, "integration"))
	fmt.Fprintf(w, "  queue depth     %d ticket(s) waiting\n", len(v.Coding.Ready))
	if v.Integration.Batches == 0 {
		// Said, not shown as zero. "0 min" is a measurement; "nothing has
		// integrated yet" is the truth, and they lead to different actions.
		fmt.Fprintf(w, "  %s\n", ui.Dim(w, "no batch has integrated yet"))
	} else {
		fmt.Fprintf(w, "  batches         %d\n", v.Integration.Batches)
		fmt.Fprintf(w, "  avg integration %s\n", round(v.Integration.Avg()))
		fmt.Fprintf(w, "  drain estimate  %s\n", drain(v))
	}

	fmt.Fprintf(w, "\n%s\n", ui.Heading(w, "throughput"))
	fmt.Fprintf(w, "  completed       %d\n", v.Throughput.Completed)
	fmt.Fprintf(w, "  merged          %d\n", v.Throughput.Merged)
	if v.Integration.Batches == 0 {
		fmt.Fprintf(w, "  %s\n", ui.Dim(w, "CI runs saved: no batches yet"))
		return
	}
	saved := v.Integration.RunsAvoided()
	switch {
	case saved > 0:
		fmt.Fprintf(w, "  CI runs saved   %d (%d members, %d runs)\n",
			saved, v.Integration.MembersSeen, v.Integration.RunsSpent)
	default:
		// The unflattering direction, reported as plainly as the other.
		fmt.Fprintf(w, "  CI runs saved   %d -- batching COST %d extra run(s)\n",
			saved, -saved)
	}
}

// drain is how long the waiting work would take to integrate at the observed
// rate. The number that turns queue depth from a fact into a decision.
func drain(v View) string {
	if len(v.Coding.Ready) == 0 {
		return "nothing waiting"
	}
	// One batch takes up to max_concurrent_tickets members, but the honest
	// figure uses the members per batch this repository has actually seen
	// rather than the cap it is allowed.
	per := float64(v.Integration.MembersSeen) / float64(v.Integration.Batches)
	if per <= 0 {
		return "unknown"
	}
	batches := float64(len(v.Coding.Ready)) / per
	return round(time.Duration(batches * float64(v.Integration.Avg())))
}

func names(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return "  " + strings.Join(keys, " ")
}

func round(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
