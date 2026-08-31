package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The child is told what it owns and that it owns nothing else. Orion's
// validator has already proved no two assigned packages import one another;
// that guarantee is worth nothing if a child edits outside its own package.
func TestTheFanChildIsToldWhatItOwnsAndWhatItCannotDo(t *testing.T) {
	p := FanChildPrompt("./internal/config", "add the Limits field")
	for _, want := range []string{
		"./internal/config",
		"add the Limits field",
		"nowhere else",
		"NO SHELL",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the child prompt does not say %q:\n%s", want, p)
		}
	}
	// The reason, not only the rule. An agent that finds it cannot run the
	// suite and is not told why concludes the environment is broken and
	// spends its turns working around it.
	if !strings.Contains(p, "your peers") {
		t.Error("the child is told it cannot test but not why")
	}
	if !strings.Contains(p, "parent run builds and tests ONCE") {
		t.Error("the child is not told who does verify")
	}
}

// Conditional on there being Go here, the same discipline testEnv's lines
// follow: naming a Go-only mechanism in a repository with no Go in it teaches
// the agent to distrust the instruction and go exploring, which costs more
// than saying nothing.
func TestTheFanOfferAppearsOnlyForAGoRepository(t *testing.T) {
	dir := t.TempDir()
	if got := fanOffer(dir); got != "" {
		t.Errorf("offered fan-out in a repository with no go.mod:\n%s", got)
	}
	if got := fanOffer(""); got != "" {
		t.Errorf("offered fan-out with no repository at all:\n%s", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := fanOffer(dir)
	for _, want := range []string{
		"orion fan",
		"assignments",
		"import edge",
		"go list",
		"serially",
		"two rounds",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the fan offer does not mention %q:\n%s", want, got)
		}
	}
	// The implementer must not read the refusal as an opening bid. An agent
	// that retries a rejected split with a better argument pays for another
	// round of validation and gets the same answer.
	if !strings.Contains(got, "not a\nnegotiation") && !strings.Contains(got, "negotiation") {
		t.Error("the offer does not say a refusal is final")
	}
}

// The offer reaches the implementer, not only the helper that writes it.
func TestTheTicketPromptCarriesTheFanOfferInAGoRepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := TicketPrompt("OR-230", "fan out", "do the thing", "http://x", dir, nil)
	if !strings.Contains(p, "orion fan") {
		t.Error("the implementer is never told the fan-out exists")
	}
}
