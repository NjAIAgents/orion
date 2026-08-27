package supervisor

import (
	"strings"
	"testing"
)

// A Story's Tasks are a checklist inside ONE piece of work, not a set of
// separate jobs. Orion was flat -- the queue is a label search and the unit
// of work is whatever carries the label -- so a decomposed Story was either
// invisible (label the Story, the agent never learns the Tasks exist) or a
// hazard (label each Task, and two Tasks touching one file collide on
// separate branches). Two tickets both creating src/fcia/cli.py from scratch
// is what that looks like in practice.

func TestAFlatTicketPromptIsUnchanged(t *testing.T) {
	p := TicketPrompt("FCIA-9", "do the thing", "details", "https://x/9", "", nil)

	if strings.Contains(p, "SUB-TASKS") {
		t.Error("a ticket with no children must not grow a sub-task section")
	}
	if !strings.Contains(p, "and only this issue") {
		t.Errorf("the single-issue scope instruction was lost:\n%s", p)
	}
}

func TestSubTasksArriveAsAnOrderedChecklist(t *testing.T) {
	kids := []Child{
		{Key: "OR-51", Summary: "add the endpoint", Description: "serve JSON"},
		{Key: "OR-52", Summary: "render it", Description: "the card grid"},
	}
	p := TicketPromptWithChildren("OR-50", "orion web", "the dashboard", "https://x/50", "", nil, kids)

	// Order is load-bearing: these are the steps of one change, and "render
	// it" before "add the endpoint" writes code against nothing.
	i1, i2 := strings.Index(p, "OR-51"), strings.Index(p, "OR-52")
	if i1 < 0 || i2 < 0 {
		t.Fatalf("both sub-tasks must appear:\n%s", p)
	}
	if i1 > i2 {
		t.Error("sub-tasks were reordered; rank order is the sequence a person set")
	}
	if !strings.Contains(p, "1.") || !strings.Contains(p, "2.") {
		t.Error("numbered, so the agent cannot mistake the list for a set")
	}

	// One deliverable, said explicitly. Without this an agent reasonably
	// reads two tasks as two things to finish and split.
	for _, want := range []string{"one branch", "one pull", "in this order"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt must say %q:\n%s", want, p)
		}
	}
	// The opening scope line must not still say "only this issue" when the
	// issue is explicitly several.
	if strings.Contains(p, "and only this issue") {
		t.Error("the single-issue scope line contradicts the sub-task list")
	}
	// Descriptions carry the actual requirement; a bare summary list would
	// make the agent invent what each task means.
	if !strings.Contains(p, "serve JSON") || !strings.Contains(p, "the card grid") {
		t.Error("each sub-task's description must reach the agent")
	}
}

// The agent must report per sub-task. A run that did four of five and says
// only "done" leaves a person to diff the branch against the board.
func TestTheAgentIsAskedToReportPerSubTask(t *testing.T) {
	p := TicketPromptWithChildren("OR-50", "s", "d", "u", "", nil,
		[]Child{{Key: "OR-51", Summary: "x"}})

	for _, want := range []string{"which sub-task keys you completed", "which you did"} {
		if !strings.Contains(p, want) {
			t.Errorf("must ask for a per-key outcome (%q):\n%s", want, p)
		}
	}
}
