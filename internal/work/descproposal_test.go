package work

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/supervisor"
)

// The shape OR-288 needed: an agent whose deliverable is a Jira description
// drafts the replacement instead of ending as an ordinary blocked question.
func TestADescriptionProposalIsRecognised(t *testing.T) {
	final := "Some reasoning here.\n\n" +
		supervisor.DescProposalStart + "\n" +
		"This issue narrows OR-77 to the capability-scoped seam.\n" +
		"It replaces the deleted-surface premise with a scoped one.\n" +
		supervisor.DescProposalEnd + "\n" +
		"Ready for review."

	got, ok := descProposalDeclared(final)
	if !ok {
		t.Fatal("expected a proposal to be recognised")
	}
	if !strings.Contains(got, "capability-scoped seam") || !strings.Contains(got, "scoped one") {
		t.Errorf("the extracted text lost content: %q", got)
	}
	if strings.Contains(got, supervisor.DescProposalStart) || strings.Contains(got, supervisor.DescProposalEnd) {
		t.Errorf("the markers themselves leaked into the extracted text: %q", got)
	}
}

// An ordinary blocked question, with no proposal markers at all, must not be
// misread as one -- that would silently turn every unrelated question into
// a Jira write request.
func TestAnOrdinaryQuestionIsNotAProposal(t *testing.T) {
	final := "I could not determine the API contract without OR-50. " +
		"Should I wait, or build against a contract you give me now?"
	if _, ok := descProposalDeclared(final); ok {
		t.Error("an ordinary question must not be read as a description proposal")
	}
}

// A START with no END is not a proposal. Treating a truncated block as one
// would send unreviewed partial prose toward the Jira approval gate.
func TestAnUnclosedBlockIsNotAProposal(t *testing.T) {
	final := supervisor.DescProposalStart + "\nSome text that never closes."
	if _, ok := descProposalDeclared(final); ok {
		t.Error("a block missing its end marker must not be treated as a proposal")
	}
}

// An empty block is refused for the same reason SetDescription itself
// refuses an empty body: more likely a template that failed to render than
// an intention.
func TestAnEmptyBlockIsNotAProposal(t *testing.T) {
	final := supervisor.DescProposalStart + "\n" + supervisor.DescProposalEnd
	if _, ok := descProposalDeclared(final); ok {
		t.Error("an empty proposal block must be refused, not forwarded")
	}
}

// The markers must start a line, the same rule NoopMarker follows -- an
// agent quoting its own instructions in a paragraph must not accidentally
// trigger the block.
func TestMarkersMustStartALine(t *testing.T) {
	final := "The instructions said to write " + supervisor.DescProposalStart +
		" and " + supervisor.DescProposalEnd + " but I have nothing to propose."
	if _, ok := descProposalDeclared(final); ok {
		t.Error("markers embedded mid-sentence must not trigger a proposal")
	}
}
