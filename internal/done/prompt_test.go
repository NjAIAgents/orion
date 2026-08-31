package done_test

// The seam between the prompt and the parser.
//
// internal/supervisor writes the instruction and internal/done reads the
// reply, and the two agree on two literal strings. Neither package imports
// the other -- supervisor is what every stage in Orion runs through, and it
// should not depend on the package that parses its output -- so nothing but
// this file stops them drifting apart.
//
// The drift is silent, which is why it is worth a test. A prompt that asked
// for "COMPLETE" while the parser looked for "DONE" would produce a reply the
// parser could not read, which Triage treats as unanswered: the intent check
// would quietly stop running and every verdict would still say done.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/done"
	"github.com/orion-sdlc/orion/internal/supervisor"
)

func donePrompt() string {
	return supervisor.DonePrompt("OR-1", "a summary", "some criteria",
		"1 file changed", "diff --git a/x b/x", false)
}

func TestThePromptAsksForExactlyTheMarkersTheParserReads(t *testing.T) {
	p := donePrompt()
	for _, marker := range []string{done.ReplyDone, done.ReplyNotDone} {
		if !strings.Contains(p, marker) {
			t.Errorf("the prompt never states %q, so a reply using it would be an "+
				"accident rather than the contract", marker)
		}
	}
}

// The round trip: what the prompt tells the agent to write is what the parser
// reads back as a verdict.
func TestARoundTripThroughBothContracts(t *testing.T) {
	if f, ok := done.ParseReply(done.ReplyDone); !ok || f != nil {
		t.Errorf("the prompt's done marker did not parse as done: (%v, %v)", f, ok)
	}
	f, ok := done.ParseReply(done.ReplyNotDone + " the --json flag is missing")
	if !ok || f == nil {
		t.Fatalf("the prompt's not-done marker did not parse as a finding: (%v, %v)", f, ok)
	}
	if !strings.Contains(f.Evidence[0], "--json") {
		t.Errorf("the reason was dropped: %v", f.Evidence)
	}
}

// The prompt has to carry the ticket's own words. An agent handed the diff
// and nothing to check it against will invent a plausible requirement and
// then judge the diff against that.
func TestThePromptCarriesTheTicketAndTheDiff(t *testing.T) {
	p := donePrompt()
	for _, want := range []string{"OR-1", "a summary", "some criteria", "diff --git"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt does not carry %q", want)
		}
	}
}

// A truncated diff is missing evidence, not missing work, and the agent has to
// be told which it is looking at.
func TestATruncatedDiffIsDeclaredAsTruncated(t *testing.T) {
	full := supervisor.DonePrompt("OR-1", "s", "c", "stat", "patch", false)
	cut := supervisor.DonePrompt("OR-1", "s", "c", "stat", "patch", true)

	if strings.Contains(full, "TRUNCATED") {
		t.Error("a complete diff was announced as truncated")
	}
	if !strings.Contains(cut, "TRUNCATED") {
		t.Error("a truncated diff was not announced, so a criterion that was cut off " +
			"reads to the agent as one that is missing")
	}
}

// It reports; it does not act. Said in the prompt as well as enforced by the
// caller, because an agent told it may merge will look for a way to, and the
// credentials are in the environment it runs in.
func TestThePromptForbidsChangingAnything(t *testing.T) {
	p := strings.ToLower(donePrompt())
	for _, want := range []string{"do not", "merge", "approve", "commit"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt does not forbid %q", want)
		}
	}
}
