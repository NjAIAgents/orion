package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// An idea that names a product by URL is stating a requirement by reference,
// and the reference is where the requirement actually lives.
//
// FOUND ON A REAL PROJECT. The idea was "something similar to <a CloudHealth
// URL>", given twice, and no stage was ever told to look at it -- so the one
// concrete statement of what the product had to do went unread while the
// stages designed from the sentence around it.
//
// Asserted on the prompt rather than on a fetch: whether the agent reaches
// the page depends on the network and on tool availability, but whether it
// was ASKED to is Orion's side of the contract and must not silently vanish.
func TestTheIntentPromptTreatsAURLAsSomethingToRead(t *testing.T) {
	w := ws(t, `{}`)
	p, err := stagePrompt(w, "intent", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"RESEARCH TARGET", // it is named as one
		"read it before writing",
		"Cite what you read",
		"open question", // an unreachable page is a gap, not licence to invent
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the intent prompt no longer says %q:\n%s", want, p)
		}
	}
}

// The research instruction must not loosen the rule it sits next to. An agent
// told to read a page and also told to record only what was said needs both
// halves, or it either ignores the URL or invents around it.
func TestResearchDoesNotLoosenTheNoInventionRule(t *testing.T) {
	w := ws(t, `{}`)
	p, err := stagePrompt(w, "intent", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "Do not write code or design a solution") {
		t.Error("the no-design rule was dropped when research was added")
	}
	if !strings.Contains(p, "NEVER AN ASSUMPTION") {
		t.Error("the open-question rule was dropped when research was added")
	}
}
