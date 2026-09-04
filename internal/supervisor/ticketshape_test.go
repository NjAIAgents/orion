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
