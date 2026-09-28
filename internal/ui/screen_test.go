package ui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A frame is the OR-334 failure class designed out: never more rows than the
// terminal has, never a line wider than it, so nothing scrolls or wraps.
func TestAFrameFitsTheTerminalExactly(t *testing.T) {
	var log []string
	for i := 0; i < 100; i++ {
		log = append(log, fmt.Sprintf("\x1b[31mline %d %s\x1b[0m", i, strings.Repeat("x", 300)))
	}
	board := strings.Repeat("board row ⏳ "+strings.Repeat("y", 200)+"\n", 5)
	f := frame(&bytes.Buffer{}, 12, 80, "header", board, log, "")

	rows := strings.Split(strings.TrimSuffix(strings.TrimPrefix(f, escHome), escBelow), "\r\n")
	if len(rows) != 12 {
		t.Fatalf("frame has %d rows on a 12-row terminal", len(rows))
	}
	for _, r := range rows {
		if w := visibleWidth(stripANSI(r)); w > 79 {
			t.Fatalf("a row is %d columns wide on an 80-column terminal: %q", w, stripANSI(r))
		}
	}
	// The newest log lines are the ones shown.
	if !strings.Contains(f, "line 99 ") || strings.Contains(f, "line 93 ") {
		t.Fatalf("the frame does not end on the newest log lines")
	}
}

func visibleWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func TestClipVisibleKeepsColourAndCountsWideRunes(t *testing.T) {
	got := clipVisible("\x1b[31mab⏳cd\x1b[0m", 4)
	if stripANSI(got) != "ab⏳" {
		t.Fatalf("clipped to %q, want the wide rune counted as two", stripANSI(got))
	}
	if !strings.HasPrefix(got, "\x1b[31m") || !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("colour not carried and reset across the cut: %q", got)
	}
}

// Lines go to the view whole, a fragment waits for its newline, and the log
// file gets every line with colour stripped.
func TestTheScreenKeepsLinesAndWritesThePlainLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "watch.log")
	term := &lockedBuf{}
	s := StartScreen(term, "LTA", logPath)
	t.Cleanup(CloseScreen)

	fmt.Fprint(s, "\x1b[32mfirst\x1b[0m\nsec")
	fmt.Fprint(s, "ond\n")
	s.mu.Lock()
	lines := append([]string(nil), s.lines...)
	s.mu.Unlock()
	if len(lines) != 2 || stripANSI(lines[0]) != "first" || lines[1] != "second" {
		t.Fatalf("lines = %q", lines)
	}

	CloseScreen()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "first\nsecond\n" {
		t.Fatalf("log file = %q, want plain lines", b)
	}
}

// Closing gives the terminal back and leaves the ending readable on the
// normal screen: the leave sequence, then the last lines, then the log path.
func TestClosingRestoresTheTerminalAndPrintsTheTail(t *testing.T) {
	term := &lockedBuf{}
	s := StartScreen(term, "LTA", filepath.Join(t.TempDir(), "w.log"))
	if !ScreenOn() {
		t.Fatal("a started screen is not reported on")
	}
	fmt.Fprintln(s, "stopped. Nothing was left half-done")
	CloseScreen()
	CloseScreen() // twice is safe

	if ScreenOn() {
		t.Fatal("the screen still reports on after closing")
	}
	out := term.String()
	leave := strings.LastIndex(out, escLeave)
	if leave < 0 {
		t.Fatal("the alternate screen was never left")
	}
	after := out[leave:]
	for _, want := range []string{"stopped. Nothing was left half-done", "full log: "} {
		if !strings.Contains(after, want) {
			t.Fatalf("%q not printed on the normal screen:\n%s", want, after)
		}
	}
	// Writes after close reach the terminal directly.
	fmt.Fprintln(s, "late")
	if !strings.HasSuffix(term.String(), "late\n") {
		t.Fatal("a write after close was lost")
	}
}

// The board is drawn by the view, not printed into its log as well.
func TestTheBoardIsNotPrintedWhileTheScreenDrawsIt(t *testing.T) {
	resetBoard()
	t.Cleanup(resetBoard)
	BoardEnable()
	boardStart("OR-1")
	s := StartScreen(&lockedBuf{}, "OR", "")
	t.Cleanup(CloseScreen)

	var out bytes.Buffer
	BoardTick(&out)
	if out.Len() != 0 {
		t.Fatalf("BoardTick printed with the screen on:\n%s", out.String())
	}
	_ = s
}

