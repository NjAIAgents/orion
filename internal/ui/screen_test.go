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
	f := frame(&bytes.Buffer{}, 12, 80, "header", board, log)

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
	if boardIcon(VerbOK) != iconFor(VerbOK) {
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
	if h := stripANSI(s.header()); !strings.Contains(h, "up 8m") {
		t.Fatalf("header = %q, want the uptime", h)
	}
}
