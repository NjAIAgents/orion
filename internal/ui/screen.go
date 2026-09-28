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
	started time.Time
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
	// headerBg is the header bar and panelBg the board beneath it: a dark
	// theme for the frozen top of the screen, white text on near-black, the
	// header a shade lighter so it reads as a title (OR-555).
	headerBg = "\x1b[97;48;5;237m"
	panelBg  = "\x1b[97;48;5;234m"
	// footBg is the bottom panel -- the batch, CI and last result -- in grey,
	// set apart from the dark top so the two read as different things (OR-557).
	// 238 since OR-559, the shade the approved design used.
	footBg = "\x1b[97;48;5;238m"
	// The light theme's three panels (OR-559): the same arrangement, pale
	// grounds under near-black text, the header the darkest so it still reads
	// as a title bar.
	lightHeaderBg = "\x1b[38;5;235;48;5;252m"
	lightPanelBg  = "\x1b[38;5;235;48;5;255m"
	lightFootBg   = "\x1b[38;5;235;48;5;253m"
)

// StartScreen takes over the terminal and returns the writer to print
// through. logPath is where every line is also written; empty, or a file
// that cannot be created, means the view alone.
func StartScreen(term io.Writer, title, logPath string) *Screen {
	s := &Screen{term: term, title: title, started: clock(), stop: make(chan struct{}), done: make(chan struct{})}
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
	// The header's counts are read before s.mu is taken, never under it:
	// BoardTick holds board.mu while it writes, and its writer can be this
	// screen, so board.mu inside s.mu is a lock-order inversion.
	sum := boardSummary()
	board.mu.Lock()
	board.spinning = true
	board.spin++
	b, foot := renderBoardParts(s, clock(), true)
	board.spinning = false
	board.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	fmt.Fprint(s.term, frame(s, rows, cols, s.header(cols, sum), b, s.lines, foot))
}

