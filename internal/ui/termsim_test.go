package ui

// A small terminal, so the erase-and-redraw contract can be checked against
// what a terminal DOES rather than against what the code counted (OR-317).
//
// Cursor up, erase to end of screen, newline, and autowrap at the right edge
// with the xterm eat-newline behaviour. Nothing else: this is the set of
// controls Live emits.

import (
	"strings"
	"unicode/utf8"
)

type termSim struct {
	cols, rows int
	lines      []string // every line ever on screen, top to bottom
	row, col   int      // cursor, row indexes into lines
}

func newTermSim(cols, rows int) *termSim {
	return &termSim{cols: cols, rows: rows, lines: []string{""}}
}

func (t *termSim) Write(p []byte) (int, error) {
	s := string(p)
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < '@' || s[j] > '~') {
				j++
			}
			if j >= len(s) {
				break
			}
			t.control(s[i+2:j], s[j])
			i = j + 1
			continue
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		switch r {
		case '\n':
			t.row++
			t.col = 0
			for t.row >= len(t.lines) {
				t.lines = append(t.lines, "")
			}
		case '\r':
			t.col = 0
		default:
			// Autowrap: a rune past the right edge starts a new row.
			if t.col >= t.cols {
				t.row++
				t.col = 0
				for t.row >= len(t.lines) {
					t.lines = append(t.lines, "")
				}
			}
			t.put(r)
		}
	}
	return len(p), nil
}

func (t *termSim) put(r rune) {
	line := []rune(t.lines[t.row])
	for len(line) < t.col {
		line = append(line, ' ')
	}
	if t.col < len(line) {
		line[t.col] = r
	} else {
		line = append(line, r)
	}
	t.lines[t.row] = string(line)
	t.col++
}

func (t *termSim) control(params string, final byte) {
	switch final {
	case 'A':
		n := 1
		if params != "" {
			n = 0
			for _, c := range params {
				n = n*10 + int(c-'0')
			}
		}
		// Clamped at the top of the VISIBLE screen, exactly as a terminal
		// clamps: scrollback is out of reach.
		top := len(t.lines) - t.rows
		if top < 0 {
			top = 0
		}
		t.row -= n
		if t.row < top {
			t.row = top
		}
	case 'J':
		// Erase from the cursor to the end of the screen.
		t.lines = t.lines[:t.row+1]
		line := []rune(t.lines[t.row])
		if t.col < len(line) {
			t.lines[t.row] = string(line[:t.col])
		}
	}
}

func (t *termSim) screen() string { return strings.Join(t.lines, "\n") }

func (t *termSim) count(sub string) int {
	n := 0
	for _, l := range t.lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}
