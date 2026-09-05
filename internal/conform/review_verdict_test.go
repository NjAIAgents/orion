package conform_test

// Review's handling of the three reply shapes a model can actually hand back
// (OR-158): both markers in one reply, neither marker, and a transport error.
// conform_test.go (internal package) already covers the ordinary CONFORMS and
// DIVERGES: paths; this file covers what Review does at the edges of that
// contract, through the exported API only.

import (
	"errors"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/conform"
)

func planned() conform.Evidence {
	return conform.Evidence{
		Key:  "OR-158",
		Plan: []conform.Source{{Path: "docs/recommendations/confirmed/OR-158.md", Text: "index the ledger by issuer"}},
		Diff: conform.Diff{Stat: "1 file changed", Patch: "diff --git a/x b/x"},
	}
}

// A reply naming both markers has still named a specific departure, and the
// named part is what a person can check -- so DIVERGES wins and the named
// departure reaches the verdict, not a silent CONFORMS.
func TestReviewBothMarkersInOneReplyDivergesWins(t *testing.T) {
	v := conform.Review(planned(), func(conform.Evidence) (string, error) {
		return conform.ReplyDiverges + " the plan says one index per issuer; the diff adds a composite one\n" +
			conform.ReplyConforms, nil
	})

	if !v.Diverged() {
		t.Fatalf("CONFORMS silenced a named divergence: %+v", v)
	}
	if len(v.Divergences) != 1 {
		t.Fatalf("got %d divergences, want the one named departure: %+v", len(v.Divergences), v.Divergences)
	}
	if !strings.Contains(v.Divergences[0].What, "composite") {
		t.Errorf("the named departure was dropped: %q", v.Divergences[0].What)
	}
	if v.Summary() != "diverges from the confirmed plan" {
		t.Errorf("Summary = %q, want the diverges wording even though CONFORMS also appeared", v.Summary())
	}
}

// A reply with neither marker is not a divergence and not a match -- it is
// reviewed=true (a model was asked and answered) with a note that says the
// question went unanswered and carries what the model actually said, cut to a
// bound so one wall of prose does not blow out the audit record.
func TestReviewUnparseableReplyRecordsWentUnansweredWithTruncatedText(t *testing.T) {
	reply := "I looked at both documents closely and " + strings.Repeat("this is not a verdict. ", 20)
	if len(reply) <= 200 {
		t.Fatalf("test fixture too short to exercise truncation: %d chars", len(reply))
	}

	v := conform.Review(planned(), func(conform.Evidence) (string, error) { return reply, nil })

	if !v.Reviewed {
		t.Error("a reply that named no marker was recorded as never reviewed, " +
			"which reads identically to a ticket with no plan or no model")
	}
	if v.Diverged() {
		t.Errorf("an unparseable reply produced a divergence out of nothing: %+v", v)
	}
	if !strings.Contains(v.Note, "went unanswered") {
		t.Errorf("the note does not say the question went unanswered: %q", v.Note)
	}
	if !strings.Contains(v.Note, reply[:100]) {
		t.Errorf("the note does not carry the model's actual reply: %q", v.Note)
	}
	if strings.Contains(v.Note, reply[len(reply)-20:]) {
		t.Errorf("the reply was not truncated in the note: %q", v.Note)
	}
}

// A transport or process error from the model is caught and written down; it
// must never propagate out of Review as a panic or a returned error, because
// the entire point of this pass is that its own failure cannot touch the
// pipeline it sits beside.
func TestReviewModelErrorIsCaughtAsANoteNotACrash(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Review panicked on a model error instead of recording it: %v", r)
		}
	}()

	v := conform.Review(planned(), func(conform.Evidence) (string, error) {
		return "", errors.New("exited 1: context deadline exceeded")
	})

	if v.Diverged() {
		t.Errorf("a model error produced a divergence out of nothing: %+v", v)
	}
	if !strings.Contains(v.Note, "exited 1") {
		t.Errorf("the error was swallowed instead of recorded: %q", v.Note)
	}
	if len(v.Plan) != 1 {
		t.Errorf("the plan sources were dropped from a verdict that could not be reached: %v", v.Plan)
	}
}
