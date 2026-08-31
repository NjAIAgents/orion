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
	"0010-routing-vocabulary-is-a-published-contract.md",
	// OR-206. Load-bearing in the same way 0001 is: it decides who sequences
	// landing, so a later "why not just turn on GitHub's merge queue?" has
	// somewhere to land other than a re-litigation.
	"0011-orion-owns-the-landing-queue.md",
	// OR-149. Settles the question 0009 left open, and it is load-bearing in
	// the same way: without it, "just let a re-run reuse the workspace" is a
	// one-line change nobody would think to argue with.
	"0012-one-workspace-per-tracker-project.md",
	// OR-148. Settles the open question that ticket carried, and it is
	// load-bearing: without it, "why not have `orion new` set the workspace up
	// too, it is right there?" is a change that reads as a convenience and
	// quietly reintroduces the two-workspaces-per-project state 0012 refuses.
	"0013-new-creates-the-tracker-project-not-a-workspace.md",
	// OR-213. Load-bearing: without it, "just point CLAUDE_CONFIG_DIR at the
	// operator's own directory, the skills are already there" is a one-line
	// simplification that reads as tidying up and silently hands every run a
	// write handle to the tracker again.
	"0014-supervised-runs-get-a-curated-config-directory.md",
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
			// The amendment is the substance now: the gate FIRED, and a file
			// that still reads as "waiting for evidence" would send the next
			// reader looking for evidence that has already arrived (OR-206).
			"0008-parallelism-level-ordering.md",
			[]string{"OR-143", "OR-144", "OR-145", "auto-rebase", "OR-206", "landing queue"},
		},
		{
			// Which option was taken and which were declined is the whole
			// point of OR-206's record. A file that kept the sections but
			// lost "why not GitHub's merge queue" would invite the question
			// again on the next incident.
			"0011-orion-owns-the-landing-queue.md",
			[]string{"OR-206", "OR-202", "merge queue", "maxAutoRebases", "require_up_to_date"},
		},
		{
			"0009-canonical-slug-one-name.md",
			[]string{"OR-149"},
		},
		{
			// WHICH way the open question went, and the reason it was not a
			// free choice, is the whole substance of OR-148's record. A file
			// that kept the sections but lost the 0012 argument would leave the
			// next reader thinking either answer is still available.
			"0013-new-creates-the-tracker-project-not-a-workspace.md",
			[]string{"OR-148", "OR-149", "0012", "no workspace", "CreateProject"},
		},
		{
			// Which actors are routable, and why the rest are not, is the
			// substance of OR-191 -- a file that kept the sections but lost
			// the roster decision would document nothing.
			"0010-routing-vocabulary-is-a-published-contract.md",
			[]string{"OR-171", "OR-176", "OR-191", "orion routes", "docsite-infra"},
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
