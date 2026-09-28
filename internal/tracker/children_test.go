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

// OR-558. Case-sensitive: "human" in lowercase is ordinary prose, not the
// marker decompose writes, so it must not withhold closing a sub-task.
func TestHumanMarkerIsCaseSensitive(t *testing.T) {
	if HumanOnly(Issue{Description: "human: this is not the marker\n"}) {
		t.Error("lowercase 'human' was read as the marker")
	}
}

// OR-558. The marker needs a word boundary after HUMAN, not just the
// substring -- otherwise a task about a HUMANOID robot or a HUMANITARIAN
// effort would silently stay open forever.
func TestHumanMarkerRequiresAWordBoundary(t *testing.T) {
	for _, desc := range []string{
		"HUMANOID walkthrough for the demo.\n",
		"HUMANITARIAN response plan.\n",
		"HUMANIZE the error messages.\n",
		"HUMAN_ONLY flag on the config.\n",
		"HUM AN gap in the copy.\n",
	} {
		if HumanOnly(Issue{Description: desc}) {
			t.Errorf("%q read as the HUMAN marker despite no word boundary", desc)
		}
	}
}

// OR-558. Leading whitespace before HUMAN is still line-start -- decompose's
// own formatting may indent the marker, and requiring column zero exactly
// would make an ordinary blank-quoted line stop being recognised.
func TestHumanMarkerWithLeadingWhitespaceIsLineStart(t *testing.T) {
	for _, desc := range []string{
		"Do the thing.\n\n  HUMAN: needs a person.\n",
		"Do the thing.\n\n\tHUMAN: needs a person.\n",
	} {
		if !HumanOnly(Issue{Description: desc}) {
			t.Errorf("%q with leading whitespace was not read as the marker", desc)
		}
	}
}

// OR-558. `(?m)^` anchors to every line start in the string, not just the
// start of the string -- the marker three lines into a description must
// still be found.
func TestHumanMarkerOnLineThreeIsRecognised(t *testing.T) {
	desc := "Step one.\nStep two.\nHUMAN: a person does this.\nStep four.\n"
	if !HumanOnly(Issue{Description: desc}) {
		t.Errorf("marker on line 3 was not recognised:\n%s", desc)
	}
}

// OR-558. A marker mid-line -- not preceded only by whitespace since the
// last newline -- is prose, not the tag, and must not be read as one.
func TestHumanMarkerInTheMiddleOfALineIsNotMatched(t *testing.T) {
	desc := "Ping the HUMAN reviewer before merging.\n"
	if HumanOnly(Issue{Description: desc}) {
		t.Errorf("mid-line HUMAN was read as the marker: %q", desc)
	}
}

// OR-558. The marker needs no colon -- only the word and a boundary -- so a
// bare "HUMAN" at line start, or one followed by other punctuation, is still
// recognised.
func TestHumanMarkerWithoutAColonIsStillMatched(t *testing.T) {
	for _, desc := range []string{
		"HUMAN needs to review this by hand.\n",
		"HUMAN, see the runbook for steps.\n",
	} {
		if !HumanOnly(Issue{Description: desc}) {
			t.Errorf("%q without a colon was not recognised as the marker", desc)
		}
	}
}

// OR-558. Nil, empty, or whitespace-only descriptions must not crash the
// check, and none of them read as a person's task.
func TestHumanOnlyOnEmptyDescriptionDoesNotCrash(t *testing.T) {
	for _, desc := range []string{"", "   ", "\n\n\t\n"} {
		if HumanOnly(Issue{Description: desc}) {
			t.Errorf("%q (empty/whitespace) was read as the HUMAN marker", desc)
		}
	}
	if HumanOnly(Issue{}) {
		t.Error("a zero-value Issue was read as the HUMAN marker")
	}
}

// OR-558. Only the Description field is tracker-visible for this marker --
// decompose never writes it into the Summary, so a marker sitting in the
// Summary of an otherwise ordinary task must not be read as a person's task.
func TestHumanMarkerIsCheckedOnlyOnDescription(t *testing.T) {
	i := Issue{Summary: "HUMAN: this is in the summary, not the description", Description: "Do the thing.\n"}
	if HumanOnly(i) {
		t.Error("a HUMAN marker in Summary (not Description) was read as the marker")
	}
}

// OR-558. HumanOnly is a pure function: the same Issue value always answers
// the same way, with no state carried between calls.
func TestHumanOnlyIsPure(t *testing.T) {
	i := Issue{Description: "HUMAN: a person's task.\n"}
	first := HumanOnly(i)
	for n := 0; n < 5; n++ {
		if HumanOnly(i) != first {
			t.Fatal("HumanOnly gave a different answer for the same input across calls")
		}
	}
}
