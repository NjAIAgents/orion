package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// Tickets written for agents grew past what a person can scan, and the fix is
// in the mould rather than in the tickets already filed (OR-164): the
// decompose prompt is what /pm-plan is handed, so a shape stated there holds
// for generated items as well as hand-written ones.
//
// These assert the three things a reader loses if the shape regresses: the
// scannable head, the rule that separates the two readers, and the
// cite-do-not-restate rule that keeps the body from growing back.
func decomposePrompt(t *testing.T) string {
	t.Helper()
	p, err := stagePrompt(ws(t, ""), "decompose", config.Toolkit{})
	if err != nil {
		t.Fatalf("decompose: %v", err)
	}
	return p
}

func TestDecomposePromptCarriesTheTicketShape(t *testing.T) {
	p := decomposePrompt(t)
	if !strings.Contains(p, "WHY:") {
		t.Errorf("the decompose prompt never asks for the WHY line:\n%s", p)
	}
	if !strings.Contains(p, "---") {
		t.Errorf("the decompose prompt never shows the rule that separates the "+
			"human-scannable head from the agent's grounding:\n%s", p)
	}
	if !strings.Contains(p, "## Open questions") {
		t.Errorf("the decompose prompt never gives open questions their own "+
			"section, so they stay buried in the body:\n%s", p)
	}
}

// The order is the requirement, not the presence of the parts. A WHY below
// the rule, or open questions below scope, reads as a long ticket again.
func TestTicketShapeOrdersTheHumanPartAboveTheRule(t *testing.T) {
	rule := strings.Index(ticketShape, "\n---\n")
	if rule < 0 {
		t.Fatalf("the ticket shape has no horizontal rule:\n%s", ticketShape)
	}
	why := strings.Index(ticketShape, "WHY:")
	if why < 0 || why > rule {
		t.Errorf("WHY must sit ABOVE the rule, where a triaging human reads:\n%s", ticketShape)
	}
	open := strings.Index(ticketShape, "## Open questions")
	if open < rule {
		t.Fatalf("open questions belong below the rule:\n%s", ticketShape)
	}
	for _, later := range []string{"## Scope", "## Grounding", "## Tests"} {
		if i := strings.Index(ticketShape, later); i < 0 || i < open {
			t.Errorf("%s must come after ## Open questions, which is what makes "+
				"an open question visible without reading the body:\n%s", later, ticketShape)
		}
	}
}

// A shape missing a section is worse than no shape at all: the planner fills
// the gap with whatever it would have written anyway, and the ticket looks
// compliant while carrying none of the section the reader relies on being
// there. This pins every section, not just the three the order test walks.
func TestTicketShapeHasEveryRequiredSection(t *testing.T) {
	for _, want := range []string{
		"<One sentence: what changes.>",
		"WHY:",
		"---",
		"## Open questions",
		"## Scope",
		"## Grounding",
		"## Tests",
	} {
		if !strings.Contains(ticketShape, want) {
			t.Errorf("the ticket shape is missing %q:\n%s", want, ticketShape)
		}
	}
}

// The shape is worthless if it only ever describes the parent Epic or Story.
// This is the line that binds it to every item the planner writes, not a
// sample of them.
func TestDecomposePromptInstructsEveryItem(t *testing.T) {
	p := decomposePrompt(t)
	if !strings.Contains(p, "Write EVERY item's description in this shape") {
		t.Errorf("the decompose prompt never tells the planner the shape applies "+
			"to every tracker item, not just some of them:\n%s", p)
	}
}

// The body grows back the moment a ticket is allowed to paste the code or
// re-argue a rejected alternative, which is what made these unreadable in the
// first place. OR-161 records those decisions as ADRs precisely so a ticket
// can reference instead of restate.
func TestDecomposePromptSaysCiteRatherThanRestate(t *testing.T) {
	p := decomposePrompt(t)
	if !strings.Contains(p, "docs/decisions/") {
		t.Errorf("the decompose prompt never sends prior-art reasoning to an ADR:\n%s", p)
	}
	if !strings.Contains(strings.ToLower(p), "cite") {
		t.Errorf("the decompose prompt never tells the planner to cite a file "+
			"rather than quote it at length:\n%s", p)
	}
}
