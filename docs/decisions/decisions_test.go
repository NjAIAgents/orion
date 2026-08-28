// Package decisions_test guards OR-161's premise: today's architectural
// decisions live in this repo, not only in Jira ticket descriptions. A test
// that only checks a doc "looks reasonable" would still pass if a file were
// deleted by an unrelated cleanup; this fails loudly instead.
package decisions_test

import (
	"os"
	"strings"
	"testing"
)

// One entry per decision recorded 2026-08-28 (OR-161). Adding a new ADR
// later does not require touching this list; removing one of these does.
var expectedDecisions = []string{
	"0001-precedence-rule-orion-owns-orchestration.md",
	"0002-superpowers-declined-as-dependency.md",
	"0003-ponytail-scoped-to-development.md",
	"0004-no-sqlite-file-based-storage.md",
	"0005-agent-roster-is-global.md",
	"0006-new-and-plan-are-sequential-phases.md",
	"0007-auto-effort-standing-preference.md",
	"0008-parallelism-level-ordering.md",
	"0009-canonical-slug-one-name.md",
}

func TestEveryDecisionHasContextDecisionConsequences(t *testing.T) {
	for _, name := range expectedDecisions {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body := string(b)
		for _, section := range []string{"## Context", "## Decision", "## Consequences"} {
			if !strings.Contains(body, section) {
				t.Errorf("%s: missing %q section", name, section)
			}
		}
	}
}

func TestPrecedenceRuleIsAlsoInClaudeMd(t *testing.T) {
	b, err := os.ReadFile("../../CLAUDE.md")
	if err != nil {
		t.Fatalf("CLAUDE.md: %v", err)
	}
	body := string(b)
	for _, phrase := range []string{"Orion owns orchestration", "never own"} {
		if !strings.Contains(body, phrase) {
			t.Errorf("CLAUDE.md: expected to state the precedence rule (missing %q)", phrase)
		}
	}
}