// The in-progress icon turns only while the full-screen view draws the board
// (OR-551); the plain log keeps the still one it always printed.
func TestTheWorkingIconTurnsOnlyOnTheScreen(t *testing.T) {
	resetBoard()
	t.Cleanup(resetBoard)
	board.mu.Lock()
	defer board.mu.Unlock()
	if boardIcon(VerbWorking) != iconFor(VerbWorking) {
		t.Fatal("the plain log's working icon changed")
	}
	board.spinning = true
	seen := map[string]bool{}
	for board.spin = 0; board.spin < 4; board.spin++ {
		seen[boardIcon(VerbWorking)] = true
	}
	if len(seen) != 4 {
		t.Fatalf("four frames drew %d distinct icons, want 4", len(seen))
	}
	if boardIcon(VerbDone) != iconFor(VerbDone) {
		t.Fatal("a finished icon spins too")
	}
}

// The header says how long the watcher has been running.
func TestTheHeaderSaysHowLongItHasRun(t *testing.T) {
	base := time.Date(2026, 9, 27, 16, 0, 0, 0, time.Local)
	now := base
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now })
	s := &Screen{title: "LTA", started: base, term: &bytes.Buffer{}}
	now = base.Add(8*time.Minute + 30*time.Second)
	if h := stripANSI(s.header(118, boardCounts{})); !strings.Contains(h, "up 8m") {
		t.Fatalf("header = %q, want the uptime", h)
	}
}

// OR-555: section labels are filled chips of one width, so content lines up
// under them. OR-559: every chip is the one slate but NEEDS YOU, which alone
// keeps magenta; without colour a chip is its label in brackets.
func TestSectionChipsAreColouredAndAligned(t *testing.T) {
	colourOn(t)
	var w bytes.Buffer
	for _, s := range []string{"RUNNING", "QUEUE", "BATCH", "CI", "LAST", "NEEDS YOU", ""} {
		c := sectionChip(&w, s)
		if n := visibleWidth(stripANSI(c)); n != sectionChipWidth {
			t.Fatalf("chip %q is %d wide, want %d", s, n, sectionChipWidth)
		}
		want := chipBg
		if s == "NEEDS YOU" {
			want = needsBg
		}
		if s != "" && !strings.Contains(c, want) {
			t.Fatalf("chip %q lacks its background %q: %q", s, want, c)
		}
		if s != "NEEDS YOU" && strings.Contains(c, needsBg) {
			t.Fatalf("chip %q wears the NEEDS YOU magenta", s)
		}
	}
	t.Setenv("NO_COLOR", "1")
	if c := sectionChip(&w, "QUEUE"); strings.Contains(c, "\x1b") || c != "[QUEUE]    " {
		t.Fatalf("a chip with NO_COLOR set = %q, want the bracketed label and no escapes", c)
	}
}

// The header bar keeps its background across the resets inside it.
func TestTheHeaderBarSurvivesInnerResets(t *testing.T) {
	colourOn(t)
	var w bytes.Buffer
	h := Heading(&w, "orion watch LTA") + " " + Dim(&w, "16:00")
	f := frame(&w, 5, 80, h, "", nil, "")
	first := strings.SplitN(strings.TrimPrefix(f, escHome), "\r\n", 2)[0]
	if !strings.Contains(first, headerBg) || strings.Count(first, headerBg) < strings.Count(first, reset) {
		t.Fatalf("a reset in the header is not followed by the bar colour: %q", first)
	}
}

// colourOn forces colour for one test: NO_COLOR unset (set to anything, even
// empty, it wins) and CLICOLOR_FORCE on.
func colourOn(t *testing.T) {
	t.Helper()
	t.Setenv("CLICOLOR_FORCE", "1")
	if v, ok := os.LookupEnv("NO_COLOR"); ok {
		os.Unsetenv("NO_COLOR")
		t.Cleanup(func() { os.Setenv("NO_COLOR", v) })
	}
}

// OR-555: the whole top panel -- header and board -- is dark, and the log
// rows beneath it are not.
func TestTheTopPanelIsDarkAndTheLogIsNot(t *testing.T) {
	colourOn(t)
	var w bytes.Buffer
	f := frame(&w, 6, 80, "head", "row one\nrow two", []string{"log line"}, "")
	rows := strings.Split(strings.TrimSuffix(strings.TrimPrefix(f, escHome), escBelow), "\r\n")
	if !strings.HasPrefix(rows[1], panelBg) || !strings.HasPrefix(rows[2], panelBg) {
		t.Fatalf("board rows are not on the dark panel: %q", rows[1:3])
	}
	if strings.Contains(rows[3], panelBg) || strings.Contains(rows[3], headerBg) {
		t.Fatalf("the log row carries the panel colour: %q", rows[3])
	}
}

