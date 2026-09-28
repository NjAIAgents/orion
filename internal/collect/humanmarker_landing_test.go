package collect

import (
	"strconv"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// OR-558. The exact incident: log-triage-agent's LTA-2 landed with LTA-30
// ("run quickstart scenario 1 end to end") HUMAN-marked among its children.
// Through the real per-branch landing path (closeTicket, not closeChildren
// called directly), LTA-30 must come out of this untouched -- no transition,
// no label change, no comment -- while every ordinary sibling closes.
func TestLandingOnLTA2LeavesLTA30OpenThroughCloseTicket(t *testing.T) {
	jira := newTracker()
	var kids []tracker.Issue
	// Numbered from 3, not 1: the story itself is LTA-2, and a sub-task
	// sharing its parent's key would collide in this fake's map-keyed
	// storage -- a coincidence no real tracker allows, so the range is
	// chosen to avoid it rather than to model it.
	for n := 3; n <= 29; n++ {
		kids = append(kids, tracker.Issue{
			Key: "LTA-" + strconv.Itoa(n), Description: "An ordinary sub-task.\n",
		})
	}
	kids = append(kids, tracker.Issue{
		Key:         "LTA-30",
		Description: "Run quickstart scenario 1 end to end.\n\nHUMAN: a person picks it up.\n",
	})
	jira.children["LTA-2"] = kids

	var buf strings.Builder
	if err := closeTicket("LTA-2", "https://forge/pull/999", "orion-ready", Deps{Jira: jira}, &buf); err != nil {
		t.Fatalf("closeTicket: %v", err)
	}

	if _, touched := jira.transitions["LTA-30"]; touched {
		t.Errorf("LTA-30 was transitioned: %q", jira.transitions["LTA-30"])
	}
	if len(jira.removed["LTA-30"]) > 0 {
		t.Errorf("LTA-30 had labels removed: %v", jira.removed["LTA-30"])
	}
	if len(jira.comments["LTA-30"]) > 0 {
		t.Errorf("LTA-30 was commented on directly: %v", jira.comments["LTA-30"])
	}
	for n := 3; n <= 29; n++ {
		key := "LTA-" + strconv.Itoa(n)
		if jira.transitions[key] != "Done" {
			t.Errorf("%s: transition = %q, want Done", key, jira.transitions[key])
		}
	}
	if !strings.Contains(jira.comments["LTA-2"][0], "LTA-30") {
		t.Errorf("the landing comment on LTA-2 does not name LTA-30: %q", jira.comments["LTA-2"][0])
	}
}

// OR-558. Every non-human sub-task's own comment -- not the story's landing
// comment, the one closeChildren posts on the sub-task itself -- must say
// where the work landed. Nothing else already pins down this exact text.
func TestNonHumanSubTasksReceiveTheDeliveredByMessage(t *testing.T) {
	jira := newTracker()
	jira.children["OR-900"] = []tracker.Issue{
		{Key: "OR-901", Description: "Add the endpoint.\n"},
	}
	var buf strings.Builder

	closeChildren("OR-900", "https://forge/pull/42", "orion-ready", Deps{Jira: jira}, &buf)

	got := jira.comments["OR-901"]
	want := "Orion:\n\ndelivered in OR-900 and merged: https://forge/pull/42"
	if len(got) != 1 || got[0] != want {
		t.Errorf("comment on OR-901 = %v, want exactly [%q]", got, want)
	}
}

// OR-558. A HUMAN-marked sub-task gets no comment of any kind from the close
// path -- not "delivered in", not anything -- since it was never touched.
func TestHumanSubTasksReceiveNoCommentAtAllFromClose(t *testing.T) {
	jira := newTracker()
	jira.children["OR-910"] = []tracker.Issue{
		{Key: "OR-911", Description: "HUMAN: sign the paperwork.\n"},
	}
	var buf strings.Builder

	closeChildren("OR-910", "https://forge/pull/42", "orion-ready", Deps{Jira: jira}, &buf)

	if len(jira.comments["OR-911"]) != 0 {
		t.Errorf("a HUMAN-marked sub-task received a comment: %v", jira.comments["OR-911"])
	}
}

// OR-558. The console reports the held sub-tasks' own keys, not just a
// count -- a reader following up needs to know which tickets to look at.
func TestConsoleOutputNamesTheHeldSubTaskKeys(t *testing.T) {
	jira := newTracker()
	jira.children["OR-920"] = []tracker.Issue{
		{Key: "OR-921", Description: "HUMAN: rotate the credential.\n"},
		{Key: "OR-922", Description: "HUMAN: confirm with the vendor.\n"},
	}
	var buf strings.Builder

	closeChildren("OR-920", "https://forge/pull/42", "orion-ready", Deps{Jira: jira}, &buf)

	out := buf.String()
	if !strings.Contains(out, "2 sub-task") {
		t.Errorf("console does not state the held count: %q", out)
	}
	for _, key := range []string{"OR-921", "OR-922"} {
		if !strings.Contains(out, key) {
			t.Errorf("console does not name held sub-task %s: %q", key, out)
		}
	}
}

// OR-558. Symmetric with the held case above: the console names the closed
// sub-tasks' own keys alongside the count.
func TestConsoleOutputNamesTheClosedSubTaskKeys(t *testing.T) {
	jira := newTracker()
	jira.children["OR-930"] = []tracker.Issue{
		{Key: "OR-931", Description: "Add the endpoint.\n"},
		{Key: "OR-932", Description: "Wire the client.\n"},
	}
	var buf strings.Builder

	closeChildren("OR-930", "https://forge/pull/42", "orion-ready", Deps{Jira: jira}, &buf)

	out := buf.String()
	if !strings.Contains(out, "2 sub-task") {
		t.Errorf("console does not state the closed count: %q", out)
	}
	for _, key := range []string{"OR-931", "OR-932"} {
		if !strings.Contains(out, key) {
			t.Errorf("console does not name closed sub-task %s: %q", key, out)
		}
	}
}

// OR-558. The marker decompose writes and collect reads is the literal
// "HUMAN:" prefix -- not "[HUMAN]" (that bracket form is what the task-list
// syntax uses and is stripped before it ever reaches a tracker body).
func TestTheHumanMarkerIsTheHumanColonPrefixNotTheBracketForm(t *testing.T) {
	if !tracker.HumanOnly(tracker.Issue{Description: "HUMAN: a person picks it up.\n"}) {
		t.Error("the HUMAN: prefix form was not recognised as the marker")
	}
	if tracker.HumanOnly(tracker.Issue{Description: "[HUMAN] a person picks it up.\n"}) {
		t.Error("the bracket task-list form was read as the tracker-body marker; " +
			"it should already have been stripped by the time collect sees it")
	}
}
