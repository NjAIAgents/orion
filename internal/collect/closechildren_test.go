package collect

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// OR-558. The landing comment is a nice-to-have on top of the close itself --
// the transitions and label clears are what a re-run and the rest of Orion
// actually rely on. A tracker that accepts every write except the comment
// must still leave the ticket, and its workable children, correctly closed.
func TestCloseTicketCompletesEvenWhenTheLandingCommentFailsToPost(t *testing.T) {
	jira := newTracker()
	jira.children["OR-500"] = []tracker.Issue{
		{Key: "OR-501", Description: "Wire the parser into the CLI.\n"},
	}
	jira.commentErr = errors.New("comment API rejected the request")
	var buf bytes.Buffer

	if err := closeTicket("OR-500", "https://forge/pull/9", "orion-ready", Deps{Jira: jira}, &buf); err != nil {
		t.Fatalf("closeTicket returned an error from a failed comment: %v", err)
	}
	if jira.transitions["OR-500"] != "Done" {
		t.Errorf("the story was not transitioned to Done despite the comment failing: %q", jira.transitions["OR-500"])
	}
	if jira.transitions["OR-501"] != "Done" {
		t.Errorf("the sub-task was not closed despite the comment failing: %q", jira.transitions["OR-501"])
	}
}

// closeChildren's return value is the contract closeTicket and landingNote
// both depend on: nil when there was nothing to hold back, non-nil (and
// naming the right keys) only when a HUMAN-marked sub-task was actually
// found among real children.
func TestCloseChildrenHeldListIsNilWhenThereAreNoChildren(t *testing.T) {
	jira := newTracker()
	var buf bytes.Buffer

	held := closeChildren("OR-600", "https://forge/pull/1", "orion-ready", Deps{Jira: jira}, &buf)
	if held != nil {
		t.Errorf("held = %#v, want nil for a ticket with no children", held)
	}
}

// Every workable child was deliverable, so nothing is held back -- but this
// is a distinct path from "no children at all" (the case above), and a
// caller must not have to tell them apart by inspecting the tracker itself.
func TestCloseChildrenHeldListIsEmptyWhenNoChildIsHumanMarked(t *testing.T) {
	jira := newTracker()
	jira.children["OR-601"] = []tracker.Issue{
		{Key: "OR-602", Description: "Add the endpoint.\n"},
		{Key: "OR-603", Description: "Render the response.\n"},
	}
	var buf bytes.Buffer

	held := closeChildren("OR-601", "https://forge/pull/2", "orion-ready", Deps{Jira: jira}, &buf)
	if len(held) != 0 {
		t.Errorf("held = %#v, want none: neither child carries the HUMAN marker", held)
	}
	for _, key := range []string{"OR-602", "OR-603"} {
		if jira.transitions[key] != "Done" {
			t.Errorf("%s should have closed as delivered, got transition %q", key, jira.transitions[key])
		}
	}
}

// OR-558's own shape at scale: many children, one held. The console output
// must report accurate counts on both sides, and closed+held must account
// for every workable child -- nobody vanishes between the two tallies.
func TestCloseChildrenReportsAccurateHeldAndClosedCounts(t *testing.T) {
	jira := newTracker()
	var kids []tracker.Issue
	for i := 1; i <= 27; i++ {
		kids = append(kids, tracker.Issue{
			Key: "LTA-" + strconv.Itoa(i), Description: "An ordinary task.\n",
		})
	}
	kids = append(kids, tracker.Issue{
		Key: "LTA-30",
		Description: "Run quickstart scenario 1 end to end.\n\n" +
			"HUMAN: a person picks it up.\n",
	})
	jira.children["LTA-2"] = kids
	var buf bytes.Buffer

	held := closeChildren("LTA-2", "https://forge/pull/507", "orion-ready", Deps{Jira: jira}, &buf)

	if len(held) != 1 || held[0] != "LTA-30" {
		t.Fatalf("held = %v, want exactly [LTA-30]", held)
	}
	closedCount := 0
	for _, k := range kids {
		if jira.transitions[k.Key] == "Done" {
			closedCount++
		}
	}
	if closedCount != 27 {
		t.Errorf("closed = %d, want 27", closedCount)
	}
	if closedCount+len(held) != len(kids) {
		t.Errorf("closed (%d) + held (%d) = %d, want %d (every workable child accounted for)",
			closedCount, len(held), closedCount+len(held), len(kids))
	}
	out := buf.String()
	if !strings.Contains(out, "27") {
		t.Errorf("console output does not state the closed count: %q", out)
	}
	if !strings.Contains(out, "1 sub-task") && !strings.Contains(out, "LTA-30") {
		t.Errorf("console output does not report the held sub-task: %q", out)
	}
}

// A sub-task's HUMAN marker check runs after Workable's own Done filter --
// something a person already closed by hand is left alone rather than
// examined for the marker or reopened by re-transitioning it.
func TestCloseChildrenNeverExaminesAnAlreadyDoneChild(t *testing.T) {
	jira := newTracker()
	jira.children["OR-700"] = []tracker.Issue{
		{Key: "OR-701", Status: "Done", Description: "HUMAN: irrelevant, already closed by hand.\n"},
		{Key: "OR-702", Description: "Add the endpoint.\n"},
	}
	var buf bytes.Buffer

	held := closeChildren("OR-700", "https://forge/pull/3", "orion-ready", Deps{Jira: jira}, &buf)
	if len(held) != 0 {
		t.Errorf("held = %v, want none: the only HUMAN-marked child was already Done and filtered out", held)
	}
	if _, touched := jira.transitions["OR-701"]; touched {
		t.Errorf("an already-Done child was transitioned again: %q", jira.transitions["OR-701"])
	}
}

// Re-running closeChildren on a story whose held sub-task has since been
// closed by a person must not error or re-hold it: HumanOnly only ever reads
// the description, and Workable has already dropped it because it is Done.
func TestCloseChildrenOnARerunSkipsAHeldSubTaskAPersonHasSinceClosed(t *testing.T) {
	jira := newTracker()
	jira.children["OR-800"] = []tracker.Issue{
		{Key: "OR-801", Status: "Done", Description: "HUMAN: now closed by the person who did it.\n"},
	}
	var buf bytes.Buffer

	held := closeChildren("OR-800", "https://forge/pull/4", "orion-ready", Deps{Jira: jira}, &buf)
	if len(held) != 0 {
		t.Errorf("held = %v, want none: the sub-task is Done and no longer workable", held)
	}
}
