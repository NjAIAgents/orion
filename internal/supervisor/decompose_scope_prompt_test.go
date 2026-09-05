package supervisor

// The decompose stage prompt is the only place a declared scope can be
// supplied (OR-260): the queue reads a `Files:` line back off the
// description, and a planner that never saw this guidance would emit trees
// the queue can only discover collide at merge time. These tests pin the
// guidance itself, separately from the golden full-prompt comparison.

import (
	"strings"
	"testing"
)

// The planner is told, in the Scope section, to name a `Files:` line.
func TestDecomposeStagePromptIncludesFilesGuidance(t *testing.T) {
	p := decomposePrompt(t)
	if !strings.Contains(p, "`Files:` line naming the packages, directories or files") {
		t.Errorf("the decompose stage prompt does not carry the Files: guidance:\n%s", p)
	}
}

// A declared scope is a prediction, not a fact, and inventing one is worse
// than omitting it: a wrong scope holds back a ticket that never collided.
func TestDecomposeStagePromptWarnsAgainstInventedScope(t *testing.T) {
	p := decomposePrompt(t)
	if !strings.Contains(p, "allowed to be is invented") {
		t.Errorf("the prompt does not warn against inventing a scope:\n%s", p)
	}
	if !strings.Contains(p, "omit the line entirely rather than guess") {
		t.Errorf("the prompt does not advise omitting the line rather than guessing:\n%s", p)
	}
}

// A parser cannot honestly resolve a collision between two siblings that
// declare the same ground; the planner is given three options and told to
// pick one rather than emit them as independent.
func TestDecomposeStagePromptListsThreeOptionsForCoupledSiblings(t *testing.T) {
	p := decomposePrompt(t)
	for _, want := range []string{"MERGE them into one item", "ORDER them with a real",
		"say in", "both descriptions that they are coupled"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt is missing coupled-sibling guidance %q:\n%s", want, p)
		}
	}
}

// The template itself (ticketShape, quoted into the prompt) carries a Files:
// placeholder under the Scope section, not just prose above or below it.
func TestTicketShapeTemplateIncludesFilesPlaceholderUnderScope(t *testing.T) {
	scopeIdx := strings.Index(ticketShape, "## Scope")
	filesIdx := strings.Index(ticketShape, "Files:")
	if scopeIdx == -1 {
		t.Fatalf("ticketShape has no Scope section:\n%s", ticketShape)
	}
	if filesIdx == -1 || filesIdx < scopeIdx {
		t.Errorf("ticketShape's Files: placeholder is not under the Scope section:\n%s", ticketShape)
	}
	if !strings.Contains(ticketShape, "Omit this line rather than guess") {
		t.Errorf("the Files: placeholder does not itself say to omit rather than guess:\n%s", ticketShape)
	}
}
