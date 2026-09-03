package ui

// fitRows trims a pinned block to what the terminal can hold.
//
// The erase moves the cursor UP by the rows last drawn, and a terminal clamps
// that move at its top row. A block taller than the screen therefore pushes
// its first rows into scrollback where the erase cannot reach, and the next
// redraw paints them again -- which is the frame repeating down the screen
// that OR-317 could not reproduce under test. Nothing capped the region: only
// the frozen window yielded, and once it had yielded everything the rows,
// the status line and the batch block still grew without a ceiling.
//
// Trimmed from the TOP. The status line and the batch verdict sit at the
// bottom, nearest the cursor, and that is where the eye returns; a ticket
// row lost off the top on a very short terminal is still counted in the
// status line's "N running".
func fitRows(lines []string, termRows, cols int) []string {
	if termRows <= 0 {
		return lines
	}
	// One row for the cursor itself, which sits below everything drawn.
	budget := termRows - 1
	start, rows := len(lines), 0
	for i := len(lines) - 1; i >= 0; i-- {
		r := screenRows(lines[i], cols)
		if rows+r > budget {
			break
		}
		rows += r
		start = i
	}
	return lines[start:]
}
