package ui

// The pinned region's geometry.
//
// This file was the frozen window's acceptance suite (OR-248). The window is
// gone -- every ticket's latest line now rides on its own row (OR-265) -- so
// what remains here is the arithmetic that outlived it: the region cannot be
// displaced by output, a resize reflows without stranding rows, a line's
// width is its cells rather than its bytes, and off a terminal nothing is
// capped at all.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

// lastFrame is what is on screen: everything after the final erase.
func lastFrame(s string) string {
	if i := strings.LastIndex(s, "\x1b[0J"); i >= 0 {
		return s[i+len("\x1b[0J"):]
	}
	return s
}

// redraw is what the timer does, from a test that does not want to wait 250ms.
func redraw(l *Live) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.eraseLocked()
	l.drawLocked()
}

// The region and its status line cannot be displaced by output volume. This is
// the failure the ticket names: a talkative tick used to push everything the
// operator was reading off the top, region included.
func TestOutputVolumeCannotDisplaceTheRegion(t *testing.T) {
	t.Setenv("LINES", "24")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	for i := 0; i < 200; i++ {
		if _, err := fmt.Fprintf(l, "chatter-%03d\n", i); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}

	frame := lastFrame(b.String())
	for _, want := range []string{"OR-237", "1 running", liveRuleGlyph} {
		if !strings.Contains(frame, want) {
			t.Errorf("200 lines of chatter displaced %q from the screen:\n%q", want, frame)
		}
	}
	// The whole block still fits the terminal, with the cursor's own row spare.
	// A block taller than the screen is a region that has scrolled off it.
	if l.drawn > 23 {
		t.Errorf("the block is %d rows on a 24-row terminal", l.drawn)
	}
}

// Resizing reflows the window without corrupting the region. The erase is a
// relative cursor move, so a wrapped line counted as one row strands a row on
// screen at every redraw and walks the region down the terminal.
func TestResizingReflowsWithoutStrandingRows(t *testing.T) {
	t.Setenv("LINES", "")
	t.Setenv("COLUMNS", "40")
	LiveReset()
	t.Cleanup(LiveReset)

	// No runs, so the region is empty and the arithmetic under test is the
	// window's alone.
	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	if _, err := fmt.Fprintf(l, "%s\n", strings.Repeat("x", 100)); err != nil {
		t.Fatalf("writing: %v", err)
	}
	// The written line goes to the scrollback, not the region, so what is
	// DRAWN is the region alone: empty here, since no run is registered.
	if l.drawn != 0 {
		t.Errorf("an empty region drew %d rows", l.drawn)
	}
	// Register a run so the region has something to measure.
	LiveStart("OR-237")
	b.Reset()
	redraw(l)
	wide := l.drawn
	if wide == 0 {
		t.Fatal("a region with a run drew nothing")
	}

	// Widen the terminal. The next redraw must erase exactly what the last
	// one drew, measured at the OLD width: an erase that counts a wrapped
	// line as one row strands a row on screen and walks the region down the
	// terminal at every redraw.
	t.Setenv("COLUMNS", "120")
	b.Reset()
	redraw(l)
	if got := b.String(); !strings.HasPrefix(got, fmt.Sprintf("\x1b[%dA\x1b[0J", wide)) {
		t.Errorf("the erase must undo the %d rows actually drawn:\n%q", wide, got)
	}
}

