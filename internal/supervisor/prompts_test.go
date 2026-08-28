package supervisor

import (
	"strings"
	"testing"
)

// OR-157: a patch derived from a symptom often just addresses the symptom --
// that is what burns a fix attempt, and the ceiling is three
// (ci.max_fix_attempts), so a wasted attempt is a third of the budget.
// FixPrompt must require the agent to state a root cause, distinct from the
// failure text itself, before it is told to make the change.

func TestFixPromptRequiresRootCauseBeforeThePatch(t *testing.T) {
	p := FixPrompt("OR-1", "orion/or-1-fix", "TestSomething failed: expected 1, got 2")

	if !strings.Contains(p, "ROOT CAUSE") {
		t.Fatalf("FixPrompt must ask for a stated root cause:\n%s", p)
	}

	// Order is load-bearing: the agent must be told to name the root cause
	// before it is told to make the change, not after.
	iRootCause := strings.Index(p, "ROOT CAUSE")
	iWhatToDo := strings.Index(p, "WHAT TO DO")
	if iRootCause < 0 || iWhatToDo < 0 {
		t.Fatalf("both sections must appear:\n%s", p)
	}
	if iRootCause > iWhatToDo {
		t.Errorf("root cause must be stated before WHAT TO DO, not after:\n%s", p)
	}

	if !strings.Contains(p, "root cause") {
		t.Errorf("the patch instruction must point back at the stated root cause, not the symptom:\n%s", p)
	}
}

func TestFixPromptStillNamesTheFailureAndTheGuardrails(t *testing.T) {
	p := FixPrompt("OR-1", "orion/or-1-fix", "boom")

	for _, want := range []string{
		"boom",
		"Do not delete, skip, weaken or rewrite a test",
		"Do not push, merge, or open a pull request",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("expected FixPrompt to still contain %q:\n%s", want, p)
		}
	}
}
