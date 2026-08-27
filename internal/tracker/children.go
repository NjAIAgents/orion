package tracker

import (
	"fmt"
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

// MaxChildren bounds what one run will take on.
//
// A Story with thirty Tasks is a decomposition error, not a big run: the
// agent would hit its turn ceiling somewhere in the middle and leave a
// branch half-done, which costs the whole run and produces something nobody
// wants to review. Refusing with the count is a better answer than starting.
const MaxChildren = 15

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
	// MaxChildren+1 so an over-large tree is DETECTED rather than silently
	// truncated to the cap. Reporting "15 children" for a Story with forty
	// would be a lie told by an off-by-one.
	kids, err := j.Search(fmt.Sprintf("parent = %q ORDER BY Rank ASC", key), MaxChildren+1)
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