// OR-555: plain blue and dim are unreadable on the dark panel, so the panel
// swaps them for bright variants; the log keeps them as they were.
func TestDarkPanelTextIsBrightened(t *testing.T) {
	colourOn(t)
	var w bytes.Buffer
	f := frame(&w, 5, 80, "head", "\x1b[34mLTA-2\x1b[0m \x1b[2mqueued\x1b[0m", []string{"\x1b[34mlog\x1b[0m"}, "")
	rows := strings.Split(strings.TrimSuffix(strings.TrimPrefix(f, escHome), escBelow), "\r\n")
	if strings.Contains(rows[1], "\x1b[34m") || strings.Contains(rows[1], "\x1b[2m") {
		t.Fatalf("dark panel row still carries plain blue or dim: %q", rows[1])
	}
	if !strings.Contains(rows[1], "\x1b[38;5;111m") {
		t.Fatalf("blue was not lifted to a fixed light shade on the panel: %q", rows[1])
	}
	for _, themed := range []string{"\x1b[94m", "\x1b[34m"} {
		if strings.Contains(rows[1], themed) {
			t.Fatalf("a theme-dependent blue survived on the panel: %q", rows[1])
		}
	}
	if !strings.Contains(rows[2], "\x1b[34m") {
		t.Fatalf("the log row's colour was changed: %q", rows[2])
	}
}

// OR-557: the batch and CI sit in a grey panel pinned to the foot of the
// screen, the log between the two panels, padded so the foot does not ride
// up when the log is short.
func TestTheBottomPanelIsPinnedAndGrey(t *testing.T) {
	colourOn(t)
	var w bytes.Buffer
	f := frame(&w, 8, 80, "head", "top", []string{"log one"}, "BATCH row\nCI row")
	rows := strings.Split(strings.TrimSuffix(strings.TrimPrefix(f, escHome), escBelow), "\r\n")
	if len(rows) != 8 {
		t.Fatalf("frame has %d rows, want the full 8 so the foot sits at the bottom", len(rows))
	}
	if !strings.HasPrefix(rows[6], footBg) || !strings.Contains(rows[6], "BATCH row") ||
		!strings.HasPrefix(rows[7], footBg) || !strings.Contains(rows[7], "CI row") {
		t.Fatalf("the last two rows are not the grey foot: %q", rows[6:])
	}
	// The grey runs to the end of the last row: nothing after it erases
	// from the cursor, which sits mid-row (seen live, 2026-09-28).
	if !strings.HasSuffix(f, footBg+escEOL+reset) {
		t.Fatalf("the frame does not end with the grey row filled to its end: %q", f[len(f)-40:])
	}
	if !strings.Contains(rows[2], "log one") || strings.Contains(rows[2], footBg) || strings.Contains(rows[2], panelBg) {
		t.Fatalf("the log row is not between the panels on the terminal background: %q", rows[2])
	}
}

// With no batch, no foot: the log keeps every row.
func TestNoBatchMeansNoBottomPanel(t *testing.T) {
	colourOn(t)
	var w bytes.Buffer
	f := frame(&w, 8, 80, "head", "top", []string{"a", "b"}, "")
	if strings.Contains(f, footBg) {
		t.Fatal("a bottom panel was drawn with nothing to show")
	}
}

// The split puts the batch in the foot and keeps RUNNING in the top.
func TestTheBoardSplitsBatchIntoTheFoot(t *testing.T) {
	resetBoard()
	t.Cleanup(resetBoard)
	BoardEnable()
	LiveBatchStart("orion/batch", "develop", []string{"LTA-2"})
	LiveChecks([]Check{{Name: "test", State: CheckRunning}})
	board.mu.Lock()
	top, foot := renderBoardParts(&bytes.Buffer{}, clock(), true)
	whole, _ := renderBoardParts(&bytes.Buffer{}, clock(), false)
	board.mu.Unlock()
	if strings.Contains(top, "BATCH") || !strings.Contains(foot, "BATCH") || !strings.Contains(foot, "CI") {
		t.Fatalf("split wrong:\ntop:\n%s\nfoot:\n%s", top, foot)
	}
	if !strings.Contains(top, "RUNNING") || !strings.Contains(whole, "BATCH") {
		t.Fatal("the unsplit board lost the batch, or the top lost RUNNING")
	}
}
