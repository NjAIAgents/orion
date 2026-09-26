package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is where the survey is checked against: gates.go cites files across
// the tree, and a citation nobody verifies is prose.
const repoRoot = "../.."

// The five OR-274 named. Written out rather than derived from Gates, because
// deriving it from the thing under test would pass on an empty list.
var namedByTheTicket = []string{
	"open-questions",
	"merge-approval",
	"breaker-trip",
	"budget-ack",
	"lessons-pending",
}

func gateByKind(t *testing.T, kind string) Gate {
	t.Helper()
	for _, g := range Gates {
		if g.Kind == kind {
			return g
		}
	}
	t.Fatalf("no gate of kind %q is enumerated", kind)
	return Gate{}
}

// The whole point of the ticket: a gate that exists and is not enumerated is
// invisible, and invisible is exactly the failure the board is against.
func TestEveryGateTheTicketNamedIsEnumerated(t *testing.T) {
	for _, kind := range namedByTheTicket {
		gateByKind(t, kind)
	}
}

// Mapped or explained, never neither. This is the clause that makes the gap
// list as load-bearing as the mapping: dropping a gate now costs a red test
// rather than an operator waiting on a board that says nothing is waiting.
func TestEveryGateIsEitherMappedOrExplained(t *testing.T) {
	for _, g := range Gates {
		switch {
		case g.State != "" && g.Why != "":
			t.Errorf("%s: mapped to %q AND carries a gap reason; it cannot be both", g.Kind, g.State)
		case g.State == "" && strings.TrimSpace(g.Why) == "":
			t.Errorf("%s: has no board state and no reason -- an unmapped gate must say why", g.Kind)
		}
		if strings.TrimSpace(g.Clears) == "" {
			t.Errorf("%s: does not say what a person does about it", g.Kind)
		}
	}
}

// The board states are the mockup's, not this file's invention. A fifth state
// is a design change and has to happen in the design first.
func TestBoardStatesAreTheOnesTheMockupDraws(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "docs/design/web/02-gate-board.html"))
	if err != nil {
		t.Fatalf("gate board mockup: %v", err)
	}
	mockup := string(b)

	known := map[GateState]bool{}
	for _, s := range []GateState{GateBlocked, GateApproval, GateQuestion, GatePlan} {
		if !strings.Contains(mockup, string(s)) {
			t.Errorf("board state %q is not drawn anywhere in 02-gate-board.html", s)
		}
		known[s] = true
	}

	for _, g := range Gates {
		if g.State != "" && !known[g.State] {
			t.Errorf("%s: board state %q is not one of the mockup's four", g.Kind, g.State)
		}
	}
}

// A survey is only worth the tree it was read out of. When a gate's
// implementation moves, this fails rather than leaving a citation pointing at
// nothing -- which is how a mapping quietly stops describing the code.
func TestEveryGateCitesAFileThatExists(t *testing.T) {
	for _, g := range Gates {
		if g.Source == "" {
			t.Errorf("%s: cites no source", g.Kind)
			continue
		}
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(g.Source))); err != nil {
			t.Errorf("%s: cites %s, which is not there: %v", g.Kind, g.Source, err)
		}
	}
}

func TestKindsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range Gates {
		if seen[g.Kind] {
			t.Errorf("%s: enumerated twice", g.Kind)
		}
		seen[g.Kind] = true
	}
}

// The two gaps are the finding, so they are pinned by name. A change that
// quietly maps budget-ack to a ticket row would be mapping an account-wide stop
// onto whichever ticket happened to be running, which is worse than the gap.
func TestTheTwoAccountLevelGatesAreRecordedAsGaps(t *testing.T) {
	for _, kind := range []string{"budget-ack", "lessons-pending"} {
		g := gateByKind(t, kind)
		if g.State != "" {
			t.Errorf("%s: mapped to %q, but it is not keyed on a ticket", kind, g.State)
		}
	}
}

// An unmapped gate with no reason is indistinguishable from an oversight --
// this is the half of TestEveryGateIsEitherMappedOrExplained that pins the
// empty-State side on its own, so a regression here reads as exactly what
// broke rather than as one of three possible clauses.
func TestEveryUnmappedGateExplainsWhy(t *testing.T) {
	for _, g := range Gates {
		if g.State == "" && strings.TrimSpace(g.Why) == "" {
			t.Errorf("%s: State is empty but Why does not say why", g.Kind)
		}
	}
}

// A gate mapped to a board state carries no gap reason -- Why is reserved for
// the gates the board cannot draw, and a mapped gate with one would read as a
// mapping the author was not sure of.
func TestEveryMappedGateHasNoWhy(t *testing.T) {
	for _, g := range Gates {
		if g.State != "" && strings.TrimSpace(g.Why) != "" {
			t.Errorf("%s: mapped to %q but still carries a Why of %q", g.Kind, g.State, g.Why)
		}
	}
}

// Every gate, mapped or not, has to tell the operator what clears it -- that
// is the answer to "what do I type" whether or not the board is up.
func TestEveryGateHasNonEmptyClears(t *testing.T) {
	for _, g := range Gates {
		if strings.TrimSpace(g.Clears) == "" {
			t.Errorf("%s: Clears is empty -- does not say what a person does about it", g.Kind)
		}
	}
}
