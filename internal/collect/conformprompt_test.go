package collect

// Both plan sources go into the prompt, each headed by its own file path
// (OR-158): the task's own plan artifact and this ticket's confirmed
// recommendation are different documents an implementer could have diverged
// from, and a finding naming neither is one nobody can re-check. Truncation
// coverage for a single source lives in conform_truncate_test.go; this file
// is about there being two sources and both keeping their own identity.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/conform"
)

func TestPromptCarriesBothPlanSourcesUnderTheirOwnPaths(t *testing.T) {
	ev := conform.Evidence{
		Key: "OR-158",
		Plan: []conform.Source{
			{Path: "plans/ledger.plan.md", Text: "index the ledger by issuer"},
			{Path: "docs/recommendations/confirmed/OR-158.md", Text: "one index per issuer"},
		},
		Diff: conform.Diff{Stat: "1 file changed", Patch: "diff --git a/ledger.go b/ledger.go"},
	}

	prompt := supervisorConformPrompt(ev)

	for _, want := range []string{
		"plans/ledger.plan.md", "index the ledger by issuer",
		"docs/recommendations/confirmed/OR-158.md", "one index per issuer",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt is missing %q, so a divergence naming this source "+
				"could not be re-checked:\n%s", want, prompt)
		}
	}
	// Each source is headed by its OWN path, not a shared or generic label --
	// otherwise a finding could name a document without saying which one.
	planAt := strings.Index(prompt, "plans/ledger.plan.md")
	confirmedAt := strings.Index(prompt, "docs/recommendations/confirmed/OR-158.md")
	if planAt < 0 || confirmedAt < 0 || planAt == confirmedAt {
		t.Errorf("the two plan sources do not each carry their own file path in the prompt:\n%s", prompt)
	}
}
