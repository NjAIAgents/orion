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

// README.md is the discoverability path ("read before re-proposing something
// that looks like a gap"). A file present on disk but missing from the index
// is exactly the kind of decision nobody can find.
func TestReadmeIndexesEveryDecision(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("README.md: %v", err)
	}
	body := string(b)
	for _, name := range expectedDecisions {
		if !strings.Contains(body, name) {
			t.Errorf("README.md: does not link %s", name)
		}
	}
}

// Structural section headers alone would still pass if a decision's content
// were vague or facts wrong. These checks pin the specific facts the ticket
// asked for, so a regression to the substance fails loudly rather than only
// a regression to file shape.
func TestDecisionContentMatchesTicket(t *testing.T) {
	cases := []struct {
		file     string
		mustHave []string
	}{
		{
			// OR-138's file-locking rationale must be folded into the storage ADR.
			"0004-no-sqlite-file-based-storage.md",
			[]string{"OR-138", "procsafe", "MkdirAll", "flock", "Windows"},
		},
		{
			// Three of five superpowers ideas adopted natively, named explicitly.
			"0002-superpowers-declined-as-dependency.md",
			[]string{"OR-156", "OR-157", "OR-158", "execute-plan"},
		},
		{
			"0003-ponytail-scoped-to-development.md",
			[]string{"OR-160"},
		},
		{
			"0005-agent-roster-is-global.md",
			[]string{"OR-132"},
		},
		{
			"0007-auto-effort-standing-preference.md",
			[]string{"OR-134", "prompt-cache"},
		},
		{
			"0008-parallelism-level-ordering.md",
			[]string{"OR-143", "OR-144", "OR-145", "auto-rebase"},
		},
		{
			"0009-canonical-slug-one-name.md",
			[]string{"OR-149"},
		},
	}

	for _, c := range cases {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		body := string(b)
		for _, fact := range c.mustHave {
			if !strings.Contains(body, fact) {
				t.Errorf("%s: expected to mention %q", c.file, fact)
			}
		}
	}
}
