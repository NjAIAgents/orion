package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/discovery"
)

// OR-151: the PM stage's whole deliverable is one file, and Orion reads it
// twice -- the discovery gate blocks every later stage on its open questions,
// and advise's PM role answers product questions out of it. So the shape the
// prompt tells the agent to write has to be the shape internal/discovery
// actually parses. If the two drift, a capture full of unanswered questions
// reports none, the gate passes, and spec, plan, scaffold and the tracker tree
// are all designed from an ambiguity nobody was ever shown.
//
// Round-tripped through discovery.Assess rather than asserted as a string:
// finding "## Open questions" in the prompt proves the prompt says it, not
// that the gate finds it.
func TestIntentPromptWritesWhatTheDiscoveryGateParses(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent")
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	// Shown indented, because that is how every block in these prompts is
	// quoted; what the agent writes into the file is the constant itself,
	// which is what the round-trip below feeds to the gate.
	if !strings.Contains(p, quote(intentShape)) {
		t.Fatalf("the intent prompt does not show the agent the shape it must write:\n%s", p)
	}
	if !strings.Contains(p, intentNone) {
		t.Errorf("the intent prompt never tells the agent how to say there are none, "+
			"and an absent section reads the same as an answered one:\n%s", p)
	}

	// The shape as shown: the gate must find the section by its heading and
	// count the question under it. Zero here means the heading was reworded
	// into something discovery does not match.
	if a := assessIntent(t, intentShape); a.Open != 1 {
		t.Errorf("the shape the prompt shows parses as %d open question(s), want 1;\n"+
			"the prompt and internal/discovery have drifted apart:\n%s", a.Open, intentShape)
	}

	// The ways the prompt tells the agent to settle one in place. Each must
	// clear the gate, or a capture that answered everything still blocks the
	// chain and the only way out is deleting the question.
	for _, bullet := range []string{
		intentNone,
		"- [x] One bullet per thing you could not decide.",
		"- One bullet per thing you could not decide. Answer: it is per issuer.",
		"- ~~One bullet per thing you could not decide.~~",
	} {
		body := "## Open questions\n" + bullet
		if a := assessIntent(t, body); a.Open != 0 {
			t.Errorf("a question settled the way the prompt describes still blocks "+
				"the chain (%d open):\n%s", a.Open, body)
		}
	}
}

// Success measures have no parser behind them, so the prompt is the only thing
// that can require them -- which is exactly why they are the part that goes
// missing. Asked for as a heading in the shape, not as a sentence the agent can
// satisfy with a paragraph about ambitions.
func TestIntentPromptRequiresSuccessMeasures(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent")
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	if !strings.Contains(intentShape, "## Success measures") {
		t.Errorf("the shape the intent stage must write carries no success measures:\n%s", intentShape)
	}
	if !strings.Contains(p, "SUCCESS MEASURES ARE PART OF THE DELIVERABLE") {
		t.Errorf("the intent prompt does not say success measures are part of the "+
			"deliverable, so they are the first thing dropped under time pressure:\n%s", p)
	}
}

// An ambiguity the PM cannot settle must become an open question, never an
// assumption: an assumption written as a statement is indistinguishable from a
// decision by the time the next stage reads it.
func TestIntentPromptForbidsAssumptions(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent")
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	if !strings.Contains(p, "NEVER AN ASSUMPTION") {
		t.Errorf("the intent prompt does not forbid inventing an answer:\n%s", p)
	}
	if !strings.Contains(strings.ToLower(p), "never delete a question") {
		t.Errorf("the intent prompt does not forbid deleting a question to unblock "+
			"the chain, which is the cheapest way past the gate:\n%s", p)
	}
	// One artifact, not two: internal/discovery parses this file and
	// advise.Artifacts scopes the PM role to it.
	if !strings.Contains(p, "EXTEND THE FILE") {
		t.Errorf("the intent prompt does not tell a re-run to extend the capture "+
			"that is already there:\n%s", p)
	}
}

// assessIntent writes a capture and reads it back through the real gate.
func assessIntent(t *testing.T, body string) discovery.Assessment {
	t.Helper()
	path := filepath.Join(t.TempDir(), "thing.md")
	if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := discovery.Assess(path)
	if !a.Found {
		t.Fatalf("discovery could not read the capture at %s", path)
	}
	return a
}
