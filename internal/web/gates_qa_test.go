package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The mockup's own four state labels, copied out by hand rather than through
// the GateBlocked/GateApproval/GateQuestion/GatePlan constants -- a test that
// compares a constant to itself would pass even if gates.go renamed every
// state and the mockup never moved.
var mockupDrawnStates = map[GateState]bool{
	"blocked":           true,
	"awaiting approval": true,
	"question":          true,
	"plan confirmation": true,
}

// OR-274 named five gates by kind. A gate the ticket asked for that never
// made it into the enumerated list is exactly the miss the board is supposed
// to prevent, so each of the five gets its own lookup rather than a loop that
// could silently pass on an empty slice.
func TestAllFiveOR274GatesAreEnumerated(t *testing.T) {
	for _, kind := range []string{
		"open-questions",
		"merge-approval",
		"breaker-trip",
		"budget-ack",
		"lessons-pending",
	} {
		gateByKind(t, kind)
	}
}

// Every mapped gate (State non-empty) must land on one of the mockup's four
// states, spelled exactly as the mockup spells them -- not a fifth state
// invented in gates.go that the design never drew.
func TestMappedGateStatesMatchMockupExactly(t *testing.T) {
	for _, g := range Gates {
		if g.State == "" {
			continue
		}
		if !mockupDrawnStates[g.State] {
			t.Errorf("%s: State %q is not one of the mockup's four drawn states", g.Kind, g.State)
		}
	}
}

// budget-ack and lessons-pending are the two OR-274 says the board cannot
// draw: no ticket to hang a row on. Each must carry State="" (nothing
// invented) and a non-empty Why (the gap is explained, not silent).
func TestAccountLevelGatesAreUnmappedWithExplanation(t *testing.T) {
	for _, kind := range []string{"budget-ack", "lessons-pending"} {
		g := gateByKind(t, kind)
		if g.State != "" {
			t.Errorf("%s: State = %q, want empty -- this gate has no ticket to be drawn on", kind, g.State)
		}
		if g.Why == "" {
			t.Errorf("%s: Why is empty -- an unmapped gate must say why it has no board row", kind)
		}
	}
}

// A gate cannot be simultaneously mapped to a board state and carrying a
// gap explanation: State and Why answer two different questions ("where is
// it drawn" vs "why is it not drawn") and a gate answering both at once
// means one of the two fields is stale.
func TestNoGateCarriesBothStateAndWhy(t *testing.T) {
	for _, g := range Gates {
		if g.State != "" && g.Why != "" {
			t.Errorf("%s: has both State %q and Why %q set", g.Kind, g.State, g.Why)
		}
	}
}

// Gates enumerates nine kinds; OR-274 only named five by hand. The other
// four -- plan-confirmation, agent-blocked, environment-hold,
// dirty-worktree -- were found beside the named ones and get no less
// scrutiny: each must still be mapped to a board state or carry a Why, same
// as the two the ticket called out by name. A silent omission on one of
// these would not be caught by a test that only walks the five named kinds.
func TestGatesBeyondTheFiveNamedAreAllMappedOrExplained(t *testing.T) {
	named := map[string]bool{
		"open-questions":  true,
		"merge-approval":  true,
		"breaker-trip":    true,
		"budget-ack":      true,
		"lessons-pending": true,
	}
	found := 0
	for _, g := range Gates {
		if named[g.Kind] {
			continue
		}
		found++
		mapped := g.State != ""
		explained := strings.TrimSpace(g.Why) != ""
		if mapped == explained {
			t.Errorf("%s: must be either mapped (State set) or explained (Why set), not both or neither -- got State=%q Why=%q", g.Kind, g.State, g.Why)
		}
	}
	if found == 0 {
		t.Fatal("no gate beyond the five OR-274 named -- this test would pass vacuously if the extra gates were ever removed")
	}
}

// gates.go's four GateState constants are meant to be the mockup's own
// vocabulary, spelled exactly. This reads the constants directly rather
// than going through a Gates entry, so a constant that drifts from the
// mockup fails here even if no gate in the list currently uses it.
func TestGateStateConstantsMatchMockupStringsExactly(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "docs/design/web/02-gate-board.html"))
	if err != nil {
		t.Fatalf("gate board mockup: %v", err)
	}
	mockup := string(b)

	constants := map[string]GateState{
		"GateBlocked":  GateBlocked,
		"GateApproval": GateApproval,
		"GateQuestion": GateQuestion,
		"GatePlan":     GatePlan,
	}
	for name, state := range constants {
		if !strings.Contains(mockup, string(state)) {
			t.Errorf("%s = %q does not appear anywhere in 02-gate-board.html", name, state)
		}
	}
}

// The two unmapped gates are unmapped for a structural reason, not because
// nobody got around to wiring them up: neither is keyed on a ticket, and
// each spans every project rather than one. A Why that doesn't say so is
// indistinguishable from a placeholder that would pass the mere
// non-empty-string check other tests already make.
func TestUnmappedGatesExplainTheStructuralReason(t *testing.T) {
	for _, kind := range []string{"budget-ack", "lessons-pending"} {
		g := gateByKind(t, kind)
		why := strings.ToLower(g.Why)
		if !strings.Contains(why, "ticket") {
			t.Errorf("%s: Why does not say this gate has no ticket to be drawn on: %q", kind, g.Why)
		}
		if !strings.Contains(why, "every project") && !strings.Contains(why, "across project") {
			t.Errorf("%s: Why does not say this gate spans every project rather than one: %q", kind, g.Why)
		}
	}
}
