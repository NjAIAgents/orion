package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// A re-run of the intent stage must extend the capture already on disk
// rather than start a second artifact beside it -- internal/discovery and
// advise.Artifacts both scope to the one file at docs/intent/<slug>.md, so a
// second file is invisible to both. The prompt is the only thing that can
// say this, since stagePrompt builds the same text whether or not a capture
// already exists on disk; the instruction has to hold unconditionally.
func TestIntentPromptExtendsExistingCaptureRatherThanOverwriting(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent", config.Toolkit{})
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	if !strings.Contains(p, "EXTEND THE FILE THAT IS ALREADY THERE") {
		t.Errorf("the intent prompt does not tell a re-run to extend an existing capture:\n%s", p)
	}
	if !strings.Contains(p, "do not start a second") {
		t.Errorf("the intent prompt does not forbid starting a second artifact beside the existing capture:\n%s", p)
	}
}

// The path is part of /capture-intent's contract (docs/intent/<slug>.md),
// and the intent prompt must point the agent at that skill and that path
// rather than let it invent a location -- discovery.Assess and the PM role
// in advise.Artifacts both read from exactly there.
func TestIntentPromptNamesTheCapturePathAndLocation(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "intent", config.Toolkit{})
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	if !strings.Contains(p, "/capture-intent") {
		t.Errorf("the intent prompt does not point the agent at the /capture-intent skill:\n%s", p)
	}
	if !strings.Contains(p, "docs/intent/<slug>.md") {
		t.Errorf("the intent prompt does not name docs/intent/<slug>.md as the capture's path:\n%s", p)
	}
	if !strings.Contains(p, "do not") || !strings.Contains(p, "relocate the file") {
		t.Errorf("the intent prompt does not forbid relocating the capture away from its contract path:\n%s", p)
	}
}

// Nothing in internal/discovery reads or requires a success-measures
// section -- Assess only ever looks for "Open questions". The prompt is the
// only enforcement there is, so a capture missing the heading the prompt
// shows fails the prompt's own contract even though the gate itself would
// wave it through unremarked.
func TestIntentCaptureMissingSuccessMeasuresFailsThePromptsOwnShape(t *testing.T) {
	if !strings.Contains(intentShape, "## Success measures") {
		t.Fatalf("the shape the prompt requires has no success-measures heading:\n%s", intentShape)
	}

	captureWithoutSuccessMeasures := "# Intent\n\n## Open questions\n" + intentNone
	if strings.Contains(captureWithoutSuccessMeasures, "## Success measures") {
		t.Fatalf("test capture unexpectedly contains the heading it is meant to omit")
	}

	// The prompt's own instruction says this capture is incomplete: it does
	// not carry the shape shown to the agent.
	if strings.Contains(captureWithoutSuccessMeasures, quote(intentShape)) {
		t.Errorf("a capture missing success measures should not match the prompt's required shape")
	}

	// But the gate that actually blocks later stages does not know or care:
	// it is Ready, because Assess never looks for this heading.
	a := assessIntent(t, captureWithoutSuccessMeasures)
	if !a.Ready() {
		t.Errorf("discovery has no parser for success measures, so their absence must not "+
			"block the gate (Open = %d); only the prompt can require them", a.Open)
	}
}

// The flip side of the case above: a capture that DOES carry a success
// measures section -- even one with wrong or empty content -- passes the
// gate exactly the same as one without, because Assess only ever inspects
// the Open questions section. Correctness of success measures is entirely
// unenforced by code.
func TestSuccessMeasuresPresenceDoesNotAffectTheDiscoveryGate(t *testing.T) {
	withGoodMeasures := "# Intent\n\n## Success measures\n" +
		"- Median response time drops below 200ms, checked in the dashboard.\n\n" +
		"## Open questions\n" + intentNone
	a := assessIntent(t, withGoodMeasures)
	if !a.Ready() {
		t.Errorf("a well-formed success measures section should not itself affect readiness (Open = %d)", a.Open)
	}

	withEmptyMeasures := "# Intent\n\n## Success measures\n\n## Open questions\n" + intentNone
	b := assessIntent(t, withEmptyMeasures)
	if !b.Ready() {
		t.Errorf("an empty success measures section is not something Assess parses at all, "+
			"so it must not block the gate either (Open = %d)", b.Open)
	}
}
