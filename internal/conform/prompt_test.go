package conform_test

// The seam between the prompt and the parser, the same one internal/done
// pins for its own pass.
//
// internal/supervisor writes the instruction and internal/conform reads the
// reply, and the two agree on two literal strings. Neither imports the other
// -- supervisor is what every stage in Orion runs through and must not depend
// on the package that parses its output -- so nothing but this file stops
// them drifting apart.
//
// The drift is silent in the worst direction. A prompt asking for "MATCHES"
// while the parser looks for "CONFORMS" produces a reply Review treats as
// unanswered, so the pass keeps running, keeps costing a model call, and
// reports "went unanswered" on every ticket forever.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/conform"
	"github.com/orion-sdlc/orion/internal/supervisor"
)

func prompt(truncated bool) string {
	return supervisor.ConformPrompt("OR-158", "index the ledger by issuer",
		"1 file changed", "diff --git a/ledger.go b/ledger.go", truncated)
}

func TestThePromptAsksForExactlyTheMarkersTheParserReads(t *testing.T) {
	p := prompt(false)
	for _, marker := range []string{conform.ReplyConforms, conform.ReplyDiverges} {
		if !strings.Contains(p, marker) {
			t.Errorf("the prompt never states %q, so a reply using it would be an "+
				"accident rather than the contract", marker)
		}
	}
}

// The round trip: what the prompt tells the agent to write is what the parser
// reads back.
func TestARoundTripThroughBothContracts(t *testing.T) {
	if d, ok := conform.ParseReply(conform.ReplyConforms); !ok || len(d) != 0 {
		t.Errorf("the prompt's conforms marker did not parse as conforming: (%v, %v)", d, ok)
	}
	d, ok := conform.ParseReply(conform.ReplyDiverges + " the plan says one index per issuer")
	if !ok || len(d) != 1 {
		t.Fatalf("the prompt's diverges marker did not parse as a divergence: (%v, %v)", d, ok)
	}
	if !strings.Contains(d[0].What, "one index per issuer") {
		t.Errorf("the reason was dropped: %q", d[0].What)
	}
}

// The prompt has to carry the plan and the diff. An agent handed the diff and
// nothing to check it against will invent a plausible plan and then judge the
// change against that.
func TestThePromptCarriesThePlanAndTheDiff(t *testing.T) {
	p := prompt(false)
	for _, want := range []string{"OR-158", "index the ledger by issuer", "diff --git"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt does not carry %q", want)
		}
	}
}

// The two clauses that decide whether the findings are worth reading: the
// plan is the only yardstick, and the answer blocks nothing. Without the
// first, this pass duplicates the review class and QA; without the second, an
// agent softens a real difference or reaches for a severity it cannot act on.
func TestThePromptScopesItToThePlanAndSaysItBlocksNothing(t *testing.T) {
	p := prompt(false)
	if !strings.Contains(p, "THE PLAN IS THE ONLY YARDSTICK") {
		t.Error("the prompt does not scope the agent to the plan, so it will report " +
			"code quality and test coverage that other passes already cover")
	}
	if !strings.Contains(p, "NOTHING IS BLOCKED BY YOUR ANSWER") {
		t.Error("the prompt never says the answer blocks nothing")
	}
	if !strings.Contains(p, "DO NOT CHANGE ANYTHING") {
		t.Error("the prompt does not forbid editing, committing or merging")
	}
}

// Truncated input is missing evidence, not a divergence, and the agent has to
// be told which it is looking at.
func TestTruncatedInputIsDeclaredAsTruncated(t *testing.T) {
	if strings.Contains(prompt(false), "TRUNCATED") {
		t.Error("complete input was announced as truncated")
	}
	if !strings.Contains(prompt(true), "TRUNCATED") {
		t.Error("truncated input was not announced, so a clause that was cut off " +
			"reads to the agent as one the change ignored")
	}
}
