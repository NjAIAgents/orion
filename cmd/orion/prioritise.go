package main

// `orion prioritise KEY KEY...` reorders the work queue: the tickets named
// come out in the order they were typed (OR-280).
//
// WHY THIS IS THE OTHER MISSING WRITE. `orion queue add` and `orion queue
// remove` decide what is IN the queue; nothing decided what comes first.
// Until now that was a drag in Jira's backlog -- a gesture, with no command
// behind it -- so the web UI, which shells out to this CLI and reimplements
// no orchestration, could not offer it at all.
//
// IT WRITES RANK, WHICH IS WHAT THE QUEUE ORDERS BY. `orion queue` and the
// watcher both read "priority DESC, Rank ASC". Rank is the field a person
// moves by dragging, and this is the same operation with a name.
//
// WHY MIXED PRIORITIES ARE REFUSED. Priority is read BEFORE Rank, so a High
// ticket ranked below a Medium one still runs first: the command would report
// success and the queue would show a different order. Rather than write an
// order it cannot deliver, it stops and says which ticket carries which
// priority. Levelling them is the operator's call and one they can see the
// effect of; silently overruling a priority to satisfy an ordering is not.
//
// WHY IT IS ALL OR NOTHING, unlike `queue add`. Queueing five tickets is five
// independent operations and a partial result is four queued tickets. An
// ORDERING is one intention: applying the half of it whose tickets happened
// to resolve leaves a queue nobody asked for, and it looks like it worked.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// rankPlan is what a prioritise run would do, decided before anything is
// written.
type rankPlan struct {
	// Order is the tickets to rank, in the order asked for. Set only when
	// the whole run is going ahead.
	Order []string
	// Missing is keys no such ticket exists for.
	Missing []string
	// Blocked is tickets that cannot take part, each with its reason.
	Blocked []queueBlock
	// Refusal is a reason the run stops as a whole rather than per ticket --
	// today only "these tickets do not share one priority".
	Refusal string
}

// ok reports whether this plan will be written.
func (p rankPlan) ok() bool {
	return p.Refusal == "" && len(p.Missing) == 0 && len(p.Blocked) == 0 && len(p.Order) > 1
}

// planPrioritise decides what ranking these keys in this order would do.
//
// current maps a key to the issue behind it; a key ABSENT from the map is one
// the tracker has no such ticket for.
func planPrioritise(keys []string, current map[string]tracker.Issue, label string) rankPlan {
	var p rankPlan
	for _, k := range keys {
		is, exists := current[k]
		if !exists {
			p.Missing = append(p.Missing, k)
			continue
		}
		// Only a QUEUED ticket has a position in the queue to change. A
		// claimed one is already running, a failed one is out of the queue,
		// and an unlabelled one was never in it -- ordering any of them is
		// an instruction with no effect, which is worse than a refusal
		// because it reads as one that worked.
		if state := tracker.State(is.Labels, label); state != "queued" {
			p.Blocked = append(p.Blocked, queueBlock{Key: k, Reasons: []string{rankBlockReason(state, label)}})
			continue
		}
		p.Order = append(p.Order, k)
	}
	if len(p.Order) > 1 {
		if names := distinctPriorities(p.Order, current); len(names) > 1 {
			p.Refusal = "the queue is ordered by priority BEFORE rank, and these tickets carry " +
				strings.Join(names, " and ") + ", so no ranking can put them in the order asked for.\n" +
				"  Give them the same priority in the tracker, or accept the priority order."
		}
	}
	return p
}

// rankBlockReason says why a ticket cannot be ordered, in terms of what the
// operator does next.
func rankBlockReason(state, label string) string {
	switch state {
	case "working":
		return "it is " + tracker.LabelWorking + ": an agent owns it right now, so its place " +
			"in the queue no longer decides anything"
	case "ci-wait":
		return "it is " + tracker.LabelCIWait + ": CI owns it right now, so its place in the " +
			"queue no longer decides anything"
	case "failed":
		return "it is " + tracker.LabelFailed + ", so it is not in the queue; requeue it first " +
			"with `orion queue add --reset`"
	}
	return "it does not carry " + label + ", so it is not in the queue; add it first with " +
		"`orion queue add`"
}

