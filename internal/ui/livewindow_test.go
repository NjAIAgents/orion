package ui

// The frozen scrollback window (OR-248).
//
// One test per acceptance criterion, and each of them fails if the cap is
// removed rather than merely if the code is refactored: the chatter is bounded,
// the region and its status line cannot be pushed off screen by volume, a
// resize reflows without stranding rows, off a terminal nothing is capped at
// all, and the full log is one keystroke away.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
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

// Chatter is capped and scrolls within its own bounded area. Forty lines of
// output leave five on screen; the thirty-five that scrolled out are gone from
// the SCREEN, which is the whole point -- events.jsonl still has them.
func TestTheFrozenWindowCapsTheChatter(t *testing.T) {
	t.Setenv("LINES", "")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	for i := 0; i < 40; i++ {
		if _, err := fmt.Fprintf(l, "chatter-%02d\n", i); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}

	frame := lastFrame(b.String())
	if n := strings.Count(frame, "chatter-"); n != liveWindowFloor {
		t.Errorf("the window showed %d chatter lines, want %d:\n%q", n, liveWindowFloor, frame)
	}
	for i := 35; i < 40; i++ {
		if want := fmt.Sprintf("chatter-%02d", i); !strings.Contains(frame, want) {
			t.Errorf("the newest lines must survive; %q is missing:\n%q", want, frame)
		}
	}
	if strings.Contains(frame, "chatter-34") {
		t.Errorf("a line past the cap is still on screen:\n%q", frame)
	}
	// And the buffer is the window: an eight-hour run must not accumulate every
	// line it ever printed in memory for a cap that might never be dropped.
	if len(l.window) != liveWindowFloor {
		t.Errorf("the window retained %d lines, want %d", len(l.window), liveWindowFloor)
	}
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

// The window has a FLOOR, not a fixed height: it takes whatever room is left
// after the pinned rows, so a taller terminal shows more history -- and a
// terminal that never said how tall it is gets the floor rather than a guess.
func TestATallerTerminalShowsMoreHistory(t *testing.T) {
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	fill := func(l *Live) {
		for i := 0; i < 60; i++ {
			if _, err := fmt.Fprintf(l, "chatter-%02d\n", i); err != nil {
				t.Fatalf("writing: %v", err)
			}
		}
	}

	// The region is four rows -- rule, header, blank, one ticket row -- and the
	// cursor keeps one, so a 40-row terminal has 35 to spare.
	t.Setenv("LINES", "40")
	var tall bytes.Buffer
	fill(&Live{w: &tall, cursor: true})
	if n := strings.Count(lastFrame(tall.String()), "chatter-"); n != 35 {
		t.Errorf("a 40-row terminal showed %d lines of history, want 35", n)
	}

	t.Setenv("LINES", "")
	var unknown bytes.Buffer
	fill(&Live{w: &unknown, cursor: true})
	if n := strings.Count(lastFrame(unknown.String()), "chatter-"); n != liveWindowFloor {
		t.Errorf("an unknown height showed %d lines, want the floor of %d", n, liveWindowFloor)
	}

	// A height too small to be one is not a height. Below the floor the window
	// keeps its five lines regardless, because a window squeezed to nothing is
	// the hung-looking screen the region was built to fix.
	t.Setenv("LINES", "8")
	var tiny bytes.Buffer
	fill(&Live{w: &tiny, cursor: true})
	if n := strings.Count(lastFrame(tiny.String()), "chatter-"); n != liveWindowFloor {
		t.Errorf("a short terminal showed %d lines, want the floor of %d", n, liveWindowFloor)
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
	if l.drawn != 3 {
		t.Errorf("100 cells at 40 columns is 3 rows, not %d", l.drawn)
	}

	// Widen the terminal. The next redraw erases what the last one drew -- three
	// rows, measured at the old width -- and the same line now needs one.
	t.Setenv("COLUMNS", "120")
	b.Reset()
	redraw(l)
	if got := b.String(); !strings.HasPrefix(got, "\x1b[3A\x1b[0J") {
		t.Errorf("the erase must undo the rows actually drawn:\n%q", got)
	}
	if l.drawn != 1 {
		t.Errorf("100 cells at 120 columns is 1 row, not %d", l.drawn)
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

// A writer is a byte stream: one Write can carry three lines or half of one.
// A window that counted writes instead of lines would cap at five of whichever
// it happened to be handed.
func TestTheWindowCountsLinesNotWrites(t *testing.T) {
	t.Setenv("LINES", "")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	if _, err := l.Write([]byte("one\ntwo\nthree\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if got := strings.Join(l.window, "|"); got != "one|two|three" {
		t.Errorf("one write of three lines is three window entries, got %q", got)
	}

	// A half-written line is held rather than shown, so a later write completes
	// it instead of appending a second entry to amend.
	if _, err := l.Write([]byte("half")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if len(l.window) != 3 {
		t.Errorf("an unfinished line must not enter the window yet: %q", l.window)
	}
	if _, err := l.Write([]byte(" a line\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if got := l.window[len(l.window)-1]; got != "half a line" {
		t.Errorf("the completed line is %q, want %q", got, "half a line")
	}
}
