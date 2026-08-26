package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "intent.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMissingIntentIsNotReady(t *testing.T) {
	a := Assess(filepath.Join(t.TempDir(), "nope.md"))
	if a.Found {
		t.Error("a missing file must not report Found")
	}
	if a.Ready() {
		t.Error("no intent at all cannot be ready to design from")
	}
}

func TestCountsOnlyUnanswered(t *testing.T) {
	a := Assess(write(t, `# Intent
Something.

## Open questions
- [ ] Do loss adjusters need access?
- [x] Which auth do we use? Answer: the existing gateway JWT.
- ~~Should it paginate?~~ decided: yes
- Answer: rate limit is 50rps, confirmed with platform
- What is the retention period?
`))
	if !a.Found {
		t.Fatal("file should be found")
	}
	if a.Open != 2 {
		t.Errorf("Open = %d, want 2 (the unticked one and the bare question)", a.Open)
	}
	if a.Ready() {
		t.Error("open questions must block")
	}
}

// "None" is how people say there is nothing outstanding. Counting it as a
// question would block a chain whose intent is actually complete.
func TestNonePlaceholdersDoNotBlock(t *testing.T) {
	for _, body := range []string{
		"## Open questions\n- None\n",
		"## Open questions\n- n/a\n",
		"## Open questions\n- Nothing\n",
		"## Open questions\n",
	} {
		a := Assess(write(t, "# Intent\n\n"+body))
		if a.Open != 0 {
			t.Errorf("%q: Open = %d, want 0", body, a.Open)
		}
		if !a.Ready() {
			t.Errorf("%q should be ready", body)
		}
	}
}

// Without closing the section at the next heading, every bullet in the rest
// of the document counts as an open question.
func TestSectionEndsAtTheNextHeading(t *testing.T) {
	a := Assess(write(t, `# Intent

## Open questions
- Do adjusters need access?

## Constraints
- No new PII in the session
- Existing authentication only
- Must not change the public API
`))
	if a.Open != 1 {
		t.Errorf("Open = %d, want 1: bullets after the next heading are not questions", a.Open)
	}
}

func TestHeadingMatchingIsLenient(t *testing.T) {
	for _, h := range []string{"## Open questions", "### OPEN QUESTION", "# Open Questions"} {
		a := Assess(write(t, "# Intent\n\n"+h+"\n- something unresolved\n"))
		if a.Open != 1 {
			t.Errorf("heading %q: Open = %d, want 1", h, a.Open)
		}
	}
}

func TestNeedsDiscovery(t *testing.T) {
	tests := []struct {
		idea string
		want bool
	}{
		{"fix the typo in the footer", true},
		{"customers should see claim status in the portal", true},
		// Long and specific, with constraints and a rationale: the
		// conversation already happened somewhere else.
		{"Customers should see claim status, next step and expected date in the portal " +
			"so that they stop phoning the contact center. Must not introduce new PII " +
			"into the portal session and must use the existing authentication. Out of " +
			"scope: third-party loss adjusters.", false},
		{"add rate limiting to the status endpoint because the claims-core API " +
			"limits us to 50rps and we must not exceed it", false},
	}
	for _, tc := range tests {
		got, reason := NeedsDiscovery(tc.idea)
		if got != tc.want {
			t.Errorf("NeedsDiscovery(%.40q...) = %v (%s), want %v", tc.idea, got, reason, tc.want)
		}
		if reason == "" {
			t.Error("a decision must come with its reason")
		}
	}
}

// A block that makes you go and find the questions is a block people learn
// to route around.
func TestGateMessageNamesTheQuestionsAndTheWayOut(t *testing.T) {
	a := Assess(write(t, "# Intent\n\n## Open questions\n- Do adjusters need access?\n"))
	m := a.GateMessage("claim-status-abc123")

	if !strings.Contains(m, "Do adjusters need access?") {
		t.Error("the message must name the question, not just point at a file")
	}
	if !strings.Contains(m, "orion answer claim-status-abc123") {
		t.Error("the message must give the exact command to resolve it")
	}
	if !strings.Contains(m, "[x]") {
		t.Error("the message must say how to mark something answered")
	}
}
