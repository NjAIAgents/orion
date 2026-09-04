package supervisor

import (
	"strings"
	"testing"
)

// QA coverage for OR-164's actual promise: a ticket a human can scan in
// about 30 seconds by reading only down to the rule, with everything an
// agent needs to build sitting below it and standing on its own. These pin
// the read-time behaviour rather than just the presence of the sections
// (ticketshape_test.go and ticketshape_or164_qa_test.go already cover that).

// Moving WHY below the rule, or letting Open questions sink under Scope,
// is the exact regression OR-164 fixed: either one puts information a
// human needs to triage back inside the body they no longer read in full.
func TestTicketShapeDegradesIfWhyBelowRuleOrOpenQuestionsBelowScope(t *testing.T) {
	rule := strings.Index(ticketShape, "\n---\n")
	if rule < 0 {
		t.Fatalf("the ticket shape has no horizontal rule:\n%s", ticketShape)
	}
	why := strings.Index(ticketShape, "WHY:")
	if why < 0 || why > rule {
		t.Errorf("WHY sits below the rule, which is the readability regression "+
			"OR-164 fixed -- a human scanning only to the rule loses it:\n%s", ticketShape)
	}
	openQuestions := strings.Index(ticketShape, "## Open questions")
	scope := strings.Index(ticketShape, "## Scope")
	if openQuestions < 0 || scope < 0 {
		t.Fatalf("the ticket shape is missing Open questions or Scope:\n%s", ticketShape)
	}
	if openQuestions > scope {
		t.Errorf("Open questions is buried below Scope, so an unresolved "+
			"question is no longer visible without reading the body:\n%s", ticketShape)
	}
}

// A human triaging priority reads only down to the rule. If that read
// requires anything from below it -- the WHY, or the one-sentence summary
// restated -- the rule has stopped doing its job and the ticket is a long
// one again in practice, even though the sections are technically there.
func TestTicketShapeBelowRuleCarriesNoTriageInfoOnItsOwn(t *testing.T) {
	rule := strings.Index(ticketShape, "\n---\n")
	if rule < 0 {
		t.Fatalf("the ticket shape has no horizontal rule:\n%s", ticketShape)
	}
	below := ticketShape[rule+len("\n---\n"):]
	if strings.Contains(below, "WHY:") {
		t.Errorf("WHY appears below the rule, so a human triaging priority "+
			"from the section below the rule alone would still find it there "+
			"instead of it living only in the scannable head:\n%s", below)
	}
	if strings.Contains(below, "<One sentence: what changes.>") {
		t.Errorf("the one-sentence summary is restated below the rule, so "+
			"triage information leaks into the part a human does not read:\n%s", below)
	}
}

// "Read to the rule in about 30 seconds" is a claim about how much text
// sits above it, not just that a sentence and a WHY line exist somewhere.
// Cap the head at a handful of non-empty lines so the shape cannot regrow
// into a paragraph a human has to slow down for.
func TestTicketShapeHeadIsShortEnoughToScanInSeconds(t *testing.T) {
	rule := strings.Index(ticketShape, "\n---\n")
	if rule < 0 {
		t.Fatalf("the ticket shape has no horizontal rule:\n%s", ticketShape)
	}
	head := ticketShape[:rule]
	var nonEmpty []string
	for _, line := range strings.Split(head, "\n") {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	const maxScannableLines = 3
	if len(nonEmpty) > maxScannableLines {
		t.Errorf("the head above the rule has %d non-empty lines, too many to "+
			"read in about 30 seconds; want at most %d:\n%s",
			len(nonEmpty), maxScannableLines, head)
	}
	if !strings.Contains(head, "<One sentence: what changes.>") || !strings.Contains(head, "WHY:") {
		t.Errorf("the head above the rule is missing the one-sentence summary "+
			"or WHY, so a human reading only to the rule cannot understand the "+
			"ticket's purpose:\n%s", head)
	}
}

// An agent building from Grounding must not need to go back and read the
// summary or WHY -- those are written for a human's triage judgement, not
// for what to build. A Grounding section that refers back to them ("as
// above", "per the summary") makes the two halves depend on each other
// again, which is the coupling the rule exists to remove.
func TestTicketShapeGroundingStandsAloneWithoutReferringToSummaryOrWhy(t *testing.T) {
	i := strings.Index(ticketShape, "## Grounding")
	if i < 0 {
		t.Fatalf("the ticket shape has no Grounding section:\n%s", ticketShape)
	}
	j := strings.Index(ticketShape[i:], "## Tests")
	if j < 0 {
		t.Fatalf("the ticket shape has no Tests section after Grounding:\n%s", ticketShape)
	}
	section := ticketShape[i : i+j]
	for _, backref := range []string{"as above", "see above", "per the summary", "per the WHY"} {
		if strings.Contains(strings.ToLower(section), backref) {
			t.Errorf("the Grounding section refers back to the summary/WHY "+
				"(%q), so an agent cannot build from it without also reading "+
				"the human-facing head:\n%s", backref, section)
		}
	}
	if !strings.Contains(section, "File paths") {
		t.Errorf("the Grounding section never tells the agent to name file "+
			"paths, which is what lets it build without the summary/WHY:\n%s", section)
	}
}
