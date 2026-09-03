package ui

import (
	"strings"
	"testing"
)

// A block taller than the terminal is trimmed from the top, so the rows the
// erase can reach are the rows on screen. Without this the first rows scroll
// into history and are painted again on every redraw.
func TestABlockTallerThanTheTerminalIsTrimmedFromTheTop(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "row")
	}
	got := fitRows(lines, 12, 80)
	if len(got) != 11 {
		t.Fatalf("12-row terminal keeps 11 rows for the block and 1 for the cursor; got %d", len(got))
	}
}

func TestAWrappedLineCountsAsTheRowsItOccupies(t *testing.T) {
	long := strings.Repeat("x", 100) // two rows at 80 columns
	got := fitRows([]string{"a", "b", long, "c"}, 4, 80)
	// budget 3: "c" (1) + long (2) = 3; "b" would make 4.
	if len(got) != 2 || got[0] != long {
		t.Errorf("expected [long c], got %q", got)
	}
}

func TestAnUnknownHeightKeepsEverything(t *testing.T) {
	lines := []string{"a", "b", "c"}
	if got := fitRows(lines, 0, 80); len(got) != 3 {
		t.Errorf("unknown height must not trim; got %q", got)
	}
}
