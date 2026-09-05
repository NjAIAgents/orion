package conform

import (
	"errors"
	"strings"
	"testing"
)

func planned() Evidence {
	return Evidence{
		Key:  "OR-158",
		Plan: []Source{{Path: "docs/recommendations/confirmed/OR-158.md", Text: "index the ledger by issuer"}},
		Diff: Diff{Stat: "1 file changed", Patch: "diff --git a/x b/x"},
	}
}

// THE PROPERTY THE WHOLE PASS RESTS ON. A divergence must not be able to stop
// anything, so there is nothing on the verdict a caller could gate on: no
// Blocked, no Failed, no Pass. If one is ever added, this pass becomes a gate
// and the reason it was built -- that a divergence is often an improvement
// and must be seen rather than acted on -- is gone.
func TestADivergenceCarriesNothingACallerCouldBlockOn(t *testing.T) {
	ev := planned()
	v := Review(ev, func(Evidence) (string, error) {
		return ReplyDiverges + " the plan says one index per issuer; the diff adds a composite one", nil
	})
	if !v.Diverged() {
		t.Fatalf("a DIVERGES reply did not produce a divergence: %+v", v)
	}
	report := v.Report()
	if !strings.Contains(report, "Nothing is blocked") {
		t.Errorf("the report never says the run continues, so a reader meets a "+
			"finding and assumes the pipeline has stopped:\n%s", report)
	}
	if !strings.Contains(report, "composite") {
		t.Errorf("the reason was dropped from the report:\n%s", report)
	}
}

// A ticket with no confirmed plan is not a ticket that matches its plan, and
// reporting the first as the second would make the audit trail read as though
// every change had been checked.
func TestNoConfirmedPlanIsReportedAsNotReviewedAndCostsNothing(t *testing.T) {
	asked := false
	v := Review(Evidence{Key: "OR-158", NoPlan: "no plan artifact on this branch",
		Diff: Diff{Patch: "diff --git a/x b/x"}},
		func(Evidence) (string, error) { asked = true; return ReplyConforms, nil })

	if asked {
		t.Error("a model was paid to compare a change against a plan that does not exist")
	}
	if v.Reviewed || v.Diverged() {
		t.Errorf("a ticket with no plan was reported as reviewed: %+v", v)
	}
	if v.Summary() != "not reviewed" {
		t.Errorf("Summary = %q, want the not-reviewed wording", v.Summary())
	}
	if !strings.Contains(v.Note, "no plan artifact on this branch") {
		t.Errorf("the reason nothing ran was dropped: %q", v.Note)
	}
}

// "checked and matched" and "never checked" are the two facts an auditor is
// telling apart later, so they must not render the same.
func TestConformsAndNotReviewedAreDistinguishable(t *testing.T) {
	conforms := Review(planned(), func(Evidence) (string, error) { return ReplyConforms, nil })
	notRun := Review(planned(), nil)

	if !conforms.Reviewed {
		t.Error("a CONFORMS reply was not recorded as reviewed")
	}
	if notRun.Reviewed {
		t.Error("a pass with no model was recorded as reviewed")
	}
	if conforms.Summary() == notRun.Summary() {
		t.Errorf("both render as %q, so the log cannot say which happened", conforms.Summary())
	}
	if !strings.Contains(notRun.Note, "no model") {
		t.Errorf("a pass that never ran does not say why: %q", notRun.Note)
	}
}

// Missing evidence is not a divergence. A diff that could not be read, an
// empty one, and a model that errored each leave nothing to report on, and
// inventing a finding from any of them would put a claim on a ticket that
// nobody can check.
func TestMissingEvidenceNeverProducesADivergence(t *testing.T) {
	unreadable := planned()
	unreadable.Diff = Diff{Unreadable: "could not fetch the remote"}

	empty := planned()
	empty.Diff = Diff{Stat: "1 file changed", Patch: "   "}

	cases := map[string]Verdict{
		"unreadable diff": Review(unreadable, func(Evidence) (string, error) {
			t.Error("the model was asked about a diff that could not be read")
			return "", nil
		}),
		"empty diff": Review(empty, func(Evidence) (string, error) {
			t.Error("the model was asked about an empty diff")
			return "", nil
		}),
		"model errored": Review(planned(), func(Evidence) (string, error) {
			return "", errors.New("exited 1")
		}),
		"unparseable reply": Review(planned(), func(Evidence) (string, error) {
			return "I think it broadly matches, more or less.", nil
		}),
	}
	for name, v := range cases {
		if v.Diverged() {
			t.Errorf("%s produced a divergence out of nothing: %+v", name, v)
		}
		if strings.TrimSpace(v.Note) == "" {
			t.Errorf("%s left no note, so the log cannot say why nothing was found", name)
		}
	}
	// The plan is still named on a verdict that could not be reached, so the
	// audit record says what it would have been checked against.
	if got := Review(unreadable, nil).Plan; len(got) != 1 {
		t.Errorf("the plan was dropped from an unreviewed verdict: %v", got)
	}
}

func TestParseReply(t *testing.T) {
	if d, ok := ParseReply(ReplyConforms); !ok || len(d) != 0 {
		t.Errorf("CONFORMS parsed as (%v, %v)", d, ok)
	}
	// Decoration in front of the marker must not hide it: a model that writes
	// "- DIVERGES: ..." has still answered.
	d, ok := ParseReply("Here is what I found.\n- " + ReplyDiverges + " the plan asks for X\n" +
		"**" + ReplyDiverges + " and for Y")
	if !ok || len(d) != 2 {
		t.Fatalf("decorated divergences parsed as (%v, %v)", d, ok)
	}
	// A divergence with no reason is unactionable, so it is not a divergence.
	if got, ok := ParseReply(ReplyDiverges + "   "); ok || len(got) != 0 {
		t.Errorf("a reasonless divergence was accepted: (%v, %v)", got, ok)
	}
	// A reply containing both has still named something specific, and the
	// named part is the part a person can check.
	both, ok := ParseReply(ReplyDiverges + " the index is composite\n" + ReplyConforms)
	if !ok || len(both) != 1 {
		t.Errorf("CONFORMS overrode a named divergence: (%v, %v)", both, ok)
	}
	if _, ok := ParseReply("neither one thing nor the other"); ok {
		t.Error("a reply with no marker was read as an answer")
	}
}

// The event log's Detail carries what was found, not merely that something
// was: a trail saying "1 divergence" is one nobody can act on later.
func TestWhatsCarriesTheFindingsForTheAuditRecord(t *testing.T) {
	v := Review(planned(), func(Evidence) (string, error) {
		return ReplyDiverges + " the plan says one index per issuer", nil
	})
	got := v.Whats()
	if len(got) != 1 || !strings.Contains(got[0], "one index per issuer") {
		t.Errorf("Whats() = %v, want the divergence's own words", got)
	}
	if len(v.Plan) != 1 || v.Plan[0] != "docs/recommendations/confirmed/OR-158.md" {
		t.Errorf("the verdict does not name the artifact it was judged against: %v", v.Plan)
	}
}
