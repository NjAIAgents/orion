package supervisor

import (
	"strings"
	"testing"
)

// QA verification for OR-164: tickets a human can scan lead with three lines,
// grounding stays below the rule. These pin the parts of ticketshape_test.go's
// coverage that are about WHAT the grounding/ADR instructions say, not just
// that the words "cite" or "docs/decisions/" appear somewhere in the prompt.

// The Grounding section itself, not just the surrounding prose, must tell the
// planner to name a file rather than paste it -- a quoted excerpt goes stale
// in place while the code moves on, so the instruction has to live where the
// planner is filling the section in, not only in the paragraph above it.
func TestTicketShapeGroundingCitesFilePathsNotExcerpts(t *testing.T) {
	i := strings.Index(ticketShape, "## Grounding")
	if i < 0 {
		t.Fatalf("the ticket shape has no Grounding section:\n%s", ticketShape)
	}
	section := ticketShape[i:]
	if !strings.Contains(section, "File paths") {
		t.Errorf("the Grounding section never asks for file paths:\n%s", section)
	}
	if !strings.Contains(section, "do not quote at length") {
		t.Errorf("the Grounding section never tells the planner to cite rather "+
			"than quote at length:\n%s", section)
	}
}

// The decompose prompt must tell the planner to point at an ADR rather than
// re-argue the alternatives it rejected -- that reasoning belongs in the one
// file the decision lives in, not copied into every ticket that touches it.
func TestDecomposePromptPointsToADRsInsteadOfRearguingAlternatives(t *testing.T) {
	p := decomposePrompt(t)
	if !strings.Contains(p, "docs/decisions/NNNN-*.md") {
		t.Errorf("the decompose prompt never shows the ADR file pattern to "+
			"reference:\n%s", p)
	}
	if !strings.Contains(p, "re-arguing the alternatives you rejected") {
		t.Errorf("the decompose prompt never tells the planner to reference an "+
			"ADR rather than re-argue the alternatives it rejected:\n%s", p)
	}
}

// The ticket shape's Grounding line names ADR ids alongside file paths, so a
// generated ticket citing a design decision points at the ADR id rather than
// restating the reasoning inline.
func TestTicketShapeGroundingNamesADRIdsForDesignDecisions(t *testing.T) {
	i := strings.Index(ticketShape, "## Grounding")
	if i < 0 {
		t.Fatalf("the ticket shape has no Grounding section:\n%s", ticketShape)
	}
	section := ticketShape[i:]
	if !strings.Contains(section, "ADR ids") {
		t.Errorf("the Grounding section never asks for ADR ids, so a generated "+
			"ticket has nothing telling it to cite the decision instead of "+
			"restating it:\n%s", section)
	}
}

// WHY is capped at two lines in the shape itself, not just described as short
// prose elsewhere -- the cap has to be the thing the planner copies into every
// item's description, or the ticket grows back past what a human can scan.
func TestTicketShapeWhyCapsAtTwoLines(t *testing.T) {
	for _, line := range strings.Split(ticketShape, "\n") {
		if strings.HasPrefix(line, "WHY:") {
			if !strings.Contains(line, "Two lines maximum") {
				t.Errorf("the WHY line does not cap itself at two lines:\n%q", line)
			}
			return
		}
	}
	t.Fatalf("the ticket shape has no WHY line:\n%s", ticketShape)
}

// Epic, Story and Task are not given separate shapes: the decompose prompt
// hands the planner exactly ONE ticketShape, so an Epic's description is
// built from the same Grounding/Scope/Tests sections as a Task's rather than
// each item type growing its own variant over time.
func TestDecomposePromptAppliesOneShapeToEveryItemType(t *testing.T) {
	p := decomposePrompt(t)
	for _, heading := range []string{"## Grounding", "## Scope", "## Tests"} {
		if n := strings.Count(p, heading); n != 1 {
			t.Errorf("expected exactly one %s section handed to the planner so "+
				"Epic, Story and Task descriptions all follow the same shape, "+
				"got %d:\n%s", heading, n, p)
		}
	}
}

