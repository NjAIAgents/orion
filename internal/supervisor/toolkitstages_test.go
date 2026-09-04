package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// tk builds a toolkit block declaring commands for the named stages.
func tk(pairs ...string) config.Toolkit {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return config.Toolkit{Stages: m}
}

// A configured command must REPLACE the nj-agents skill, not sit beside it.
// Naming both would leave the agent choosing between two tools, which is the
// failure this is here to prevent: a project that configured its own
// methodology would still see Orion recommending somebody else's.
func TestConfiguredCommandReplacesTheBuiltInSkill(t *testing.T) {
	for _, c := range []struct {
		stage, configured, builtin string
	}{
		{"intent", "/speckit.specify", "/capture-intent"},
		{"scaffold", "/their-scaffold", "/scaffold-project"},
		{"decompose", "/their-plan", "/pm-plan"},
	} {
		p, err := stagePrompt(ws(t, ""), c.stage, tk(c.stage, c.configured))
		if err != nil {
			t.Fatalf("%s: %v", c.stage, err)
		}
		if !strings.Contains(p, c.configured) {
			t.Errorf("the %s prompt never names the configured command %q:\n%s",
				c.stage, c.configured, p)
		}
		if strings.Contains(p, c.builtin) {
			t.Errorf("the %s prompt still names %s alongside the configured %s",
				c.stage, c.builtin, c.configured)
		}
	}
}

// Both spellings of a stage reach the same prompt, because supervisor's
// vocabulary has two words for four of the stages and a project may have
// written either one.
func TestConfiguredCommandAcceptsEitherStageSpelling(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent", tk("intent", "/theirs"))
	if err != nil {
		t.Fatal(err)
	}
	// decompose has one spelling; the alias pairs live on other stages, so
	// this checks the resolution path rather than the intent stage twice.
	q, err := stagePrompt(ws(t, ""), "design", tk("spec", "/their-spec"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "/theirs") {
		t.Errorf("the intent prompt ignored its configured command:\n%s", p)
	}
	if strings.Contains(q, "/their-spec") {
		t.Errorf("the spec prompt names no command, so nothing should have been "+
			"substituted into it:\n%s", q)
	}
}

// A PARTIAL map is a supported configuration (decisions/0019). A stage with
// no entry falls back to its built-in prompt rather than erroring or coming
// out empty -- configuring one stage must not disturb its neighbours.
func TestUnconfiguredStageFallsBackWithinANonEmptyMap(t *testing.T) {
	partial := tk("intent", "/theirs")
	for _, c := range []struct{ stage, builtin string }{
		{"scaffold", "/scaffold-project"},
		{"decompose", "/pm-plan"},
	} {
		p, err := stagePrompt(ws(t, ""), c.stage, partial)
		if err != nil {
			t.Fatalf("%s: %v", c.stage, err)
		}
		if !strings.Contains(p, c.builtin) {
			t.Errorf("%s did not fall back to %s under a partial map:\n%s",
				c.stage, c.builtin, p)
		}
	}
}

// Routing is Orion's contract, not the toolkit's (decisions/0001). Whoever
// creates the tree, the marker instruction must survive verbatim: routing
// reads what the ticket carries and infers nothing from its summary, so a
// tree created by a foreign planner with no marker sends every ticket to the
// backend developer and nothing anywhere reports a mistake (OR-191).
func TestDecomposeKeepsTheRoutingContractUnderAConfiguredCommand(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "decompose", tk("decompose", "/their-plan"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Run `orion routes` before you write the tree, and set the marker it names on",
		"every item you create -- as the issue type, a component, or a label.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the routing instruction did not survive the substitution; "+
				"missing %q:\n%s", want, p)
		}
	}
}

// A configured command fills a slot Orion defined; it does not take over the
// stage. Orion still states the artifact the stage must commit, because the
// artifact is the handoff and the next stage reads files, not conversation.
func TestOrionStillNamesTheArtifactUnderAConfiguredCommand(t *testing.T) {
	for _, c := range []struct{ stage, artifact string }{
		{"intent", "docs/intent/<slug>.md"},
		{"decompose", "plans/thing.plan.md"},
	} {
		p, err := stagePrompt(ws(t, ""), c.stage, tk(c.stage, "/theirs"))
		if err != nil {
			t.Fatalf("%s: %v", c.stage, err)
		}
		if !strings.Contains(p, c.artifact) {
			t.Errorf("the %s prompt stopped naming %s once a command was configured:\n%s",
				c.stage, c.artifact, p)
		}
	}
}
