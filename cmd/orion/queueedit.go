package main

// `orion queue add` and `orion queue remove` put a ticket into the queue and
// take it out again, which until now meant editing the ORION label by hand in
// the Jira UI, one ticket at a time (OR-223).
//
// WHY THIS IS THE COMMAND THAT WAS MISSING. `orion queue` could READ the
// queue and nothing could change it, so the single most frequent write an
// operator performs was the only one with no verb: on 2026-08-30 the label was
// moved by hand about fifteen times -- queueing OR-206 and OR-207, then
// OR-204, OR-209 and OR-210, unqueueing OR-135 and OR-154 behind a dependency,
// pulling OR-217 out after three breaker trips. Every one of those was a REST
// call written by hand against the tool that owns the queue.
//
// WHY IT REFUSES A TICKET WITH NO fixVersion. OR-221 makes both signals
// required to claim, and a gate is only reasonable if satisfying it is cheap:
// OR-222 made the version cheap and this makes the label cheap. Labelling a
// ticket that carries no milestone would create exactly the silent
// never-runs state OR-221 exists to prevent, so it is refused here, where the
// operator is still looking, rather than discovered later as a ticket that sat
// in the queue and was never picked up.
//
// WHY IT WILL NOT TOUCH A CLAIMED TICKET. orion-working and orion-ci-wait are
// the queue's lock: an agent or a CI poll owns that ticket right now. Removing
// the label under a running agent, or re-adding ORION beside the claim,
// corrupts the state the whole watcher reads. There is no override for that
// one -- the way out is to let the run finish or fail.
//
// WHY REMOVE IS ONLY AN UNQUEUE. Taking a ticket out of the queue leaves its
// status and its fixVersion exactly as they were. It stops future claiming and
// nothing else; unqueueing is not undoing, and a remove that also reverted a
// status would make the cheap operation the dangerous one.

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

// queueBlock is a ticket this run will NOT touch, and every reason why.
//
// Reasons plural because a failed ticket that also carries no milestone has
// two problems, and reporting one of them sends the operator back for a second
// run to be told about the other.
type queueBlock struct {
	Key     string
	Reasons []string
}

// queuePlan is what an add or a remove would do, decided before anything is
// written.
type queuePlan struct {
	// Add is tickets that will gain the queue label.
	Add []string
	// Reset is failed tickets being requeued: the failed label is cleared, the
	// queue label added, and the status returned to To Do. Three operations
	// that are always wanted together (see runQueueEdit).
	Reset []string
	// Remove is tickets that will lose the queue label.
	Remove []string
	// Already is tickets already in the state being asked for: the idempotent
	// case, which must be a no-op that says so.
	Already []string
	// Missing is keys no such ticket exists for -- the hazard a range
	// introduces, since a range names tickets nobody looked at.
	Missing []string
	// Blocked is tickets a rule refuses to touch, each with its reason.
	Blocked []queueBlock
}

func (p queuePlan) writes() int { return len(p.Add) + len(p.Reset) + len(p.Remove) }

// planQueueAdd decides what queueing these keys would do.
//
// current maps a key to the issue behind it; a key ABSENT from the map is one
// the tracker has no such ticket for. Order follows the keys as given, so the
// plan reads back in the order the operator typed -- including the expanded
// interior of a range, which is the part they did not type and most need to
// see.
func planQueueAdd(keys []string, current map[string]tracker.Issue, label string, reset bool) queuePlan {
	var p queuePlan
	for _, k := range keys {
		is, exists := current[k]
		if !exists {
			p.Missing = append(p.Missing, k)
			continue
		}
		state := tracker.State(is.Labels, label)
		// A claim outranks everything else: whatever else is wrong with this
		// ticket, nothing may be written to it while a run owns it.
		if reason, claimed := claimReason(state); claimed {
			p.Blocked = append(p.Blocked, queueBlock{Key: k, Reasons: []string{reason}})
			continue
		}
		if state == "queued" {
			p.Already = append(p.Already, k)
			continue
		}
		var reasons []string
		if len(is.FixVersions) == 0 {
			reasons = append(reasons, "no fixVersion, so it could never be claimed; "+
				"attach one first with `orion release add <version> "+k+"`")
		}
		if state == "failed" && !reset {
			reasons = append(reasons, "it is "+tracker.LabelFailed+", which needs clearing AND "+
				"the status returned to To Do; pass --reset to do both")
		}
		switch {
		case len(reasons) > 0:
			p.Blocked = append(p.Blocked, queueBlock{Key: k, Reasons: reasons})
		case state == "failed":
			p.Reset = append(p.Reset, k)
		default:
			p.Add = append(p.Add, k)
		}
	}
	return p
}

