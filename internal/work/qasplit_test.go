package work

import (
	"strings"
	"testing"
)

// TestEveryCaseSurvivesTheSplit is the one that matters. A fan that loses a
// case writes fewer tests than the serial path it replaced, which is worse
// than being slow -- and it would do it silently, because nothing downstream
// counts the cases back.
func TestEveryCaseSurvivesTheSplit(t *testing.T) {
	cases := "Authentication:\n- rejects an expired token\n- rejects a missing token\n" +
		"Rate limiting:\n- returns 429 past the limit\n- resets after the window\n" +
		"- allows a burst under the limit"

	for _, n := range []int{2, 3, 4, 5, 9} {
		groups := caseGroups(cases, n)
		total := 0
		for _, g := range groups {
			total += countCases(g)
		}
		if want := countCases(cases); total != want {
			t.Errorf("n=%d: %d cases across the groups, want %d", n, total, want)
		}
	}
}

// TestAGroupCarriesTheHeadingItsCasesSatUnder: a group handed
// "- resets after the window" with no heading has lost what the case is
// about, and the subagent writing it has to guess.
func TestAGroupCarriesTheHeadingItsCasesSatUnder(t *testing.T) {
	cases := "Authentication:\n- rejects an expired token\n" +
		"Rate limiting:\n- returns 429 past the limit"

	groups := caseGroups(cases, 2)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	for _, g := range groups {
		if !strings.Contains(g, ":") {
			t.Errorf("group has no heading, so its cases lost their context:\n%s", g)
		}
	}
}

// TestNoGroupIsMoreThanOneCaseLargerThanAnother. A fan finishes when its
// slowest child finishes, so a split that hands one child the remainder has
// kept the serial cost it was meant to remove.
func TestNoGroupIsMoreThanOneCaseLargerThanAnother(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("- case\n")
	}

	groups := caseGroups(b.String(), 5)
	if len(groups) != 5 {
		t.Fatalf("expected 5 groups, got %d", len(groups))
	}
	lo, hi := countCases(groups[0]), countCases(groups[0])
	for _, g := range groups[1:] {
		if n := countCases(g); n < lo {
			lo = n
		} else if n > hi {
			hi = n
		}
	}
	if hi-lo > 1 {
		t.Errorf("groups range from %d to %d cases; the split is uneven", lo, hi)
	}
}

// TestTooLittleToSplitTakesTheSerialPath. nil is the caller's signal to run
// one agent exactly as before, which is the fallback the fan must always
// have: an optimisation that cannot run must cost wall time, never coverage.
func TestTooLittleToSplitTakesTheSerialPath(t *testing.T) {
	for name, in := range map[string]struct {
		cases string
		n     int
	}{
		"no cases at all":  {"", 5},
		"only a heading":   {"Authentication:", 5},
		"a single case":    {"- rejects an expired token", 5},
		"a width of one":   {"- one\n- two\n- three", 1},
		"a width of zero":  {"- one\n- two\n- three", 0},
		"a negative width": {"- one\n- two\n- three", -3},
	} {
		if got := caseGroups(in.cases, in.n); got != nil {
			t.Errorf("%s: expected the serial path (nil), got %d group(s)", name, len(got))
		}
	}
}

// TestTheSplitNeverMakesAnEmptyGroup. An empty group is a dispatched agent
// with nothing to do: a model call spent on nothing, and a row in the display
// that never explains itself.
func TestTheSplitNeverMakesAnEmptyGroup(t *testing.T) {
	// Fewer cases than the requested width is the case that would produce
	// them, so ask for far more groups than there are cases.
	groups := caseGroups("- one\n- two\n- three", 10)
	if len(groups) != 3 {
		t.Fatalf("expected one group per case, got %d", len(groups))
	}
	for i, g := range groups {
		if countCases(g) == 0 {
			t.Errorf("group %d is empty", i)
		}
	}
}
