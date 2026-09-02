package ui

import (
	"strings"
	"testing"
)

// The console is process-global and sameWriter compares by POINTER, so a
// writer allocated where a finished run's writer used to live compares equal
// to the stale console.w. Rule 3 then reads the new run's first line as a
// repeat of the old run's last one and counts it instead of printing it --
// the text never reaches the buffer at all.
//
// That is what made TestAnEnvironmentalFaultHoldsTheQueueRatherThanDrainingIt
// fail about one run in three on the macOS runner and pass every time
// locally: it depends on the allocator reusing an address, not on anything
// the watcher does (OR-262).
func TestConsoleResetLetsANewRunPrintItsFirstLine(t *testing.T) {
	const msg = "claude is not authenticated; sign in"

	b := new(strings.Builder)
	Say(b, "OR-1", "orion", "fail", "%s", msg)
	if !strings.Contains(b.String(), msg) {
		t.Fatalf("the first run never printed its line: %q", b.String())
	}

	// A second run whose writer lands on the same address as the first.
	b.Reset()
	ConsoleReset()
	Say(b, "OR-1", "orion", "fail", "%s", msg)
	if !strings.Contains(b.String(), msg) {
		t.Errorf("the line was swallowed as a repeat of a run that had ended: %q", b.String())
	}
}

// Guards the reason ConsoleReset is separate from Reset: Reset is a boundary
// WITHIN a run and must keep collapsing genuine repeats, or every duplicated
// line in a long watch would print in full.
func TestRepeatsStillCollapseWithinOneRun(t *testing.T) {
	ConsoleReset()
	b := new(strings.Builder)
	Say(b, "OR-1", "orion", "fail", "the same thing again")
	Say(b, "OR-1", "orion", "fail", "the same thing again")
	Flush(b)
	if n := strings.Count(b.String(), "the same thing again"); n != 2 {
		t.Fatalf("want the line plus its count, got %d occurrence(s): %q", n, b.String())
	}
	if !strings.Contains(b.String(), "(x2)") {
		t.Errorf("the run of identical lines lost its count: %q", b.String())
	}
}
