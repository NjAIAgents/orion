package supervisor

import (
	"strings"
	"testing"
)

// Command substitution must not touch Orion's own half of the decompose
// stage. The `orion routes` marker instruction is Orion's contract, not the
// planner's, and prompts.go says as much: it is carried verbatim rather than
// restated so it cannot drift from the table `orion routes` prints.
func TestDecomposePromptKeepsRoutingInstructionVerbatimWithConfiguredCommand(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "decompose", tk("decompose", "/their-plan"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p,
		"Run `orion routes` before you write the tree, and set the marker it names on") {
		t.Errorf("configuring the decompose command dropped or altered the routing "+
			"instruction, which must survive verbatim:\n%s", p)
	}
}

// The intent prompt's heading contract with internal/discovery must survive
// command substitution: the gate finds "Success measures" and "Open
// questions" BY HEADING, so a substitution that reworded or dropped either
// one would make a complete intent parse as having open questions forever.
func TestIntentPromptKeepsRequiredHeadingsWithConfiguredCommand(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent", tk("intent", "/their-capture"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "## Success measures") {
		t.Errorf("configuring the intent command dropped the \"Success measures\" "+
			"heading the discovery gate relies on:\n%s", p)
	}
	if !strings.Contains(p, "## Open questions") {
		t.Errorf("configuring the intent command dropped the \"Open questions\" "+
			"heading the discovery gate relies on:\n%s", p)
	}
}

// The scaffold prompt tells the agent to read the spec before choosing a
// stack. That path reference is Orion's own instruction, independent of
// which scaffold command is configured, and must still point at the real
// spec path after substitution.
func TestScaffoldPromptKeepsSpecPathReferenceWithConfiguredCommand(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "scaffold", tk("scaffold", "/their-scaffold"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "specs/thing.spec.md") {
		t.Errorf("configuring the scaffold command dropped the spec path reference:\n%s", p)
	}
}
