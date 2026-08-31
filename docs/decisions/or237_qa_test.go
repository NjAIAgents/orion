// QA for OR-237: covers acceptance criteria the implementer's own
// decisions_test.go left unchecked -- status/date metadata, the specific
// Consequences facts a later editor could quietly drop, the changelog
// fragment's content, and that the cross-references to 0001/0011 name real
// files rather than dangling links.
package decisions_test

import (
	"os"
	"strings"
	"testing"
)

const adr0015Path = "0015-ci-authority-under-a-merge-ref.md"

// A status/date typo is invisible to TestEveryDecisionHasContextDecisionConsequences,
// which only checks the section headers exist -- it would still pass if this
// ADR were left "Proposed" or dated wrong.
func TestADR0015StatusAndDate(t *testing.T) {
	b, err := os.ReadFile(adr0015Path)
	if err != nil {
		t.Fatalf("%s: %v", adr0015Path, err)
	}
	body := string(b)
	if !strings.Contains(body, "Status: Accepted") {
		t.Errorf("%s: expected \"Status: Accepted\"", adr0015Path)
	}
	if !strings.Contains(body, "Date: 2026-08-30") {
		t.Errorf("%s: expected \"Date: 2026-08-30\"", adr0015Path)
	}
}

// The existing TestDecisionContentMatchesTicket keyword list proves the
// empty-rollup and require_checks guards are named, but the ticket also asks
// the ADR to state three Consequences facts that a later trim could drop
// without breaking any existing assertion: the reversibility direction, the
// missing-attribution-within-a-batch cost, and the orion-protect hazard.
// None of those three phrases overlap with the existing keyword list.
func TestADR0015ConsequencesFacts(t *testing.T) {
	b, err := os.ReadFile(adr0015Path)
	if err != nil {
		t.Fatalf("%s: %v", adr0015Path, err)
	}
	body := string(b)
	for _, fact := range []string{
		"Reversible in one direction only",
		"does not say which branch broke it",
		"must not require a check that only the merge ref reports",
		"needs a response path",
	} {
		if !strings.Contains(body, fact) {
			t.Errorf("%s: expected to state %q", adr0015Path, fact)
		}
	}
}

// The merge-ref-as-PR option is declined explicitly, but the reason -- that
// taking it back gives back the parallelism saving OR-236 exists to
// achieve -- is a separate sentence from the "Not adopted" label and could be
// deleted while the label survives.
func TestADR0015DeclinesMergeRefAsPRForNamedReason(t *testing.T) {
	b, err := os.ReadFile(adr0015Path)
	if err != nil {
		t.Fatalf("%s: %v", adr0015Path, err)
	}
	body := string(b)
	if !strings.Contains(body, "Not adopted") {
		t.Errorf("%s: expected the declined merge-ref-as-PR option to be labeled \"Not adopted\"", adr0015Path)
	}
	if !strings.Contains(body, "gives back the saving") && !strings.Contains(body, "gives back the savings") {
		t.Errorf("%s: expected the decline reason to name the OR-236 parallelism saving it would give back", adr0015Path)
	}
}

// The cross-references are only useful if they point at files that actually
// exist under the names 0015 claims. A rename or deletion of 0001/0011 would
// leave the prose reference intact while the link rotted.
func TestADR0015CrossReferencesResolve(t *testing.T) {
	b, err := os.ReadFile(adr0015Path)
	if err != nil {
		t.Fatalf("%s: %v", adr0015Path, err)
	}
	body := string(b)
	for _, target := range []string{
		"0001-precedence-rule-orion-owns-orchestration.md",
		"0011-orion-owns-the-landing-queue.md",
	} {
		if !strings.Contains(body, target) {
			t.Errorf("%s: expected a link to %s", adr0015Path, target)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("%s: cross-referenced file %s does not exist: %v", adr0015Path, target, err)
		}
	}
}

// The changelog fragment is a separate artifact from the ADR with its own
// acceptance criteria; nothing in decisions_test.go reads it at all.
//
// It is read from EITHER place, because a fragment is not permanent: `orion
// changelog` collates fragments into CHANGELOG.md and deletes them, which is
// the whole point of the .changelog.d/ design. Asserting only on the fragment
// made this test fail on develop the first time the release it documents was
// cut -- a test that breaks on every release by construction. What is worth
// asserting is that the facts are documented SOMEWHERE a reader will find
// them, so look in the fragment first and in the collated changelog after.
func TestChangelogFragmentOR237(t *testing.T) {
	path := "../../.changelog.d/OR-237.md"
	b, err := os.ReadFile(path)
	if err != nil {
		path = "../../CHANGELOG.md"
		if b, err = os.ReadFile(path); err != nil {
			t.Fatalf("OR-237 is documented in neither the fragment nor %s: %v", path, err)
		}
	}
	body := string(b)

	// The "### Added" heading only means something in the fragment; once
	// collated it sits under the version's own section among others.
	if strings.HasSuffix(path, "OR-237.md") &&
		!strings.HasPrefix(strings.TrimSpace(body), "### Added") {
		t.Errorf("%s: expected the fragment to open with an \"### Added\" section", path)
	}
	for _, fact := range []string{
		"docs/decisions/0015",
		"OR-236",
		"OR-237",
		"post-merge check",
		"workflow trigger",
		"branch protection",
	} {
		if !strings.Contains(body, fact) {
			t.Errorf("%s: expected to mention %q", path, fact)
		}
	}
}