// distinctPriorities lists the priorities the ordered set carries, in the
// order first seen, with a ticket naming each so the message is actionable.
func distinctPriorities(keys []string, current map[string]tracker.Issue) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		name := strings.TrimSpace(current[k].Priority)
		if name == "" {
			// Priority is disabled on some team-managed projects. Every
			// ticket then reads the same, which is the case where ranking
			// decides everything -- so it must not look like a difference.
			name = "no priority"
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name+" ("+k+")")
		}
	}
	return out
}

// printRankPlan reports what will change BEFORE anything is written, the same
// shape `queue add` prints and for the same reason.
func printRankPlan(w io.Writer, project, label string, p rankPlan) {
	ui.Ok(w, "plan", "%s in %s: %d ticket(s) to reorder, %d refused, %d not found",
		label, project, len(p.Order), len(p.Blocked), len(p.Missing))
	for i, k := range p.Order {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf("%2d. %s", i+1, k)))
	}
	for _, b := range p.Blocked {
		ui.Warn(w, "%s cannot be ordered: %s", b.Key, strings.Join(b.Reasons, "; and "))
	}
	for _, k := range p.Missing {
		ui.Warn(w, "no such ticket: %s", k)
	}
	if p.Refusal != "" {
		ui.Warn(w, "%s", p.Refusal)
	}
}

// runPrioritise is `orion prioritise`.
func runPrioritise(args []string) {
	var project string
	rest := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" {
			i++
			if i < len(args) {
				project = args[i]
			}
			continue
		}
		rest = append(rest, args[i])
	}
	const usage = "orion prioritise <KEY|KEY..KEY>... [--project KEY]"

	keys, err := expandTicketKeys("prioritise", "<KEY|KEY..KEY>... [--project KEY]", rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	if len(keys) < 2 {
		// One key expresses no ordering: there is nothing to put it before
		// or after. Accepting it would be a command that writes nothing and
		// reports success.
		fmt.Fprintln(os.Stderr, usage+"\n       two or more tickets, in the order you want them worked"+
			"\n       e.g. orion prioritise OR-140 OR-100 OR-142")
		os.Exit(64)
	}
	key, err := projectForKeys("prioritise", project, keys)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}

	root, err := config.FindRoot(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "not inside an Orion project (no orion.json or .git found)")
		os.Exit(1)
	}
	cfg := config.Load(root)
	w := os.Stdout
	if !cfg.Tracker.Enabled {
		fmt.Fprintln(os.Stderr, "tracker is disabled in orion.json; there is no queue to reorder")
		os.Exit(1)
	}
	// The queue is this repository's, so reordering another project's
	// tickets would reorder a queue no watcher here reads.
	if bound := strings.ToUpper(strings.TrimSpace(cfg.Tracker.ProjectKey)); bound != "" && bound != key {
		fmt.Fprintf(os.Stderr, "orion prioritise: this repository is bound to %s, but these are %s "+
			"tickets; run it from that project's repository\n", bound, key)
		os.Exit(1)
	}
	label := cfg.Tracker.QueueLabel

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	// Resolve every key before writing any, one request each -- the reason
	// `queue add` does it this way: a JQL key list containing a key that does
	// not exist fails the WHOLE query in Jira, turning a deleted ticket into
	// a parse error.
	current := make(map[string]tracker.Issue, len(keys))
	for _, k := range keys {
		is, err := j.GetIssue(k)
		if errors.Is(err, tracker.ErrIssueNotFound) {
			continue
		}
		exitOn(err)
		current[k] = *is
	}

	plan := planPrioritise(keys, current, label)
	printRankPlan(w, key, label, plan)
	if !plan.ok() {
		ui.Fail(w, "nothing was reordered: an ordering is one intention, so a part of it "+
			"is not a smaller version of it")
		os.Exit(1)
	}

	// Each ticket after the first goes immediately behind the one before it,
	// so the set ends up in the order given. Pairwise and in order, so a
	// failure halfway leaves a prefix that is correct as far as it goes and
	// a re-run finishes the job.
	for i := 1; i < len(plan.Order); i++ {
		if err := j.RankAfter(plan.Order[i], plan.Order[i-1]); err != nil {
			ui.Fail(w, "%v", err)
			ui.Fail(w, "%d of %d moves were made; re-run the same command once that is fixed",
				i-1, len(plan.Order)-1)
			os.Exit(1)
		}
		ui.Ok(w, "ranked", "%s after %s", plan.Order[i], plan.Order[i-1])
	}
	ui.Ok(w, "reordered", "%d ticket(s); see the queue with `orion queue`", len(plan.Order))
}
