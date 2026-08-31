package main

// `orion release add` attaches tickets to a milestone -- the one step of a
// release that still had to be done outside Orion (OR-222).
//
// WHY THIS BELONGS HERE AND NOT IN A SCRIPT. Every other step already lives
// in this command: create, close, list, status, verify, and `changelog
// --version` beside them. Attaching tickets was the gap, and its absence is
// why a milestone drifts: OR-105 was tagged to the wrong version and a
// concurrency change carried no ticket at all, both of which `release status`
// then correctly reported as reconciliation failures. A milestone is only
// trustworthy if maintaining it is as cheap as reading it.
//
// WHY THERE IS NO `release remove` YET. It is the obvious sibling and the
// parser is shared with it already (expandTicketKeys), so building it is a
// small step whenever it is wanted. It is not wanted yet: the drift this
// command exists to fix is a ticket on the WRONG milestone, and that is an
// add, reported as a move. Taking a ticket off every milestone -- what remove
// would do -- returns it to the pile `release status` warns about as carrying
// no fixVersion at all, which is a state nobody has asked to create on
// purpose. A verb with no use is a verb that gets used by mistake.
//
// WHY IT DOES NOT ASK BEFORE WRITING. It prints the whole plan first, which
// is what makes a range safe to type, but it does not then prompt. `release
// create` and `release close` do not prompt either, and this is the same
// class of change to the same milestone: reversible by re-running with the
// other version, and needed unattended by the promotion in OR-188. The gate
// that matters is the one on a RELEASED version below, which prompting would
// not have improved.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// fixMove is a ticket that is LEAVING one or more milestones to join this one.
type fixMove struct {
	Key  string
	From []string
}

// fixPlan is what attaching a set of keys would do, decided before anything
// is written.
//
// Four outcomes and not a count, because they are four different sentences.
// "9 updated" would have hidden which nine tickets left which milestone on the
// night this command was written.
type fixPlan struct {
	// Add is tickets that carry no milestone at all.
	Add []string
	// Move is tickets that carry a DIFFERENT milestone, which they leave.
	Move []fixMove
	// Already is tickets already on this milestone: the idempotent case.
	Already []string
	// Missing is keys no such ticket exists for -- the hazard a range
	// introduces, since a range names tickets nobody looked at.
	Missing []string
}

// writes counts the tickets this plan would actually change.
func (p fixPlan) writes() int { return len(p.Add) + len(p.Move) }

// planFixVersion decides what attaching these keys to `target` would do,
// given each key's current milestones.
//
// current maps a key to the milestone names it carries; a key ABSENT from the
// map is one Jira has no such ticket for. Presence with an empty list is a
// real ticket on no milestone, which is a different answer and must not
// collapse into the same one.
//
// Order follows the keys as given, so the plan reads back in the order the
// operator typed -- including the expanded interior of a range, which is the
// part they did not type and most need to see.
func planFixVersion(target string, keys []string, current map[string][]string) fixPlan {
	var p fixPlan
	for _, k := range keys {
		on, exists := current[k]
		switch {
		case !exists:
			p.Missing = append(p.Missing, k)
		// Already on it: no write, even when the ticket also carries another
		// milestone. Writing would silently strip that other one, and a
		// re-run must change nothing.
		case containsExact(on, target):
			p.Already = append(p.Already, k)
		case len(on) == 0:
			p.Add = append(p.Add, k)
		default:
			p.Move = append(p.Move, fixMove{Key: k, From: on})
		}
	}
	return p
}

func containsExact(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// projectForKeys resolves which Jira project to act on.
//
// INFERRED FROM THE KEYS, unlike `release create`, which asks the registry and
// refuses when several projects are registered. The two are not inconsistent:
// `create` is handed a version name, which names no project, so it has
// nothing to infer FROM. `add` is handed keys, and OR-140 says OR as
// unambiguously as the flag would -- so requiring the flag anyway would be
// consistency of syntax bought by ignoring evidence already on the command
// line, and would make the common invocation longer for no added certainty.
//
// --project is still accepted and still scopes the version lookup. A flag that
// CONTRADICTS the keys is refused rather than silently winning: that
// combination is always a mistake, and resolving the version under one project
// while writing to tickets in another is the specific mistake that puts a
// ticket on the wrong milestone.
func projectForKeys(flag string, keys []string) (string, error) {
	from := ""
	for _, k := range keys {
		p, _, ok := splitTicketKey(k)
		if !ok {
			return "", fmt.Errorf("orion release add: %q is not a ticket key", k)
		}
		if from == "" {
			from = p
			continue
		}
		if p != from {
			return "", fmt.Errorf("orion release add: these keys span two projects, %s and %s; "+
				"a milestone belongs to one project, so run them separately", from, p)
		}
	}
	if from == "" {
		return "", fmt.Errorf("orion release add: no tickets given")
	}
	if flag == "" {
		return from, nil
	}
	if f := strings.ToUpper(strings.TrimSpace(flag)); f != from {
		return "", fmt.Errorf("orion release add: --project %s does not match the keys, "+
			"which are %s tickets; one of the two is wrong", f, from)
	}
	return from, nil
}

// printFixPlan reports what will change BEFORE anything is written.
//
// The preview is the whole reason a range is safe to type: it lists the
// tickets the range expanded to, so a ticket the operator did not picture is
// visible before it moves rather than after.
func printFixPlan(w io.Writer, project, target string, p fixPlan) {
	ui.Ok(w, "plan", "%s on %s: %d to add, %d to move, %d already there, %d not found",
		target, project, len(p.Add), len(p.Move), len(p.Already), len(p.Missing))
	for _, k := range p.Add {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, "add      "+k))
	}
	for _, m := range p.Move {
		fmt.Fprintf(w, "          %s\n",
			ui.Dim(w, "move     "+m.Key+"  from "+strings.Join(m.From, ", ")))
	}
	for _, k := range p.Already {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, "already  "+k))
	}
	for _, k := range p.Missing {
		ui.Warn(w, "no such ticket: %s", k)
	}
}

