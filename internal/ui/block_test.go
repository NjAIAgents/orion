package ui

import (
	"strings"
	"testing"
)

// A block boundary is only useful if it is findable in a CONCURRENT log, which
// means both edges exist and both name the block.
func TestABlockIsBoundedAtBothEnds(t *testing.T) {
	setUTF8Locale(t)
	start, end := BlockStart("cost report OR-219"), BlockEnd("cost report OR-219")
	for _, s := range []string{start, end} {
		if !strings.Contains(s, "cost report OR-219") {
			t.Errorf("boundary %q does not name the block", s)
		}
	}
	if !strings.Contains(end, "end") {
		t.Errorf("the closing boundary %q does not say it is the end -- a reader "+
			"who cannot see the rule has nothing to go on", end)
	}
	if start == end {
		t.Error("the two boundaries are indistinguishable")
	}
}

// A box-drawing rule on a non-UTF-8 terminal is mojibake, so it degrades --
// and the WORDS survive, which is the OR-163 rule the stage axis is built on.
func TestTheBoundaryDegradesOnANonUTF8Locale(t *testing.T) {
	setASCIILocale(t)
	assertPlainBoundary(t, BlockStart("cost report OR-219"))
}

// NO_COLOR is set by people who need the plain form of everything, not only of
// the colours.
func TestTheBoundaryDegradesUnderNoColor(t *testing.T) {
	setUTF8Locale(t)
	t.Setenv("NO_COLOR", "1")
	assertPlainBoundary(t, BlockStart("cost report OR-219"))
}

func assertPlainBoundary(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, blockRuleGlyph) {
		t.Errorf("still rendered a box-drawing rule: %q", s)
	}
	if !strings.Contains(s, blockRuleASCII) {
		t.Errorf("the degraded boundary lost its rule entirely: %q", s)
	}
	if !strings.Contains(s, "cost report OR-219") {
		t.Errorf("the degraded boundary lost its words: %q", s)
	}
}

// Never coloured: a block like the cost report is rendered once and sent both
// to the terminal and to a tracker comment, where an escape code is line noise.
func TestTheBoundaryCarriesNoEscapeCodes(t *testing.T) {
	setUTF8Locale(t)
	t.Setenv("CLICOLOR_FORCE", "1")
	if s := BlockStart("cost report OR-219"); strings.Contains(s, "\x1b[") {
		t.Errorf("boundary carries an escape code: %q", s)
	}
}
