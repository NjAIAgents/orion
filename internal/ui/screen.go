package ui

// The watcher's full-screen view, drawn like top (OR-548).
//
// The board at the top, the newest log lines under it, the whole screen
// redrawn once a second. The scrolling log printed the board again every few
// minutes, and a person watching had to find the latest copy among the lines.
//
// OR-334 removed a pinned region after six display defects in two days, every
// one of them the same shape: the region erased a counted number of rows, the
// terminal had scrolled or wrapped a different number, and the difference
// was stranded on screen. This view has nothing to count. It runs on the
// alternate screen, every frame starts from the top-left corner, auto-wrap is
// switched off so no line can take two rows, and a frame is cut to the
// terminal's height so nothing ever scrolls. A wrong frame is repaired by the
// next one a second later instead of accumulating.
//
// Every line also goes to a log file, with colour stripped, because the
// alternate screen has no scrollback: what scrolled off the view is in the
// file, and the header names it.

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// screenKeep is how many log lines the view holds; more than any terminal
// shows, few enough to cost nothing.
const screenKeep = 500

// screenEvery is how often the view redraws: the same 250ms `orion plan`'s
// live line turns its spinner at (OR-551).
const screenEvery = 250 * time.Millisecond

// Screen is the full-screen view. It is an io.Writer: everything the watcher
// prints goes into it and appears under the board.
type Screen struct {
	mu      sync.Mutex
	term    io.Writer
	log     io.WriteCloser
	logPath string
	title   string
	lines   []string
	partial string
	stop    chan struct{}
	done    chan struct{}
	closed  bool
}

var current struct {
	mu sync.Mutex
	s  *Screen
}

// Escape sequences. Alternate screen on/off, cursor hidden/shown, auto-wrap
// off/on, cursor to the top-left, erase to end of line, erase below.
const (
	escEnter = "\x1b[?1049h\x1b[?25l\x1b[?7l"
	escLeave = "\x1b[?7h\x1b[?25h\x1b[?1049l"
	escHome  = "\x1b[H"
	escEOL   = "\x1b[K"
	escBelow = "\x1b[J"
)

// StartScreen takes over the terminal and returns the writer to print
// through. logPath is where every line is also written; empty, or a file
// that cannot be created, means the view alone.
func StartScreen(term io.Writer, title, logPath string) *Screen {
	s := &Screen{term: term, title: title, stop: make(chan struct{}), done: make(chan struct{})}
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			s.log, s.logPath = f, logPath
		}
	}
	current.mu.Lock()
	current.s = s
	current.mu.Unlock()
	fmt.Fprint(term, escEnter)
	s.draw()
	go s.loop()
	return s
}

// ScreenOn reports whether a full-screen view owns the terminal, so the board
// is not also printed into the log it draws.
func ScreenOn() bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.s != nil
}

// CloseScreen gives the terminal back, if a view has it. Safe to call more
// than once and from any goroutine; the signal handler calls it before exit.
func CloseScreen() {
	current.mu.Lock()
	s := current.s
	current.s = nil
	current.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// Unwrap names the terminal underneath, so colour decisions see a terminal.
func (s *Screen) Unwrap() io.Writer { return s.term }

// Write keeps each complete line for the view and the log file. A trailing
// fragment waits for the rest of its line.
func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.term.Write(p)
	}
	if s.log != nil {
		_, _ = io.WriteString(s.log, stripANSI(string(p)))
	}
	text := s.partial + string(p)
	parts := strings.Split(text, "\n")
	s.partial = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		s.lines = append(s.lines, strings.TrimRight(l, "\r"))
	}
	if over := len(s.lines) - screenKeep; over > 0 {
		s.lines = append([]string(nil), s.lines[over:]...)
	}
	return len(p), nil
}

// Close stops the redraw, leaves the alternate screen, and prints the last
// lines on the normal screen so the watcher's ending stays readable after it
// exits.
func (s *Screen) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	<-s.done

	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprint(s.term, escLeave)
	tail := s.lines
	if s.partial != "" {
		tail = append(tail, s.partial)
	}
	if len(tail) > 30 {
		tail = tail[len(tail)-30:]
	}
	for _, l := range tail {
		fmt.Fprintln(s.term, l)
	}
	if s.logPath != "" {
		fmt.Fprintf(s.term, "full log: %s\n", s.logPath)
	}
	if s.log != nil {
		_ = s.log.Close()
	}
}

func (s *Screen) loop() {
	defer close(s.done)
	t := time.NewTicker(screenEvery)
	defer t.Stop()
	frames := 0
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			// The size is asked once a second, not every frame: asking forks
			// stty, and four a second beside the agents is the cost OR-317 cut.
			if frames++; frames%4 == 0 {
				invalidateTerminalSize()
			}
			s.draw()
		}
	}
}

func (s *Screen) draw() {
	rows, cols := sttySize()
	if rows <= 0 {
		rows = 40
	}
	if cols <= 0 {
		cols = 100
	}
	board.mu.Lock()
	board.spinning = true
	board.spin++
	b := renderBoard(s, clock())
	board.spinning = false
	board.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	fmt.Fprint(s.term, frame(s, rows, cols, s.header(), b, s.lines))
}

func (s *Screen) header() string {
	h := fmt.Sprintf(" %s  %s", Heading(s, "orion watch "+s.title), Dim(s, clock().Format("15:04:05")))
	if s.logPath != "" {
		h += Dim(s, "   log "+s.logPath)
	}
	return h + Dim(s, "   ctrl-c stops")
}

// frame is one full redraw: exactly rows lines or fewer, none wider than the
// terminal, the log lines getting whatever the board leaves.
func frame(w io.Writer, rows, cols int, header, board string, log []string) string {
	top := append([]string{header}, strings.Split(strings.TrimRight(board, "\n"), "\n")...)
	if len(top) > rows {
		top = top[:rows]
	}
	room := rows - len(top)
	if room > len(log) {
		room = len(log)
	}
	all := append(top, log[len(log)-room:]...)

	var b strings.Builder
	b.WriteString(escHome)
	for i, l := range all {
		b.WriteString(clipVisible(l, cols-1))
		b.WriteString(escEOL)
		if i < len(all)-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString(escBelow)
	return b.String()
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// clipVisible cuts a line to n visible columns, passing colour codes through
// uncounted and resetting colour where it cuts. A wide rune counts two.
func clipVisible(s string, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	width := 0
	for i := 0; i < len(s); {
		if loc := ansiRE.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(s[i : i+loc[1]])
			i += loc[1]
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if width+rw > n {
			b.WriteString("\x1b[0m")
			return b.String()
		}
		b.WriteRune(r)
		width += rw
		i += size
	}
	return b.String()
}

// runeWidth is 2 for the wide ranges a status line uses (emoji, CJK), else 1.
func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3, r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE4F, r >= 0xFF00 && r <= 0xFF60,
		r >= 0x1F300 && r <= 0x1FAFF, r == 0x23F3, r == 0x231B:
		return 2
	}
	return 1
}