func runReleaseAdd(args []string) {
	var project string
	force := false
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			i++
			if i < len(args) {
				project = args[i]
			}
		case "--force":
			force = true
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "orion release add: which version? "+
			"e.g. orion release add v0.8.3 OR-100 OR-140..OR-145")
		os.Exit(64)
	}
	name := rest[0]

	keys, err := expandTicketKeys("release add",
		"<version> <KEY|KEY..KEY>... [--project KEY] [--force]", rest[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	if len(keys) == 0 {
		fmt.Fprintln(os.Stderr, "orion release add: which tickets? "+
			"e.g. orion release add "+name+" OR-100 OR-140..OR-145")
		os.Exit(64)
	}
	key, err := projectForKeys(project, keys)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)
	w := os.Stdout

	// The SAME lookup `create` and `close` use, deliberately: internal/tracker
	// owns version resolution and its case-exact matching, and a second path to
	// fixVersion is how two answers to one question drift apart.
	v, found, err := j.FindVersion(key, name)
	exitOn(err)
	if !found {
		ui.Fail(w, "%s has no version named %s", key, name)
		// Name what DOES exist. "not found" alone leaves the operator guessing
		// between a typo, a case difference and a milestone nobody created.
		if vs, lerr := j.ListVersions(key); lerr == nil && len(vs) > 0 {
			names := make([]string, 0, len(vs))
			for _, existing := range vs {
				names = append(names, existing.Name)
			}
			ui.Warn(w, "%s has: %s", key, strings.Join(names, ", "))
		}
		os.Exit(1)
	}
	// A released milestone records what SHIPPED. Adding to it rewrites a
	// history that is already public -- in the changelog, in the release notes,
	// in whatever anyone read. --force exists because a ticket genuinely
	// omitted from a shipped release does happen; the default must not.
	if v.Released && !force {
		ui.Fail(w, "%s on %s is already released, so adding a ticket to it would "+
			"rewrite history that has shipped", v.Name, key)
		ui.Warn(w, "attach it to the next milestone, or pass --force if it really did ship in %s",
			v.Name)
		os.Exit(1)
	}

	// RESOLVE EVERY KEY BEFORE WRITING ANY. One key per request rather than one
	// query for the set: Jira answers a JQL key list containing a key that does
	// not exist with a 400 for the whole query, which would turn "one ticket in
	// this range was deleted" into "nothing happened and here is a parse error".
	current := make(map[string][]string, len(keys))
	for _, k := range keys {
		is, err := j.GetIssue(k)
		if errors.Is(err, tracker.ErrIssueNotFound) {
			continue
		}
		exitOn(err)
		current[k] = is.FixVersions
	}

	plan := planFixVersion(v.Name, keys, current)
	printFixPlan(w, key, v.Name, plan)

	if plan.writes() == 0 {
		// Re-running is a no-op that says so, the same property `release
		// create` has: a command that errors on re-run cannot be retried.
		ui.Ok(w, "unchanged", "%d ticket(s) already on %s; nothing to write",
			len(plan.Already), v.Name)
		if len(plan.Missing) > 0 {
			os.Exit(1)
		}
		return
	}

	var failed []string
	for _, k := range plan.Add {
		if err := j.SetFixVersion(k, v.ID); err != nil {
			ui.Fail(w, "%s: %v", k, err)
			failed = append(failed, k)
			continue
		}
		ui.Ok(w, "added", "%s -> %s", k, v.Name)
	}
	for _, m := range plan.Move {
		if err := j.SetFixVersion(m.Key, v.ID); err != nil {
			ui.Fail(w, "%s: %v", m.Key, err)
			failed = append(failed, m.Key)
			continue
		}
		// A move is not an add, and saying which milestone it LEFT is the
		// point: the other version's contents changed too, and nothing else
		// reports that.
		ui.Ok(w, "moved", "%s %s -> %s", m.Key, strings.Join(m.From, ", "), v.Name)
	}

	if len(failed) > 0 {
		// Partial application is safe to leave: the command is idempotent, so
		// the fix is to re-run it once the cause is dealt with.
		ui.Fail(w, "%d ticket(s) could not be updated: %s", len(failed), strings.Join(failed, ", "))
		os.Exit(1)
	}
	if len(plan.Missing) > 0 {
		// The writes succeeded, but a key naming no ticket means the range was
		// not what the operator thought. A zero exit here would let a cron line
		// keep passing over a typo.
		ui.Fail(w, "%d key(s) name no ticket: %s",
			len(plan.Missing), strings.Join(plan.Missing, ", "))
		os.Exit(1)
	}
}