// planQueueRemove decides what unqueueing these keys would do.
//
// A ticket that is not queued at all -- including one sitting at orion-failed,
// which no longer carries the queue label -- is Already, not an error. Remove
// is how an operator takes work back off the list, and it has to be safe to
// type over a set that is only partly on it.
func planQueueRemove(keys []string, current map[string]tracker.Issue, label string) queuePlan {
	var p queuePlan
	for _, k := range keys {
		is, exists := current[k]
		if !exists {
			p.Missing = append(p.Missing, k)
			continue
		}
		state := tracker.State(is.Labels, label)
		switch {
		case isClaimed(state):
			reason, _ := claimReason(state)
			p.Blocked = append(p.Blocked, queueBlock{Key: k, Reasons: []string{reason}})
		case state == "queued":
			p.Remove = append(p.Remove, k)
		default:
			p.Already = append(p.Already, k)
		}
	}
	return p
}

func isClaimed(state string) bool { return state == "working" || state == "ci-wait" }

// claimReason names the lock rather than saying "busy", because which lock it
// is decides what the operator does next: wait for an agent, or wait for CI.
func claimReason(state string) (string, bool) {
	switch state {
	case "working":
		return "it is " + tracker.LabelWorking + ": an agent owns it right now, and " +
			"relabelling under a running agent corrupts the claim", true
	case "ci-wait":
		return "it is " + tracker.LabelCIWait + ": CI owns it right now, and " +
			"relabelling it would strand the open pull request", true
	}
	return "", false
}

// printQueuePlan reports what will change BEFORE anything is written, the same
// shape `release add` prints and for the same reason: the preview is what makes
// a range safe to type, since it lists the tickets the range expanded to.
func printQueuePlan(w io.Writer, verb, project, label string, p queuePlan) {
	if verb == "add" {
		ui.Ok(w, "plan", "%s in %s: %d to queue, %d to requeue, %d already queued, "+
			"%d refused, %d not found",
			label, project, len(p.Add), len(p.Reset), len(p.Already), len(p.Blocked), len(p.Missing))
	} else {
		ui.Ok(w, "plan", "%s in %s: %d to unqueue, %d not queued, %d refused, %d not found",
			label, project, len(p.Remove), len(p.Already), len(p.Blocked), len(p.Missing))
	}
	for _, k := range p.Add {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, "queue    "+k))
	}
	for _, k := range p.Reset {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			"requeue  "+k+"  clears "+tracker.LabelFailed+" and returns it to To Do"))
	}
	for _, k := range p.Remove {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, "unqueue  "+k))
	}
	for _, k := range p.Already {
		if verb == "add" {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, "already  "+k))
		} else {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, "not queued  "+k))
		}
	}
	for _, b := range p.Blocked {
		ui.Warn(w, "%s is not touched: %s", b.Key, strings.Join(b.Reasons, "; and "))
	}
	for _, k := range p.Missing {
		ui.Warn(w, "no such ticket: %s", k)
	}
}

