package tracker

import (
	"strings"
)

// A ticket's children, and why Orion cares about them.
//
// Orion was flat: the queue is a label search and the unit of work is
// whatever carries the label. A Story decomposed into Tasks therefore had
// two bad options, and both waste the decomposition:
//
//	label the Story  the agent reads its description and never learns the
//	                 Tasks exist
//	label each Task  each becomes its own branch, PR, CI run and approval --
//	                 and any two Tasks touching the same file collide
//
// The second is not theoretical. FCIA-8 and FCIA-10 were separate tickets
// worked in parallel; both created src/fcia/cli.py from scratch and only
// conflicted by luck. Tasks under ONE Story are more likely to overlap than
// unrelated tickets, not less -- overlapping is what makes them one Story.
//
// So a claimed parent works its children itself, in order, in one branch.

// ManyChildren is where a story is worth a word of warning -- not a refusal.
//
// An earlier version REFUSED above a cap, on the reasoning that a large
// story would exhaust the agent's turn ceiling and leave a branch
// half-finished. That reasoning was sound and the remedy was wrong: stories
// with twenty-five tasks are ordinary, and a tool that will not work them is
// a tool that does not fit how people actually decompose.
//
// The turn ceiling is the real constraint, so the fix is to RAISE IT for a
// big story (see work.turnsFor) rather than to decline the work. This
// threshold now only decides whether to say "this is a big one" out loud,
// which is worth knowing before forty minutes pass.
const ManyChildren = 20

// maxChildrenFetched bounds the QUERY, not the work. Jira's search caps at
// 100 per page and a story with more children than that is a data problem
// rather than a plan, so one page is the honest limit to read.
const maxChildrenFetched = 100

// Children returns the issues whose parent is key, in Jira's own rank order.
//
// One JQL query covers both hierarchies Jira has: classic sub-tasks and the
// child issues of a team-managed project both set `parent`. Asking by parent
// rather than by issue type means a project using either shape works without
// Orion knowing which it is.
//
// Ordered by rank, deliberately. Rank is what a person changes by dragging
// tickets in the backlog, so "do these in this order" is already expressible
// without any syntax -- and the order matters here in a way it does not for
// independent tickets, because a Story's Tasks are usually sequenced by
// dependency ("add the endpoint" before "render it").
func (j *Jira) Children(key string) ([]Issue, error) {
	key = strings.TrimSpace(strings.ToUpper(key))
	if key == "" {
		return nil, nil
	}
	kids, err := j.Search(JQLEq("parent", key)+" ORDER BY Rank ASC", maxChildrenFetched)
	if err != nil {
		// A project whose Jira has no parent field, or a permission that
		// hides children, is not a failure of the run: it means this ticket
		// is worked as itself, which is the old behaviour.
		return nil, err
	}
	return kids, nil
}

// Workable filters children down to the ones an agent should be asked to do.
//
// A child that is already Done is context, not work. Including it invites
// the agent to redo something a person finished by hand, and "it was already
// done" is not a thing an agent can reliably tell from a description.
func Workable(children []Issue) []Issue {
	var out []Issue
	for _, c := range children {
		if isDone(c.Status) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func isDone(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "closed", "resolved", "cancelled", "canceled", "won't do", "wont do":
		return true
	}
	return false
}
