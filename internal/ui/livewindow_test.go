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
	// The buffer is BOUNDED, which is what this was always about: an
	// eight-hour run must not accumulate every line it ever printed for a cap
	// that might never be dropped.
	//
	// Bounded at liveWindowBuffer rather than at the visible cap, because
	// trimming the buffer to what fits made the loss permanent -- a line
	// dropped while the region was tall could never come back when it shrank,
	// so the window only ever emptied (OR-264).
	if len(l.window) > liveWindowBuffer {
		t.Errorf("the window retained %d lines, more than the %d it bounds at",
			len(l.window), liveWindowBuffer)
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

// The window is a FIXED HEIGHT: five lines of scrollback, then a wall.
//
// It shipped as a floor that grew into whatever the terminal had spare
// (OR-248), but that could never be observed -- terminalRows read LINES,
// which no shell exports, so the height was always unknown and the window sat
// on five by accident. Fixing the detection made it grow to twenty-four lines
// on a full-screen terminal, which is the unbounded log this feature exists
// to bound. The spare rows belong to the region (OR-264).
func TestTheWindowIsCappedHoweverTallTheTerminal(t *testing.T) {
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

	// Room for thirty-three lines of history, and it still shows five.
	t.Setenv("LINES", "40")
	var tall bytes.Buffer
	fill(&Live{w: &tall, cursor: true})
	if n := strings.Count(lastFrame(tall.String()), "chatter-"); n != liveWindowFloor {
		t.Errorf("a 40-row terminal showed %d lines of history, want the cap of %d",
			n, liveWindowFloor)
	}

	// An unknown height gets the same answer, which is what makes the cap a
	// height rather than a guess.
	t.Setenv("LINES", "")
	var unknown bytes.Buffer
	fill(&Live{w: &unknown, cursor: true})
	if n := strings.Count(lastFrame(unknown.String()), "chatter-"); n != liveWindowFloor {
		t.Errorf("an unknown height showed %d lines, want %d", n, liveWindowFloor)
	}

	// Below the cap the window yields to the region rather than overflowing:
	// an eight-row terminal cannot hold five lines of history AND the rows,
	// and the rows are the thing being watched.
	t.Setenv("LINES", "8")
	var tiny bytes.Buffer
	fill(&Live{w: &tiny, cursor: true})
	if n := strings.Count(lastFrame(tiny.String()), "chatter-"); n > liveWindowFloor {
		t.Errorf("a short terminal showed %d lines, more than the cap of %d", n, liveWindowFloor)
	}
	if !strings.Contains(lastFrame(tiny.String()), "OR-237") {
		t.Errorf("the ticket row was pushed off an 8-row terminal:\n%s", lastFrame(tiny.String()))
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
	// Three rows for the wrapped line, plus the frame's two.
	if l.drawn != 5 {
		t.Errorf("100 cells at 40 columns is 3 rows plus a 2-row frame, not %d", l.drawn)
	}

	// Widen the terminal. The next redraw erases what the last one drew -- five
	// rows, measured at the old width -- and the same line now needs one plus
	// the frame.
	t.Setenv("COLUMNS", "120")
	b.Reset()
	redraw(l)
	if got := b.String(); !strings.HasPrefix(got, "\x1b[5A\x1b[0J") {
		t.Errorf("the erase must undo the rows actually drawn:\n%q", got)
	}
	if l.drawn != 3 {
		t.Errorf("100 cells at 120 columns is 1 row plus a 2-row frame, not %d", l.drawn)
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

// A run that ends after only a couple of lines shows those lines, not a blank
// screen padded to nothing and not a screen that pretends five lines happened
// when only two did.
func TestEndingOnFewWritesShowsThoseLinesNotBlank(t *testing.T) {
	t.Setenv("LINES", "")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	if _, err := fmt.Fprintln(l, "only-line"); err != nil {
		t.Fatalf("writing: %v", err)
	}

	frame := lastFrame(b.String())
	if !strings.Contains(frame, "only-line") {
		t.Errorf("a single line must still be on screen, got:\n%q", frame)
	}
	if len(l.window) != 1 {
		t.Errorf("the window holds %d lines, want 1", len(l.window))
	}
}

// A blank line is still a line: whitespace-only output must survive into the
// window rather than being swallowed as if it were nothing.
func TestWhitespaceOnlyLinesAreKept(t *testing.T) {
	t.Setenv("LINES", "")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	if _, err := fmt.Fprintln(l, "   "); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := fmt.Fprintln(l, "after"); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if len(l.window) != 2 || l.window[0] != "   " {
		t.Errorf("a whitespace-only line must be kept as its own entry, got %q", l.window)
	}
}

// A line ending \r\n is normalised to end at \n, matching what a terminal
// does with a carriage return before a newline. A \r that is not at the end
// of the line is content, not a line ending, and must not be touched.
func TestCarriageReturnAtLineEndIsStripped(t *testing.T) {
	t.Setenv("LINES", "")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	if _, err := fmt.Fprint(l, "crlf-line\r\nmid\rline\n"); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if got := strings.Join(l.window, "|"); got != "crlf-line|mid\rline" {
		t.Errorf("got %q", got)
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

// Three zones share the screen and exactly one of them scrolls (OR-248).
// Without a boundary the scrolling zone and the pinned rows below it read as
// one continuous log, which is precisely the distinction that matters. The
// labels say which zone is which and how much history is being kept, so a
// line vanishing off the top is explained rather than merely noticed.
func TestTheFrozenWindowIsBoundedByALabelledFrame(t *testing.T) {
	t.Setenv("LINES", "24")
	t.Setenv("COLUMNS", "100")
	LiveReset()
	t.Cleanup(LiveReset)

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	for i := 0; i < 3; i++ {
		if _, err := fmt.Fprintf(l, "chatter-%d\n", i); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}
	got := lastFrame(b.String())

	if !strings.Contains(got, "recent") {
		t.Errorf("the window does not say what it is:\n%s", got)
	}
	if !strings.Contains(got, "3 line(s)") {
		t.Errorf("the window does not say how much history it is holding:\n%s", got)
	}
	if !strings.Contains(got, "scrolls, then gone") {
		t.Errorf("the window does not say its lines are transient:\n%s", got)
	}
	// The frame bounds the chatter: every captured line falls between the two
	// rules, not outside them.
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	top, bottom := -1, -1
	for i, ln := range lines {
		if strings.Contains(ln, "recent") {
			top = i
		}
		if strings.Contains(ln, "scrolls, then gone") {
			bottom = i
		}
	}
	if top < 0 || bottom < 0 || bottom <= top {
		t.Fatalf("the frame is not a pair of rules around the window:\n%s", got)
	}
	for i, ln := range lines {
		if strings.Contains(ln, "chatter-") && (i < top || i > bottom) {
			t.Errorf("line %d escaped the frame:\n%s", i, got)
		}
	}
}

// Nothing captured yet, no frame. An empty box above an empty region is two
// rules saying nothing.
func TestNoChatterDrawsNoFrame(t *testing.T) {
	t.Setenv("LINES", "24")
	LiveReset()
	t.Cleanup(LiveReset)

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	redraw(l)
	if strings.Contains(b.String(), "recent") {
		t.Errorf("an empty window must not draw a frame:\n%s", b.String())
	}
}

// THE REGION OUTRANKS THE WINDOW FLOOR. On a terminal too short for both, an
// unconditional five-line floor spends rows the region needs and the ticket
// rows go off the top -- with the header still saying "3 running" over the
// empty space where they were, which is exactly how this was reported
// (OR-264).
func TestAShortTerminalKeepsTheRowsAndShrinksTheWindow(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	// Twelve rows for a region that needs most of them: rule, header, blank
	// and three ticket rows, plus the window's own frame.
	t.Setenv("LINES", "12")
	LiveReset()
	t.Cleanup(LiveReset)

	now := time.Now()
	for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
		liveStart(k, now.Add(-10*time.Minute))
	}

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	for i := 0; i < 20; i++ {
		if _, err := fmt.Fprintf(l, "chatter-%02d\n", i); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}
	got := lastFrame(b.String())

	// Every ticket row survived.
	for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
		if !strings.Contains(got, k) {
			t.Errorf("%s was pushed off a short terminal:\n%s", k, got)
		}
	}
	// And the whole block still fits, which is what makes that true on a real
	// terminal rather than only in a buffer.
	if rows := strings.Count(strings.TrimRight(got, "\n"), "\n") + 1; rows > 12 {
		t.Errorf("the block is %d rows on a 12-row terminal:\n%s", rows, got)
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
