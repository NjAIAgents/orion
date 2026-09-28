package collect

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// OR-314 case 21. The per-branch path (merged, via closeTicket) and the batch
// path (closeLanded, via runBatch) must not drift into two definitions of
// "this ticket is finished" -- they share one function, closeTicket, and a
// call through either entry point has to leave a tracker in the same state.
func TestThePerBranchAndBatchPathsProduceEquivalentResultsThroughCloseTicket(t *testing.T) {
	const prURL = "https://forge/pull/396"

	direct := newTracker()
	var directBuf bytes.Buffer
	if err := closeTicket("OR-150", prURL, "orion-ready", Deps{Jira: direct}, &directBuf); err != nil {
		t.Fatalf("closeTicket (per-branch path): %v", err)
	}

	viaBatch := newTracker()
	var batchBuf bytes.Buffer
	closeLanded([]string{"OR-150"}, prURL, "orion-ready", Deps{Jira: viaBatch}, &batchBuf)

	if direct.transitions["OR-150"] != viaBatch.transitions["OR-150"] {
		t.Errorf("transition = %q via closeTicket, %q via closeLanded: the two "+
			"paths must produce the same result", direct.transitions["OR-150"],
			viaBatch.transitions["OR-150"])
	}
	if !reflect.DeepEqual(direct.removed["OR-150"], viaBatch.removed["OR-150"]) {
		t.Errorf("labels removed = %v via closeTicket, %v via closeLanded",
			direct.removed["OR-150"], viaBatch.removed["OR-150"])
	}
	if !reflect.DeepEqual(direct.comments["OR-150"], viaBatch.comments["OR-150"]) {
		t.Errorf("comments = %v via closeTicket, %v via closeLanded",
			direct.comments["OR-150"], viaBatch.comments["OR-150"])
	}
}

// OR-314 case 22. A batch that goes red still has to close what it actually
// landed. This exercises a real bisected batch -- one member ejected before
// CI, one isolated as the culprit that made CI fail, and the rest landing --
// through Land() rather than a hand-built Batch, then feeds the outcome into
// closeLanded exactly as runBatch does.
func TestABatchThatFailsCIStillClosesOnlyTheMembersItLanded(t *testing.T) {
	g := newFakeGit("orion/or-2")
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3", "OR-4"), nil)
	if err != nil {
		t.Fatal(err)
	}
	landed := b.Members(Landed)
	if len(landed) == 0 {
		t.Fatalf("expected some members to land: %v", b.Describe())
	}

	jira := newTracker()
	var buf bytes.Buffer
	closeLanded(landed, "https://forge/pull/500", "orion-ready", Deps{Jira: jira}, &buf)

	for _, key := range landed {
		if jira.transitions[key] != "Done" {
			t.Errorf("landed member %s: transition = %q, want Done", key, jira.transitions[key])
		}
	}
	for _, key := range []string{"OR-2", "OR-3"} {
		if jira.transitions[key] != "" || len(jira.removed[key]) > 0 || len(jira.comments[key]) > 0 {
			t.Errorf("%s (ejected or culprit) must be untouched, but got "+
				"transition=%q labels=%v comments=%v", key, jira.transitions[key],
				jira.removed[key], jira.comments[key])
		}
	}
}

// OR-314 case 23. runBatch derives the members it closes with b.Members(Landed)
// (batchrun.go), not a hand-rolled filter -- so a test that only checks the
// end state of closeLanded cannot catch a future change to what gets fed
// into it. This asserts the derivation itself: the exact set and order
// b.Members(Landed) returns from a real, bisected batch.
func TestTheSetPassedToCloseLandedIsExactlyBMembersLanded(t *testing.T) {
	g := newFakeGit("orion/or-2")
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-6": true}}

	b, err := Land(g, tr, "batch", "develop",
		members("OR-1", "OR-2", "OR-3", "OR-4", "OR-5", "OR-6", "OR-7", "OR-8"), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Derived independently, by outcome, rather than via b.Members -- so this
	// does not just check b.Members against itself.
	var want []string
	for _, r := range b.Results {
		if r.Outcome == Landed {
			want = append(want, r.Key)
		}
	}
	sort.Strings(want)

	got := b.Members(Landed)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("b.Members(Landed) = %v, want %v -- runBatch passes this exact "+
			"value to closeLanded, so a mismatch here closes the wrong tickets "+
			"in production", got, want)
	}
	if len(got) != 6 {
		t.Errorf("landed %d, want the 6 members that are neither ejected (OR-2 "+
			"conflicts at assembly) nor the culprit (OR-6): %v", len(got), b.Describe())
	}
}