// runQueueEdit is `orion queue add` and `orion queue remove`.
func runQueueEdit(verb string, args []string) {
	var project string
	reset := false
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			i++
			if i < len(args) {
				project = args[i]
			}
		case "--reset":
			reset = true
		default:
			rest = append(rest, args[i])
		}
	}
	usage := "orion queue " + verb + " <KEY|KEY..KEY>... [--project KEY]"
	if verb == "add" {
		usage += " [--reset]"
	} else if reset {
		// Silently ignoring a flag is how an operator comes to believe a remove
		// resets a failed ticket. It does not: remove only unqueues.
		fmt.Fprintln(os.Stderr, "orion queue remove: --reset belongs to `orion queue add`; "+
			"removing a label never changes a status")
		os.Exit(64)
	}

	keys, err := expandTicketKeys("queue "+verb, "<KEY|KEY..KEY>... [--project KEY]", rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	if len(keys) == 0 {
		fmt.Fprintln(os.Stderr, usage+"\n       e.g. orion queue "+verb+" OR-100 OR-140..OR-145")
		os.Exit(64)
	}
	key, err := projectForKeys("queue "+verb, project, keys)
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
		fmt.Fprintln(os.Stderr, "tracker is disabled in orion.json; there is no queue to change")
		os.Exit(1)
	}
	// The queue label is this repository's, so writing it onto another
	// project's tickets would put work in a queue no watcher here reads.
	if bound := strings.ToUpper(strings.TrimSpace(cfg.Tracker.ProjectKey)); bound != "" && bound != key {
		fmt.Fprintf(os.Stderr, "orion queue %s: this repository is bound to %s, but these are %s "+
			"tickets; run it from that project's repository\n", verb, bound, key)
		os.Exit(1)
	}
	label := cfg.Tracker.QueueLabel

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	// RESOLVE EVERY KEY BEFORE WRITING ANY, one request each rather than one
	// query for the set: Jira answers a JQL key list containing a key that does
	// not exist with a 400 for the whole query, which would turn "one ticket in
	// this range was deleted" into "nothing happened and here is a parse error".
	current := make(map[string]tracker.Issue, len(keys))
	for _, k := range keys {
		is, err := j.GetIssue(k)
		if errors.Is(err, tracker.ErrIssueNotFound) {
			continue
		}
		exitOn(err)
		current[k] = *is
	}

	var plan queuePlan
	if verb == "add" {
		plan = planQueueAdd(keys, current, label, reset)
	} else {
		plan = planQueueRemove(keys, current, label)
	}
	printQueuePlan(w, verb, key, label, plan)

	if plan.writes() == 0 {
		// Re-running is a no-op that says so: a command that errors on a re-run
		// cannot be retried, and requeueing a set that is already half queued is
		// the normal way this gets used.
		ui.Ok(w, "unchanged", "%d ticket(s) already as asked; nothing to write", len(plan.Already))
		if len(plan.Missing) > 0 || len(plan.Blocked) > 0 {
			os.Exit(1)
		}
		return
	}

	var failed []string
	for _, k := range plan.Add {
		if err := j.SetLabels(k, []string{label}, nil); err != nil {
			ui.Fail(w, "%s: %v", k, err)
			failed = append(failed, k)
			continue
		}
		ui.Ok(w, "queued", "%s <- %s", k, label)
	}
	for _, k := range plan.Reset {
		// Label first, status second. The label is what the watcher queries on,
		// so a ticket that gets the label and not the transition is still
		// queued -- whereas the reverse leaves a ticket sitting at To Do,
		// unlabelled, looking ready and never running: the exact mistake this
		// flag exists to prevent.
		if err := j.SetLabels(k, []string{label}, []string{tracker.LabelFailed}); err != nil {
			ui.Fail(w, "%s: %v", k, err)
			failed = append(failed, k)
			continue
		}
		if err := j.TransitionTo(k, "To Do"); err != nil {
			// Not fatal, the same judgement `orion work` makes about In
			// Progress: a workflow without that status is a configuration
			// difference, not a reason to call a requeue that worked a failure.
			ui.Warn(w, "%s is queued, but it could not be moved to To Do: %v", k, err)
			continue
		}
		ui.Ok(w, "requeued", "%s <- %s, %s cleared, back to To Do", k, label, tracker.LabelFailed)
	}
	for _, k := range plan.Remove {
		if err := j.SetLabels(k, nil, []string{label}); err != nil {
			ui.Fail(w, "%s: %v", k, err)
			failed = append(failed, k)
			continue
		}
		// Say what was NOT done, because "removed" reads like a bigger change
		// than it is: the ticket keeps its status and its milestone.
		ui.Ok(w, "unqueued", "%s -> %s removed; status and fixVersion unchanged", k, label)
	}

	if len(failed) > 0 {
		// Partial application is safe to leave: the command is idempotent, so
		// the fix is to re-run it once the cause is dealt with.
		ui.Fail(w, "%d ticket(s) could not be updated: %s", len(failed), strings.Join(failed, ", "))
		os.Exit(1)
	}
	if len(plan.Blocked) > 0 || len(plan.Missing) > 0 {
		// The writes succeeded, but a refused or non-existent ticket means the
		// operator did not get what they asked for. A zero exit here would let
		// a script -- or a person reading the last line -- treat a ticket that
		// never entered the queue as one that did.
		ui.Fail(w, "%d ticket(s) refused, %d key(s) name no ticket",
			len(plan.Blocked), len(plan.Missing))
		os.Exit(1)
	}
}
