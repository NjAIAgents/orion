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
	// Shipped is tickets whose CURRENT milestone is already released.
	//
	// Moving one rewrites history that is already public: it shipped in that
	// release, the changelog and the release notes say so, and a milestone
	// that no longer lists it makes those two records disagree with Jira.
	// The destination has always been guarded (a released milestone cannot be
	// added to); this is the same rule applied to the SOURCE, which it was
	// not, and OR-306 left an already-released v0.8.11 that way.
	Shipped []fixMove
}

// writes counts the tickets this plan would actually change.
func (p fixPlan) writes() int { return len(p.Add) + len(p.Move) }

// blocked reports whether the plan contains anything this command refuses to
// do. Separate from writes(): a plan of nothing-but-refusals has no writes
// and is still not a no-op, because it must exit non-zero rather than say
// "nothing to write".
func (p fixPlan) blocked() bool { return len(p.Shipped) > 0 }

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
// released is the set of milestone names that have already shipped. A key
// carrying one of them is refused rather than moved -- see fixPlan.Shipped. A
// nil map means nothing is known to have shipped, which keeps every existing
// caller's behaviour unchanged.
func planFixVersion(target string, keys []string, current map[string][]string, released map[string]bool) fixPlan {
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
			if from := shippedAmong(on, released); len(from) > 0 {
				p.Shipped = append(p.Shipped, fixMove{Key: k, From: from})
				continue
			}
			p.Move = append(p.Move, fixMove{Key: k, From: on})
		}
	}
	return p
}

// shippedAmong returns the milestones in list that have already been
// released. Plural because a ticket can carry several, and naming every one
// is what lets the operator see what a --force would actually rewrite.
func shippedAmong(list []string, released map[string]bool) []string {
	var out []string
	for _, v := range list {
		if released[v] {
			out = append(out, v)
		}
	}
	return out
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
//
// Shared with `orion queue add|remove`, which resolves its project exactly the
// same way and for the same reason -- the label it writes belongs to one
// project too (OR-223). cmd names the caller so the refusal reads as the
// command the operator typed.
func projectForKeys(cmd, flag string, keys []string) (string, error) {
	from := ""
	for _, k := range keys {
		p, _, ok := splitTicketKey(k)
		if !ok {
			return "", fmt.Errorf("orion %s: %q is not a ticket key", cmd, k)
		}
		if from == "" {
			from = p
			continue
		}
		if p != from {
			return "", fmt.Errorf("orion %s: these keys span two projects, %s and %s; "+
				"run them separately", cmd, from, p)
		}
	}
	if from == "" {
		return "", fmt.Errorf("orion %s: no tickets given", cmd)
	}
	if flag == "" {
		return from, nil
	}
	if f := strings.ToUpper(strings.TrimSpace(flag)); f != from {
		return "", fmt.Errorf("orion %s: --project %s does not match the keys, "+
			"which are %s tickets; one of the two is wrong", cmd, f, from)
	}
	return from, nil
}

// printFixPlan reports what will change BEFORE anything is written.
//
// The preview is the whole reason a range is safe to type: it lists the
// tickets the range expanded to, so a ticket the operator did not picture is
// visible before it moves rather than after.
func printFixPlan(w io.Writer, project, target string, p fixPlan) {
	ui.Ok(w, "plan", "%s on %s: %d to add, %d to move, %d already there, "+
		"%d already shipped, %d not found",
		target, project, len(p.Add), len(p.Move), len(p.Already),
		len(p.Shipped), len(p.Missing))
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
	for _, m := range p.Shipped {
		ui.Warn(w, "shipped  %s  already released in %s",
			m.Key, strings.Join(m.From, ", "))
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
	key, err := projectForKeys("release add", project, keys)
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

	// Which milestones have already shipped, so a move OFF one is refused.
	// A failure to list is not fatal: it degrades to the old behaviour rather
	// than blocking the command, and says so, because a lookup that cannot
	// answer must not silently read as "nothing has shipped".
	released := map[string]bool{}
	if vs, lerr := j.ListVersions(key); lerr == nil {
		for _, existing := range vs {
			if existing.Released {
				released[existing.Name] = true
			}
		}
	} else {
		ui.Warn(w, "could not read %s's milestones, so a move off a released "+
			"one cannot be caught here: %v", key, lerr)
	}

	plan := planFixVersion(v.Name, keys, current, released)
	printFixPlan(w, key, v.Name, plan)

	// A ticket that already shipped is not moved. Its release is public --
	// the changelog and the release notes name it -- and a milestone that no
	// longer lists it makes those records disagree with Jira.
	if plan.blocked() && !force {
		ui.Fail(w, "%d ticket(s) already shipped in a released milestone; "+
			"moving one rewrites a history that is already public.", len(plan.Shipped))
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			"If the milestone is genuinely wrong, re-run with --force."))
		os.Exit(1)
	}

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

	// --force was given, so the refusals proceed as ordinary moves. Merged
	// here rather than in planFixVersion so the plan the operator READ stays
	// the plan that was decided: the refusal is still printed as a refusal
	// above, and forcing it does not rewrite that account after the fact.
	if len(plan.Shipped) > 0 {
		ui.Warn(w, "--force: moving %d ticket(s) off a released milestone", len(plan.Shipped))
		plan.Move = append(plan.Move, plan.Shipped...)
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
