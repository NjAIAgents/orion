package collect

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"
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
