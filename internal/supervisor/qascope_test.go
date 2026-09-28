package supervisor

import (
	"strings"
	"testing"
)

// OR-565: every prompt that writes a test carries the scope and fixture
// rules -- the QA run, each fan-out author, and the developer fixing QA's
// findings, which is where LTA-141's "AKIA..." fixture literal came from.
func TestEveryTestWritingPromptCarriesScopeAndFixtureRules(t *testing.T) {
	prompts := map[string]string{
		"qa":       QAPrompt("LTA-141", "doc", "criteria", "", QATools{}, ""),
		"author":   QAAuthorPrompt("LTA-141", "doc", "case one"),
		"findings": QAFindingsMessage("case one: expected x"),
	}
	for name, p := range prompts {
		for _, want := range []string{"Test only what THIS ticket changes",
			"name the ticket in", "looks like a credential", `"AKIA" + "X" * 16`} {
			if !strings.Contains(p, want) {
				t.Errorf("the %s prompt lacks %q", name, want)
			}
		}
		// A real-looking key in the prompt itself would teach the pattern.
		if strings.Contains(p, "AKIAABCD") {
			t.Errorf("the %s prompt carries a key-shaped literal", name)
		}
	}
	if strings.Contains(QAPrompt("K", "s", "d", "", QATools{}, ""), "16.\nWHAT YOU MAY") {
		t.Error("the scope block runs into the next heading with no blank line")
	}
}