// header is the summary bar (OR-559): what is watched and for how long, then
// the counts a person looks up to see -- running, queued, landed, failed and
// the spend -- each in its state's colour, and the clock at the right edge.
// The counts lived in a totals row under the queue, where they were the
// last thing read on a screen whose first question is "how is it going".
//
// The log path follows the clock when the row has room, and is dropped when
// it has not: Close prints it on the normal screen whatever happens, so a
// narrow terminal loses nothing it cannot get back. "ctrl-c stops" went
// the same way -- it is what every terminal program does.
func (s *Screen) header(cols int, c boardCounts) string {
	now := clock()
	bar := "│"
	if !glyphs() {
		bar = "|"
	}
	count := func(verb string, n int, what string) string {
		return paint(s, stateColor(verb), fmt.Sprintf("%s %d %s", strings.TrimSpace(iconFor(verb)), n, what))
	}
	sep := Dim(s, " · ")
	left := " " + Heading(s, "orion watch "+s.title) + Dim(s, " · up "+roundDur(now.Sub(s.started))+"  "+bar+"  ") +
		count(VerbWorking, c.running, "running") + sep + count(VerbWaiting, c.queued, "queued") + sep +
		count(VerbOK, c.landed, "landed") + sep + count(VerbFail, c.failed, "failed") + sep +
		fmt.Sprintf("$%.2f", c.spend)
	right := now.Format("15:04:05")
	if s.logPath != "" && visibleCells(left)+2+len(right)+6+visibleCells(s.logPath) <= cols-1 {
		right += "   log " + s.logPath
	}
	gap := cols - 1 - visibleCells(left) - visibleCells(right)
	if gap < 2 {
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + Dim(s, right)
}

// frame is one full redraw: exactly rows lines or fewer, none wider than the
// terminal, the log lines getting whatever the board leaves.
func frame(w io.Writer, rows, cols int, header, board string, log []string, bottom string) string {
	top := append([]string{header}, strings.Split(strings.TrimRight(board, "\n"), "\n")...)
	if len(top) > rows {
		top = top[:rows]
	}
	// The bottom panel (OR-557) keeps its rows at the foot of the screen,
	// and the log gets whatever is left between the two panels -- padded
	// with blank rows, so the bottom panel does not ride up when the log is
	// short.
	var foot []string
	if strings.TrimSpace(bottom) != "" {
		foot = strings.Split(strings.TrimRight(bottom, "\n"), "\n")
	}
	if len(foot) > rows-len(top) {
		foot = foot[:rows-len(top)]
	}
	room := rows - len(top) - len(foot)
	shown := room
	if shown > len(log) {
		shown = len(log)
	}
	middle := append([]string(nil), log[len(log)-shown:]...)
	if len(foot) > 0 {
		for len(middle) < room {
			middle = append(middle, "")
		}
	}

	type row struct {
		text string
		bg   string // "" is the terminal's own background
	}
	var all []row
	for i, l := range top {
		bg := panelBg
		if i == 0 {
			bg = headerBg
		}
		all = append(all, row{l, bg})
	}
	for _, l := range middle {
		all = append(all, row{l, ""})
	}
	for _, l := range foot {
		all = append(all, row{l, footBg})
	}

	// The theme picks the panels' grounds and the recolouring their text
	// gets (OR-559). Mono never reaches the panel branch below: enabled is
	// false under it, so the frame carries no colour code at all.
	recolor := onDark
	if theme() == themeLight {
		recolor = onLight
		for i := range all {
			switch all[i].bg {
			case headerBg:
				all[i].bg = lightHeaderBg
			case panelBg:
				all[i].bg = lightPanelBg
			case footBg:
				all[i].bg = lightFootBg
			}
		}
	}

	var b strings.Builder
	b.WriteString(escHome)
	for i, r := range all {
		// A panel row's background is set before the erase, and
		// erase-to-end-of-line fills the row with it (OR-555). The log keeps
		// the terminal's own colours.
		if r.bg != "" && enabled(w) {
			// Every reset inside re-applies the panel, or the first dim word
			// would end the colour halfway along the row.
			line := strings.ReplaceAll(recolor.Replace(clipVisible(r.text, cols-1)), reset, reset+r.bg)
			b.WriteString(r.bg + line + r.bg + escEOL + reset)
		} else {
			text := r.text
			if !enabled(w) {
				// Mono means none (OR-559), even in a line that arrived
				// already painted -- an agent's own output, say.
				text = stripANSI(text)
			}
			b.WriteString(clipVisible(text, cols-1))
			b.WriteString(escEOL)
		}
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
// uncounted and resetting colour where it cuts. A wide rune counts two. A
// line that carried no escape code is cut without one, so a mono frame
// (OR-559) stays free of them.
func clipVisible(s string, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	width := 0
	coloured := false
	for i := 0; i < len(s); {
		if loc := ansiRE.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(s[i : i+loc[1]])
			i += loc[1]
			coloured = true
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if width+rw > n {
			if coloured {
				b.WriteString("\x1b[0m")
			}
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

// visibleCells is how many terminal columns s takes: escape codes count
// nothing and a wide rune counts two. What the watch view pads and aligns
// by (OR-559), since a rune count puts a row with colour or a wide glyph out
// of line with the rest.
func visibleCells(s string) int {
	n := 0
	for _, r := range stripANSI(s) {
		n += runeWidth(r)
	}
	return n
}

// padCells widens s, colour and all, to n visible columns. Never truncates.
func padCells(s string, n int) string {
	if d := n - visibleCells(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// onDark swaps the terminal's basic and bright colours for fixed light 256-colour
// shades on the dark panels (OR-555). Basic colours follow the terminal's theme, and
// several themes draw even "bright blue" as a dark blue that disappears on near-black;
// a 256-colour value is the same on every theme. Dim becomes a light grey for the same
// reason. The 256-colour identity palettes are already light and pass through.
var onDark = strings.NewReplacer(
	"\x1b[30m", "\x1b[38;5;250m",
	"\x1b[31m", "\x1b[38;5;210m", "\x1b[91m", "\x1b[38;5;210m",
	"\x1b[32m", "\x1b[38;5;114m", "\x1b[92m", "\x1b[38;5;114m",
	"\x1b[33m", "\x1b[38;5;221m", "\x1b[93m", "\x1b[38;5;221m",
	"\x1b[34m", "\x1b[38;5;111m", "\x1b[94m", "\x1b[38;5;117m",
	"\x1b[35m", "\x1b[38;5;213m", "\x1b[95m", "\x1b[38;5;219m",
	"\x1b[36m", "\x1b[38;5;116m", "\x1b[96m", "\x1b[38;5;123m",
	"\x1b[2m", "\x1b[38;5;248m",
)

// onLight is onDark's mirror for the light theme (OR-559): every colour the
// panels carry, basic or 256, moved to a DARK shade that reads on a pale
// ground. The terminal's own yellow and cyan are near-invisible on white on
// most themes, and the dark theme's fixed light shades (the key colour, the
// actor colour) would be worse. The section chip's slate is lifted a step so
// it is still a chip on a white panel; NEEDS YOU keeps its magenta.
var onLight = strings.NewReplacer(
	"\x1b[30m", "\x1b[38;5;235m",
	"\x1b[31m", "\x1b[38;5;160m", "\x1b[91m", "\x1b[38;5;160m",
	"\x1b[32m", "\x1b[38;5;28m", "\x1b[92m", "\x1b[38;5;28m",
	"\x1b[33m", "\x1b[38;5;130m", "\x1b[93m", "\x1b[38;5;130m",
	"\x1b[34m", "\x1b[38;5;25m", "\x1b[94m", "\x1b[38;5;25m",
	"\x1b[35m", "\x1b[38;5;127m", "\x1b[95m", "\x1b[38;5;127m",
	"\x1b[36m", "\x1b[38;5;30m", "\x1b[96m", "\x1b[38;5;30m",
	"\x1b[2m", "\x1b[38;5;242m",
	"\x1b[38;5;117m", "\x1b[38;5;25m", // the ticket key
	"\x1b[38;5;180m", "\x1b[38;5;94m", // who
	chipBg, "\x1b[48;5;103m",
)
