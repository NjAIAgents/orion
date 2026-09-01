package tracker

// Dependency-aware queueing (OR-95).
//
// The queue was a flat label search: it claimed whatever Jira returned, in
// whatever order Jira returned it, and nothing read issue links. So a ticket
// queued behind work that does not exist yet was indistinguishable from one
// that was ready, and an agent could be spawned against a codebase where the
// thing it is meant to build on has not been written.
//
// The cost of getting that wrong is not one wasted run. It is a wasted run,
// plus a merge conflict, plus a person working out which of two parallel
// implementations of the same idea is the real one.
//
// THIS IS CODE, NOT AN AGENT, and that is deliberate. Ordering a queue is
// deterministic work with a correct answer: a topological check over blocker
// links returns the same result every time, costs nothing, runs on every
// tick, and can be tested. A model deciding queue order returns a PLAUSIBLE
// order, and a queue that orders differently on identical input is a bug
// nobody can reproduce. Judgement about the backlog is a different question
// and belongs to a different role.

import (
	"fmt"
	"sort"
	"strings"
)

// Blocked is one ticket that cannot be worked yet, and why.
type Blocked struct {
	Key string
	// By are the unresolved blockers, sorted. Named rather than counted,
	// because "blocked" without the blocker sends the reader to Jira to find
	// out the one thing they wanted to know.
	By []string
	// Cycle is set when the ticket is part of a dependency cycle rather than
	// merely waiting on something. A different problem with a different fix:
	// waiting resolves itself, a cycle never does.
	Cycle bool
}

// Reason is the sentence `orion queue` prints.
func (b Blocked) Reason() string {
	if b.Cycle {
		return "in a dependency cycle with " + strings.Join(b.By, ", ") +
			"; neither can be worked until the links are corrected"
	}
	return "blocked by " + strings.Join(b.By, ", ")
}

// Ready splits candidates into those that may be claimed now and those that
// may not, with a reason for each of the latter.
//
// Named Ready rather than Workable because Workable already means something
// narrower in this package -- children.go asks it of SUB-TASKS, which is a
// different relationship: a sub-task is part of its parent, a blocker is a
// precondition. Two functions with one name would invite the two to be
// confused at exactly the call site where the distinction matters.
//
// resolved answers, for keys NOT in candidates, both "is it finished?" and
// "do we know anything about it at all?" -- a blocker is usually a ticket
// that merged last week and carries no label, so it is not in the queue.
//
// TWO RETURN VALUES, not one, because there are three states and a boolean
// holds two. A blocker that is known and still open must HOLD its ticket; a
// blocker nobody can see must not. Collapsing those loses whichever case the
// author was not thinking about, and a test caught exactly that here.
//
// Nil means nothing outside the candidate set is known either way.
//
// ORDER IS PRESERVED. Dependencies decide what is ELIGIBLE; Jira's own Rank
// decides what goes first among those, and people already curate it. A
// topological sort that reordered the ready set would silently overrule the
// backlog somebody arranged by hand.
func Ready(candidates []Issue, resolved func(key string) (done, known bool)) (ready []Issue, blocked []Blocked) {
	if resolved == nil {
		resolved = func(string) (bool, bool) { return false, false }
	}
	byKey := make(map[string]Issue, len(candidates))
	for _, i := range candidates {
		byKey[norm(i.Key)] = i
	}

	inCycle := cycles(byKey)

	for _, i := range candidates {
		if members, ok := inCycle[norm(i.Key)]; ok {
			blocked = append(blocked, Blocked{Key: i.Key, By: members, Cycle: true})
			continue
		}
		var waiting []string
		for _, b := range i.BlockedBy {
			if open, known := blockerOpen(norm(b), byKey, resolved); known && open {
				waiting = append(waiting, b)
			}
		}
		if len(waiting) > 0 {
			sort.Strings(waiting)
			blocked = append(blocked, Blocked{Key: i.Key, By: waiting})
			continue
		}
		ready = append(ready, i)
	}
	return ready, blocked
}

// blockerOpen reports whether a blocker is still outstanding, and whether
// anything is known about it at all.
//
// AN UNKNOWN BLOCKER DOES NOT BLOCK. A link may point at a ticket in another
// project, or one this query cannot see, or one Jira declined to return. The
// alternative -- treating unknown as unresolved -- produces a ticket that can
// NEVER be worked because of a reference nobody can inspect, which is the
// same failure as a required status check that never reports. Reported as
// satisfied, and said out loud by the caller rather than hidden.
func blockerOpen(key string, byKey map[string]Issue,
	resolved func(string) (bool, bool)) (open, known bool) {

	if i, ok := byKey[key]; ok {
		// In the candidate set. Usually that means queued and unfinished, but
		// a ticket can be labelled and Done at once, so its status is asked
		// rather than assumed.
		return !i.Resolved() && !isDone(i.Status), true
	}
	done, known := resolved(key)
	return known && !done, known
}

// cycles finds every ticket that is part of a dependency cycle, mapping it to
// the others in that cycle.
//
// A -> B -> A is a data error, not a scheduling problem. It is detected,
// BOTH keys are reported, and NEITHER is worked. Silently picking one is
// worse than refusing both: the pick would be arbitrary, would differ between
// runs, and would produce an ordering nobody could explain.
func cycles(byKey map[string]Issue) map[string][]string {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current path
		black = 2 // finished
	)
	colour := make(map[string]int, len(byKey))
	out := map[string][]string{}

	// Sorted so the walk is deterministic. Two runs over identical input must
	// report the same cycle members in the same order, or the message changes
	// between ticks for no reason.
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var path []string
	var walk func(k string)
	walk = func(k string) {
		switch colour[k] {
		case grey:
			// Found the loop: everything from k onwards on the current path.
			at := 0
			for i, p := range path {
				if p == k {
					at = i
					break
				}
			}
			ring := append([]string(nil), path[at:]...)
			sort.Strings(ring)
			for _, m := range ring {
				out[m] = others(ring, m)
			}
			return
		case black:
			return
		}
		colour[k] = grey
		path = append(path, k)
		i := byKey[k]
		blockers := append([]string(nil), i.BlockedBy...)
		sort.Strings(blockers)
		for _, b := range blockers {
			if _, ok := byKey[norm(b)]; ok {
				walk(norm(b))
			}
		}
		path = path[:len(path)-1]
		colour[k] = black
	}
	for _, k := range keys {
		if colour[k] == white {
			walk(k)
		}
	}
	return out
}

func others(ring []string, self string) []string {
	var out []string
	for _, r := range ring {
		if r != self {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		// A ticket blocked by itself. Absurd, and possible: say so rather
		// than printing "in a cycle with " and nothing.
		return []string{self}
	}
	return out
}

func norm(k string) string { return strings.ToUpper(strings.TrimSpace(k)) }

// DescribeBlocked is the one-line form for `orion queue`.
func DescribeBlocked(b Blocked) string {
	return fmt.Sprintf("%-8s %s", b.Key, b.Reason())
}