// OR-314 case 24. The comment closeLanded leaves on a landed member's ticket
// has to carry the exact pull request URL the batch was closed with -- not a
// truncated or reformatted version of it, since that URL is how a reader
// finds where the work went.
func TestTheClosingCommentCarriesTheExactPullRequestURL(t *testing.T) {
	const prURL = "https://forge.example.com/org/repo/pull/507?tab=files"
	jira := newTracker()
	var buf bytes.Buffer

	closeLanded([]string{"OR-150"}, prURL, "orion-ready", Deps{Jira: jira}, &buf)

	comments := jira.comments["OR-150"]
	if len(comments) == 0 {
		t.Fatal("expected a comment on the landed member's ticket")
	}
	comment := comments[0]
	if !strings.Contains(comment, prURL) {
		t.Errorf("comment = %q, want it to contain the exact URL %q", comment, prURL)
	}
	if !strings.HasSuffix(strings.TrimSpace(comment), prURL) {
		t.Errorf("comment = %q, want the URL exactly as passed, with nothing "+
			"appended or stripped from it", comment)
	}
}

// OR-558. A story's HUMAN-marked sub-task was not delivered by the story's
// pull request -- it was never offered to the queue, so no agent picked it up
// and nobody did it. Landing the story closed all twenty-eight of LTA-2's
// sub-tasks as "delivered by LTA-2", LTA-30 (a quickstart run a person does)
// among them, which also released the twelve tickets linked behind it.
//
// So: the others close, that one stays open, and the landing comment says so
// -- in the comment a reader is already looking at, not a second one.
func TestALandedStoryLeavesItsHumanMarkedSubTasksOpenAndSaysSo(t *testing.T) {
	const prURL = "https://forge/pull/507"
	jira := newTracker()
	jira.children["LTA-2"] = []tracker.Issue{
		{Key: "LTA-29", StatusCategory: "indeterminate",
			Description: "Wire the parser into the CLI.\n\nPhase: 3\n"},
		{Key: "LTA-30", StatusCategory: "indeterminate",
			Description: "Run quickstart scenario 1 end to end and read the filed issues.\n\n" +
				"HUMAN: the task list marks this as work no agent can do, so Orion " +
				"does not offer it to the queue. A person picks it up.\n"},
		{Key: "LTA-31", StatusCategory: "indeterminate",
			Description: "Add the regression test.\n\nPhase: 3\n"},
	}
	var buf bytes.Buffer

	closeLanded([]string{"LTA-2"}, prURL, "orion-ready", Deps{Jira: jira}, &buf)

	for _, key := range []string{"LTA-29", "LTA-31"} {
		if jira.transitions[key] != "Done" {
			t.Errorf("%s could have been delivered by the story and must close, "+
				"got transition %q", key, jira.transitions[key])
		}
	}
	if jira.transitions["LTA-30"] != "" {
		t.Errorf("LTA-30 is a person's task; nobody did it, and closing it as "+
			"delivered also releases everything linked behind it. Transition = %q",
			jira.transitions["LTA-30"])
	}
	if len(jira.comments["LTA-30"]) > 0 {
		t.Errorf("LTA-30 was told it was delivered: %v", jira.comments["LTA-30"])
	}
	if len(jira.removed["LTA-30"]) > 0 {
		t.Errorf("LTA-30's labels were cleared as though it were finished: %v",
			jira.removed["LTA-30"])
	}

	comments := jira.comments["LTA-2"]
	if len(comments) == 0 {
		t.Fatal("expected a landing comment on the story")
	}
	landing := comments[0]
	if !strings.Contains(landing, prURL) {
		t.Errorf("the landing comment lost the pull request URL: %q", landing)
	}
	if !strings.Contains(landing, "LTA-30") {
		t.Errorf("the landing comment must name the sub-task left open, or the "+
			"story reads as wholly delivered: %q", landing)
	}
	if strings.Contains(landing, "LTA-29") || strings.Contains(landing, "LTA-31") {
		t.Errorf("only the held sub-tasks are named; the closed ones are not "+
			"news: %q", landing)
	}
	if !strings.Contains(buf.String(), "LTA-30") {
		t.Errorf("the console must say which sub-task stayed open, or 2 of 3 "+
			"closing looks like a tracker failure:\n%s", buf.String())
	}
}

// OR-558. One held sub-task reads as one and several read as several: a
// landing comment is read by a person, and "LTA-30 are a person's tasks" is
// the kind of line that makes a reader doubt the rest of it.
func TestTheLandingNoteNamesHeldSubTasksInThePlainestForm(t *testing.T) {
	const prURL = "https://forge/pull/507"
	if got := landingNote(prURL, nil); got != "merged: "+prURL {
		t.Errorf("with nothing held the note is the URL and nothing else, got %q", got)
	}
	one := landingNote(prURL, []string{"LTA-30"})
	if !strings.Contains(one, "LTA-30 is a person's task and stays open.") {
		t.Errorf("one held task: %q", one)
	}
	many := landingNote(prURL, []string{"LTA-30", "LTA-34"})
	if !strings.Contains(many, "LTA-30, LTA-34 are a person's tasks and stay open.") {
		t.Errorf("two held tasks: %q", many)
	}
}
