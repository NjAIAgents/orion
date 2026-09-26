// QA for OR-266: covers two acceptance criteria decisions_test.go's keyword
// list does not check -- that 0024 states what each layer prevents as an
// actual table (not just as prose the keyword list can match piecemeal), and
// that the changelog documents the security change for a reader who never
// opens the ADR.
package decisions_test

import (
	"os"
	"strings"
	"testing"
)

const adr0024Path = "0024-local-surface-authentication.md"

// Keyword matching alone would pass on the four threat rows appearing
// anywhere in the file, in any order, joined to the wrong layer. The table
// is the acceptance criterion -- a reader comparing layers against threats
// needs the grid, not four separate sentences to reassemble by hand.
func TestADR0024HasThreatCoverageTable(t *testing.T) {
	b, err := os.ReadFile(adr0024Path)
	if err != nil {
		t.Fatalf("%s: %v", adr0024Path, err)
	}
	body := string(b)

	if !strings.Contains(body, "| Threat | Token | Origin / Sec-Fetch-Site | Host allowlist |") {
		t.Errorf("%s: expected a markdown table with columns Threat / Token / "+
			"Origin / Sec-Fetch-Site / Host allowlist", adr0024Path)
	}
	for _, row := range []string{
		"Another local process POSTs to a write endpoint",
		"Cross-origin form POST from a visited website (CSRF)",
		"DNS rebinding: attacker page becomes same-origin",
		"Operator pastes the token into a hostile page",
	} {
		if !strings.Contains(body, row) {
			t.Errorf("%s: expected the coverage table to have a row for %q", adr0024Path, row)
		}
	}
	// Every threat must have at least one layer that does not stop it -- that
	// is the whole argument for needing three. A table where one row read
	// "stops" in every column would silently make the other two layers optional.
	if !strings.Contains(body, "Each column has a row it cannot cover") {
		t.Errorf("%s: expected the table to be followed by the point that no "+
			"single layer covers every row", adr0024Path)
	}
}

// Same rationale as OR-237's equivalent test: a fragment is not permanent,
// `orion changelog` collates it into CHANGELOG.md and deletes it, so assert
// the facts are documented in whichever of the two currently holds them.
func TestChangelogFragmentOR266(t *testing.T) {
	path := "../../.changelog.d/OR-266.md"
	b, err := os.ReadFile(path)
	if err != nil {
		path = "../../CHANGELOG.md"
		if b, err = os.ReadFile(path); err != nil {
			t.Fatalf("OR-266 is documented in neither the fragment nor %s: %v", path, err)
		}
	}
	body := string(b)

	if strings.HasSuffix(path, "OR-266.md") &&
		!strings.HasPrefix(strings.TrimSpace(body), "### Security") {
		t.Errorf("%s: expected the fragment to open with a \"### Security\" section", path)
	}
	for _, fact := range []string{
		"X-Orion-Token",
		"default-deny",
		"Host allowlist",
		"docs/decisions/0024-local-surface-authentication.md",
	} {
		if !strings.Contains(body, fact) {
			t.Errorf("%s: expected to mention %q", path, fact)
		}
	}
}