// An item with nothing unresolved must still write the heading with "None"
// under it, not omit the section -- an absent "## Open questions" reads as
// "nobody checked" while a present one filled with "None" reads as "checked,
// found nothing". The shape has to spell out that fallback for the planner
// to copy, or a clean item quietly drops the section instead.
func TestTicketShapeOpenQuestionsSectionDefaultsToNone(t *testing.T) {
	i := strings.Index(ticketShape, "## Open questions")
	if i < 0 {
		t.Fatalf("the ticket shape has no Open questions section:\n%s", ticketShape)
	}
	section := ticketShape[i:]
	if j := strings.Index(section, "## Scope"); j >= 0 {
		section = section[:j]
	}
	if !strings.Contains(section, "None") {
		t.Errorf("the Open questions section never tells the planner to write "+
			"\"None\" when there is nothing unresolved, so a clean item has no "+
			"instruction for what to put there instead of just omitting the "+
			"section:\n%s", section)
	}
}

// The Grounding section has to ask for file paths and the existing behaviour
// a change sits against, not just forbid long quotation -- a section that
// only says "don't paste code" tells the planner what NOT to do without ever
// saying what belongs there instead, and it drifts back to prose the moment
// nobody is watching.
func TestTicketShapeGroundingListsFilePathsAndExistingBehaviorWithoutQuoting(t *testing.T) {
	i := strings.Index(ticketShape, "## Grounding")
	if i < 0 {
		t.Fatalf("the ticket shape has no Grounding section:\n%s", ticketShape)
	}
	section := ticketShape[i:]
	if !strings.Contains(section, "File paths") {
		t.Errorf("the Grounding section never asks for file paths:\n%s", section)
	}
	if !strings.Contains(section, "existing behaviour") {
		t.Errorf("the Grounding section never asks for the existing behaviour a "+
			"change sits against:\n%s", section)
	}
	if !strings.Contains(section, "do not quote at length") {
		t.Errorf("the Grounding section never tells the planner to cite rather "+
			"than quote code at length:\n%s", section)
	}
}

// All four headings have to reach the planner in the decompose prompt, not
// just live in the ticketShape constant -- a heading present in the Go
// source but dropped before the prompt is assembled would still ship broken
// tickets, since the planner only ever sees the assembled prompt.
func TestDecomposePromptContainsAllFourSectionHeaders(t *testing.T) {
	p := decomposePrompt(t)
	for _, heading := range []string{
		"## Open questions", "## Scope", "## Grounding", "## Tests",
	} {
		if !strings.Contains(p, heading) {
			t.Errorf("the decompose prompt is missing the %s heading, so a "+
				"generated ticket has no instruction to include it:\n%s", heading, p)
		}
	}
}

// The horizontal rule is a pinned literal, not an implied structure --
// removing it collapses the shape back into one undifferentiated block that
// a human has to read in full to find the WHY line, which is the exact
// regression OR-164 fixed.
func TestTicketShapeHasHorizontalRule(t *testing.T) {
	if !strings.Contains(ticketShape, "\n---\n") {
		t.Errorf("the ticket shape has no horizontal rule separating the "+
			"human-scannable head from the agent's grounding:\n%s", ticketShape)
	}
}

// The cite-rather-than-restate instruction is the specific phrase that keeps
// the Grounding section from growing back into pasted code and re-argued
// alternatives -- pinning the exact wording, not just the word "cite"
// appearing somewhere in the prompt, catches a rewrite that keeps a vague
// citation nod but drops the "do not quote at length" half of the rule.
func TestTicketShapeGroundingHasCiteRatherThanRestateInstruction(t *testing.T) {
	if !strings.Contains(ticketShape, "Cite; do not quote at length.") {
		t.Errorf("the ticket shape's Grounding section never tells the planner "+
			"to cite rather than quote at length:\n%s", ticketShape)
	}
}
