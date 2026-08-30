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

// The ticket pairs this change with OR-129: the root cause is asked for as
// the FIRST LINE of the closing message specifically so it surfaces in the
// console summary. A root cause mentioned only inside the commit body would
// not satisfy that -- it has to be the lead line of what the agent says when
// it stops.
func TestFixPromptAsksForRootCauseAsFirstLineOfClosingMessage(t *testing.T) {
	p := FixPrompt("OR-1", "orion/or-1-fix", "boom")

	if !strings.Contains(p, "COMMITS") {
		t.Fatalf("expected a COMMITS section:\n%s", p)
	}
	commits := p[strings.Index(p, "COMMITS"):]

	if !strings.Contains(commits, "Lead your closing message with the root cause") {
		t.Errorf("expected the COMMITS section to instruct leading the closing message with the root cause:\n%s", commits)
	}
}

// The instruction to make the change must be tied back to the stated root
// cause, not merely mention the phrase "root cause" somewhere unrelated.
// This is what distinguishes OR-157 from a prompt that just adds a
// diagnosis step nobody's told to act on.
func TestFixPromptPatchInstructionTiesToTheStatedRootCause(t *testing.T) {
	p := FixPrompt("OR-1", "orion/or-1-fix", "boom")

	i := strings.Index(p, "WHAT TO DO")
	if i < 0 {
		t.Fatalf("expected a WHAT TO DO section:\n%s", p)
	}
	// Bound the section to what comes before the next all-caps heading.
	rest := p[i:]
	if j := strings.Index(rest[len("WHAT TO DO"):], "\n\n"); j >= 0 {
		rest = rest[:len("WHAT TO DO")+j]
	}

	if !strings.Contains(rest, "root cause") {
		t.Errorf("WHAT TO DO must instruct fixing the stated root cause, not just the symptom:\n%s", rest)
	}
	if !strings.Contains(rest, "not") && !strings.Contains(rest, "symptom") {
		t.Errorf("WHAT TO DO must explicitly rule out patching the symptom instead:\n%s", rest)
	}
}

// A failure log is not guaranteed non-empty (e.g. a truncated capture, or a
// non-test CI failure with no assertion text) -- FixPrompt must still ask
// for a root cause rather than silently skip the section.
func TestFixPromptRequiresRootCauseEvenWithEmptyFailureText(t *testing.T) {
	p := FixPrompt("OR-1", "orion/or-1-fix", "")

	if !strings.Contains(p, "ROOT CAUSE") {
		t.Errorf("FixPrompt must still ask for a root cause when the failure text is empty:\n%s", p)
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

// OR-191: routing reads a marker off the created ticket, and until this
// prompt said so nothing that created a ticket knew the vocabulary existed
// -- so every ticket took the default while the run log correctly announced
// the default on every run.
//
// The prompt must send the planner to `orion routes` and must NOT restate
// the keywords. A copy of the vocabulary in a prompt is a second copy, and
// it drifts from the table routing actually reads the moment either changes;
// Orion owns the tracker contract and the skill applies it (CLAUDE.md's
// precedence rule).
func TestDecomposePromptSendsThePlannerToThePublishedTable(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "decompose")
	if err != nil {
		t.Fatalf("decompose: %v", err)
	}
	if !strings.Contains(p, "orion routes") {
		t.Errorf("the decompose prompt never names the published routing table:\n%s", p)
	}
	if !strings.Contains(p, "marker") {
		t.Errorf("the decompose prompt never tells the planner to set the marker:\n%s", p)
	}
	// The keywords themselves belong to internal/work, not here.
	for _, kw := range []string{"front-end", "documentation", "adr", "requirements"} {
		if strings.Contains(p, kw) {
			t.Errorf("the prompt carries its own copy of the vocabulary (%q); it must "+
				"point at `orion routes` instead, or the two drift", kw)
		}
	}
}
