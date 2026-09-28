package tracker

import "testing"

// A finished sub-task is context, not work. Passing it to an agent invites
// it to redo something a person completed by hand -- which it cannot tell
// from the text, because a Task's description says what to do, not whether
// it was done.
func TestDoneChildrenAreNotWork(t *testing.T) {
	kids := []Issue{
		{Key: "OR-51", Status: "To Do"},
		{Key: "OR-52", Status: "Done"},
		{Key: "OR-53", Status: "In Progress"},
		{Key: "OR-54", Status: "Closed"},
		{Key: "OR-55", Status: "Cancelled"},
	}
	got := Workable(kids)
	if len(got) != 2 {
		t.Fatalf("workable = %d, want 2 (To Do + In Progress); got %+v", len(got), got)
	}
	for _, g := range got {
		if g.Key == "OR-52" || g.Key == "OR-54" || g.Key == "OR-55" {
			t.Errorf("%s is finished and was handed to the agent anyway", g.Key)
		}
	}
}

// Status vocabulary varies by workflow, and treating an unknown status as
// Done would silently drop real work. Unknown means workable.
func TestAnUnfamiliarStatusIsTreatedAsWork(t *testing.T) {
	got := Workable([]Issue{{Key: "OR-56", Status: "Awaiting Review"}})
	if len(got) != 1 {
		t.Error("an unrecognised status was assumed finished; that silently drops work")
	}
}

// A HUMAN-marked sub-task is work no agent can do, and its description is the
// only place a tracker records that -- so this is what stops it being closed
// as delivered by the story it sits under (OR-558).
func TestAHumanMarkedIssueIsRecognisedFromItsDescription(t *testing.T) {
	human := Issue{Key: "LTA-30", Description: "Run quickstart scenario 1 end to end.\n\n" +
		"HUMAN: the task list marks this as work no agent can do, so Orion does " +
		"not offer it to the queue. A person picks it up.\n"}
	if !HumanOnly(human) {
		t.Errorf("the marker decompose writes was not recognised:\n%s", human.Description)
	}
	ordinary := Issue{Key: "LTA-31", Description: "Add the endpoint.\n\nPhase: 3\n"}
	if HumanOnly(ordinary) {
		t.Error("an ordinary task read as a person's task; its sub-tasks would " +
			"then never be closed")
	}
}

// The marker is a line of its own, not a word. A description that discusses
// humans in prose is ordinary work, and reading it as a person's task would
// leave a delivered sub-task open for ever.
func TestProseAboutHumansIsNotTheHumanMarker(t *testing.T) {
	for _, desc := range []string{
		"Render the human-readable summary in the digest.",
		"The human reviewer sees the diff before it merges.",
		"HUMANE, the vendor, publishes the schema we consume.",
	} {
		if HumanOnly(Issue{Description: desc}) {
			t.Errorf("%q read as a HUMAN marker", desc)
		}
	}
}
