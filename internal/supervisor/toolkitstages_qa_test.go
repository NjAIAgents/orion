package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// Whitespace around a configured command must be trimmed before it is
// substituted into the prompt -- a project's config is hand-edited JSON, and
// a stray space or newline around the value should not survive into text an
// agent reads as a literal command to type.
func TestConfiguredCommandWhitespaceIsTrimmedBeforeSubstitution(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent", tk("intent", "  /theirs  "))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "Use the /theirs skill") {
		t.Errorf("the configured command was not trimmed before substitution:\n%s", p)
	}
	if strings.Contains(p, "  /theirs  ") {
		t.Errorf("the untrimmed command leaked into the prompt:\n%s", p)
	}
}

// An unknown stage name must still return the same error it always has, not
// silently produce an empty prompt now that a toolkit block is threaded
// through -- the toolkit config never changes what counts as a valid stage.
func TestUnknownStageStillReturnsErrorNotEmptyPrompt(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "no-such-stage", config.Toolkit{})
	if err == nil {
		t.Fatalf("want an error for an unknown stage, got prompt: %q", p)
	}
	if p != "" {
		t.Errorf("want an empty prompt alongside the error, got: %q", p)
	}
}

// The headless note is appended by stagePrompt itself, after stageBody
// resolves the configured command -- it must survive on every stage
// regardless of whether that stage's command came from config or the
// built-in fallback.
func TestHeadlessNoteAppendsToEveryStagePromptEvenWithConfiguredCommands(t *testing.T) {
	configured := tk("intent", "/theirs", "scaffold", "/their-scaffold", "decompose", "/their-plan")
	for _, stage := range goldenStages {
		p, err := stagePrompt(ws(t, ""), stage, configured)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if !strings.Contains(p, "THIS RUN IS HEADLESS") {
			t.Errorf("the %s prompt is missing the headless note under a configured "+
				"toolkit block:\n%s", stage, p)
		}
	}
}

// Partial config, the other direction from TestUnconfiguredStageFallsBackWithinANonEmptyMap:
// when only decompose is configured, the intent prompt still names its own
// tool as /capture-intent (intent itself is unconfigured) but the same
// prompt's cross-reference to the decompose stage ("later points at this
// capture as grounding") must resolve to the configured decompose command,
// not /pm-plan -- that reference is resolved independently of whether intent
// itself has a configured command (case 7 of OR-298's QA case list).
func TestIntentPromptNamesOwnBuiltinButResolvesConfiguredDecomposeReference(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent", tk("decompose", "/their-plan"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "Use the /capture-intent skill") {
		t.Errorf("intent is unconfigured, so its own prompt must still name "+
			"/capture-intent:\n%s", p)
	}
	if !strings.Contains(p, "/their-plan later points at this capture as grounding") {
		t.Errorf("the intent prompt's reference to the decompose stage must resolve "+
			"the configured decompose command, not the /pm-plan builtin:\n%s", p)
	}
	if strings.Contains(p, "/pm-plan") {
		t.Errorf("the intent prompt still names the /pm-plan builtin even though "+
			"decompose was configured:\n%s", p)
	}
}
