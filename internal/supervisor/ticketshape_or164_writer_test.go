package supervisor

import (
	"strings"
	"testing"
)

// Cases for OR-164 not already covered by ticketshape_test.go /
// ticketshape_or164_qa_test.go: the order check actually catches a
// regression, the rule genuinely hides scope/grounding from a human who
// stops there, the sections below the rule stand on their own for an agent,
// and the rule's position is immune to how long Grounding grows.

// shapeOrdersOpenQuestionsBeforeScope mirrors the check
// TestTicketShapeOrdersTheHumanPartAboveTheRule makes inline, so it can be
// run against both the real shape and a deliberately corrupted one.
func shapeOrdersOpenQuestionsBeforeScope(shape string) bool {
	open := strings.Index(shape, "## Open questions")
	scope := strings.Index(shape, "## Scope")
	return open >= 0 && scope >= 0 && open < scope
}

func TestReorderingOpenQuestionsAfterScopeCausesTestFailure(t *testing.T) {
	if !shapeOrdersOpenQuestionsBeforeScope(ticketShape) {
		t.Fatalf("the shipped ticket shape does not even pass its own order check:\n%s", ticketShape)
	}

	blocks := strings.Split(ticketShape, "\n\n")
	openIdx, scopeIdx := -1, -1
	for i, b := range blocks {
		if strings.HasPrefix(b, "## Open questions") {
			openIdx = i
		}
		if strings.HasPrefix(b, "## Scope") {
			scopeIdx = i
		}
	}
	if openIdx < 0 || scopeIdx < 0 {
		t.Fatalf("could not locate Open questions/Scope blocks to swap:\n%s", ticketShape)
	}
	blocks[openIdx], blocks[scopeIdx] = blocks[scopeIdx], blocks[openIdx]
	reordered := strings.Join(blocks, "\n\n")

	if shapeOrdersOpenQuestionsBeforeScope(reordered) {
		t.Errorf("swapping Open questions and Scope should fail the order check, " +
			"otherwise a regression that reorders the real ticketShape ships undetected")
	}
}

// A human triaging a backlog reads down to the rule and stops (that is the
// entire point of putting it there). If Scope or Grounding text leaked above
// the rule, that reader would form an opinion on scope/grounding without ever
// seeing the section meant to answer it.
func TestHumanStoppingAtRuleCannotDetermineScopeOrGrounding(t *testing.T) {
	rule := strings.Index(ticketShape, "\n---\n")
	if rule < 0 {
		t.Fatalf("the ticket shape has no horizontal rule:\n%s", ticketShape)
	}
	head := ticketShape[:rule]
	for _, section := range []string{"## Scope", "## Grounding"} {
		if strings.Contains(head, section) {
			t.Errorf("a human stopping at the rule would already see %s, which "+
				"defeats the reason the rule is there:\n%s", section, head)
		}
	}
}

// An agent reads past the rule and is expected to build from what is below
// it. If a section below the rule leaned on "see above" instead of stating
// its own instructions, the agent would have to re-read the human-facing
// summary to fill it in.
func TestAgentBelowRuleNeedsNoReferenceBackToSummary(t *testing.T) {
	rule := strings.Index(ticketShape, "\n---\n")
	if rule < 0 {
		t.Fatalf("the ticket shape has no horizontal rule:\n%s", ticketShape)
	}
	body := ticketShape[rule+len("\n---\n"):]

	sections := []string{"## Open questions", "## Scope", "## Grounding", "## Tests"}
	for i, heading := range sections {
		start := strings.Index(body, heading)
		if start < 0 {
			t.Fatalf("the ticket shape is missing %s below the rule:\n%s", heading, ticketShape)
		}
		end := len(body)
		if i+1 < len(sections) {
			if next := strings.Index(body, sections[i+1]); next >= 0 {
				end = next
			}
		}
		content := strings.TrimSpace(body[start+len(heading) : end])
		if content == "" {
			t.Errorf("%s carries no instructions of its own, so an agent building "+
				"from it alone has nothing to go on:\n%s", heading, body)
		}
		lower := strings.ToLower(content)
		for _, backref := range []string{"see above", "as above", "the summary"} {
			if strings.Contains(lower, backref) {
				t.Errorf("%s refers back to the summary (%q) instead of standing "+
					"alone for the agent reading only this half:\n%s", heading, backref, content)
			}
		}
	}
}

// Grounding is meant to grow arbitrarily long -- that is what lets an agent
// build without re-deriving context. A human's readability must not depend
// on how long it gets, because the rule (which sits before Grounding) is
// what tells them where to stop, not the length of what follows it.
func TestReadabilityDoesNotDegradeWhenGroundingSectionIsLong(t *testing.T) {
	rule := strings.Index(ticketShape, "\n---\n")
	if rule < 0 {
		t.Fatalf("the ticket shape has no horizontal rule:\n%s", ticketShape)
	}

	const groundingPlaceholder = "<File paths, existing behaviour, ADR ids. Cite; do not quote at length.>"
	if !strings.Contains(ticketShape, groundingPlaceholder) {
		t.Fatalf("the Grounding placeholder text changed; update this test to match:\n%s", ticketShape)
	}
	longGrounding := groundingPlaceholder + " " + strings.Repeat("Extra grounding detail. ", 200)
	withLongGrounding := strings.Replace(ticketShape, groundingPlaceholder, longGrounding, 1)

	if got := strings.Index(withLongGrounding, "\n---\n"); got != rule {
		t.Errorf("the rule moved from offset %d to %d once Grounding grew, so a "+
			"human can no longer rely on a fixed stopping point", rule, got)
	}
}