// The width a line occupies is its CELLS, not its bytes or its runes: paint()
// wraps fields in escapes that occupy no columns, and charging a coloured line
// for them makes the window think it wrapped when it did not.
func TestDisplayCellsSkipsEscapes(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"OR-237", 6},
		{"\x1b[36mOR-237\x1b[0m", 6},
		{"\x1b[1;36mOR\x1b[0m·\x1b[2m237\x1b[0m", 6},
		{"", 0},
		{"\x1b[0J", 0},
	} {
		if got := displayCells(c.in); got != c.want {
			t.Errorf("displayCells(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// Off a TTY nothing changes: the log stays a complete, greppable, uncapped
// record. A capped file would be a file with holes in it, and the holes would
// be exactly the lines somebody redirected the output to keep.
func TestOffATerminalNothingIsCapped(t *testing.T) {
	t.Setenv("LINES", "24")
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	l := &Live{w: &b, cursor: false}
	for i := 0; i < 40; i++ {
		if _, err := fmt.Fprintf(l, "chatter-%02d\n", i); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}

	got := b.String()
	for i := 0; i < 40; i++ {
		if want := fmt.Sprintf("chatter-%02d\n", i); strings.Count(got, want) != 1 {
			t.Errorf("off a terminal every line must appear exactly once; %q did not:\n%q", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Error("a non-terminal destination must get no cursor control")
	}
}

// The full log remains reachable on demand: a keystroke drops the cap, and
// from there the log prints in full into the terminal's own scrollback.
func TestAKeystrokeDropsTheCap(t *testing.T) {
	t.Setenv("LINES", "")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	for i := 0; i < 20; i++ {
		if _, err := fmt.Fprintf(l, "before-%02d\n", i); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}

	// ctrl-r, then enter: line-buffered, because raw mode costs an ioctl per
	// platform and Orion ships six of them.
	l.watchInput(strings.NewReader("\x12\n"))
	if !l.full {
		t.Fatal("a keystroke must drop the cap")
	}
	if l.window != nil {
		t.Errorf("the window must be committed and forgotten, not held: %q", l.window)
	}

	b.Reset()
	for i := 0; i < 20; i++ {
		if _, err := fmt.Fprintf(l, "after-%02d\n", i); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}
	got := b.String()
	for i := 0; i < 20; i++ {
		if want := fmt.Sprintf("after-%02d\n", i); strings.Count(got, want) != 1 {
			t.Errorf("past the cap every line prints once and stays; %q did not:\n%q", want, got)
		}
	}
	// And the region is still pinned under it: dropping the cap unbounds the
	// chatter, it does not turn the display off.
	if !strings.Contains(lastFrame(got), "OR-237") {
		t.Errorf("the region must survive the cap being dropped:\n%q", got)
	}
}

// Once the cap is dropped it cannot be re-applied: a second keystroke, or a
// second call to Full, must be a no-op rather than re-arming the window with
// whatever has accumulated since -- that would leave the operator's screen
// mid-log with no way to tell which lines the window had eaten.
func TestCannotRecapOnceDropped(t *testing.T) {
	t.Setenv("LINES", "")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	if _, err := fmt.Fprintln(l, "before"); err != nil {
		t.Fatalf("writing: %v", err)
	}
	l.Full()
	if !l.full {
		t.Fatal("Full must drop the cap")
	}

	b.Reset()
	l.Full() // second call: must not erase or redraw a second time
	if got := b.String(); got != "" {
		t.Errorf("a second Full must be a no-op, wrote:\n%q", got)
	}
	if !l.full {
		t.Error("the cap must stay dropped")
	}
}

// terminalRows() only trusts LINES when it parses to at least 10; anything
// else -- unset, non-numeric, negative, zero, or too small to be a real
// terminal -- must report "unknown" so the window falls back to its floor
// rather than guessing a height that could push the region off screen.
func TestTerminalRowsRejectsInvalidValues(t *testing.T) {
	for _, v := range []string{"", "not-a-number", "-5", "0", "9"} {
		t.Setenv("LINES", v)
		if got := terminalRows(); got != 0 {
			t.Errorf("LINES=%q: terminalRows() = %d, want 0 (unknown)", v, got)
		}
	}
	t.Setenv("LINES", "10")
	if got := terminalRows(); got != 10 {
		t.Errorf("LINES=10: terminalRows() = %d, want 10", got)
	}
}

// The region gets breathing room below it, so the status line is not hard
// against the bottom edge with the cursor sitting on it.
//
// The padding is charged to the REGION's budget, not the window's: on a
// terminal too short for everything the window gives up lines and the ticket
// rows stay, which is the same precedence the frame follows (OR-264).
func TestTheRegionIsPaddedAtTheBottomWithoutCostingTheRows(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	t.Setenv("LINES", "14")
	LiveReset()
	t.Cleanup(LiveReset)

	now := time.Now()
	for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
		liveStart(k, now.Add(-10*time.Minute))
	}
	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	for i := 0; i < 12; i++ {
		if _, err := fmt.Fprintf(l, "chatter-%02d\n", i); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}
	frame := lastFrame(b.String())

	if !strings.HasSuffix(frame, strings.Repeat("\n", liveBottomPad)) {
		t.Errorf("the region is not padded at the bottom:\n%q", frame)
	}
	// The rows are what the padding must never cost.
	for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
		if !strings.Contains(frame, k) {
			t.Errorf("%s was pushed off to make room for padding:\n%s", k, frame)
		}
	}
	// And the whole block still fits the terminal it was measured against.
	if l.drawn > 14 {
		t.Errorf("the block is %d rows on a 14-row terminal", l.drawn)
	}
}
