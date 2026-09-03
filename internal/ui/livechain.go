package ui

// The CI block at the foot of the batch (OR-246, revised 2026-09-01).
//
// Verdict, then membership, then evidence: a titled rule, the CHAIN, the
// JOBS, a tally. The chain is one bar segmented by member with ONE fill
// sweeping across the cells, because there is one run and one elapsed -- a
// fill per member would be the same number drawn three times, which says
// three independent things are progressing when one is. The jobs beneath say
// what that run is made of, and they finish at very different times.
//
// This replaces three bars on one line -- membership, checks and time -- that
// overflowed 76 columns and clipped the elapsed and the median, which is the
// one number a person waits on.

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// ciBlock is the whole foot: rule, chain, jobs, tally.
func ciBlock(w io.Writer, b *liveBatch, now time.Time, cols int) []string {
	verdict, right, ratio := ciVerdict(b, now)
	out := []string{ciRule(w, b, verdict, right, cols)}
	if line := chain(w, b, ratio, cols); line != "" {
		out = append(out, line)
	}
	out = append(out, jobRows(w, b, now, cols)...)
	return out
}

// ciVerdict is the word on the rule, what sits at its right end, and how far
// the fill has swept. Per phase, because the question changes: how far along
// while testing, how many landed once done.
func ciVerdict(b *liveBatch, now time.Time) (verdict, right string, ratio float64) {
	switch b.phase {
	case BatchAssembling:
		in := 0
		for _, m := range b.members {
			if m.state == MemberMerged {
				in++
			}
		}
		return "not started", fmt.Sprintf("%d of %d members assembled", in, len(b.members)), 0
	case BatchTesting:
		elapsed := time.Duration(0)
		if !b.ciStarted.IsZero() {
			elapsed = now.Sub(b.ciStarted)
		}
		if b.median <= 0 {
			return "running", elapsedString(elapsed) + " " + liveSep + " no baseline yet", 0
		}
		ratio = float64(elapsed) / float64(b.median)
		if ratio > 1 {
			// Past the median the fill stops rather than creeping toward a
			// finish nothing can predict, and the rule says so.
			return "running long", fmt.Sprintf("%s %s median %s", elapsedString(elapsed), liveSep, coarse(b.median)), 1
		}
		return "running", fmt.Sprintf("%s / ~%s median", elapsedString(elapsed), coarse(b.median)), ratio
	case BatchIsolating:
		return "red " + liveSep + " isolating",
			fmt.Sprintf("run %d of ~%d", b.runs, isolationRuns(len(b.members))), 1
	}
	n := b.countStates()
	var parts []string
	if n[MemberLanded] > 0 {
		parts = append(parts, fmt.Sprintf("%d landed", n[MemberLanded]))
	}
	if n[MemberCulprit] > 0 {
		parts = append(parts, fmt.Sprintf("%d culprit", n[MemberCulprit]))
	}
	if len(parts) == 0 {
		parts = []string{"landed nothing"}
	}
	runs := "runs"
	if b.runs == 1 {
		runs = "run"
	}
	right = fmt.Sprintf("%d %s", b.runs, runs)
	if !b.ciStarted.IsZero() {
		right += " " + liveSep + " " + coarse(now.Sub(b.ciStarted))
	}
	return strings.Join(parts, " "+liveSep+" "), right, 1
}

// ciRule is the titled rule: `── CI ─── running ──────── 4m12s / ~11m median ──`.
func ciRule(w io.Writer, b *liveBatch, verdict, right string, cols int) string {
	h := liveRuleGlyph
	if !glyphs() {
		h = liveRuleASCII
	}
	width := liveRuleWidth
	if cols > 0 && cols-1 < width {
		width = cols - 1
	}
	colour := cyan
	switch {
	case b.phase == BatchAssembling:
		colour = dim
	case strings.HasPrefix(verdict, "red") || b.countStates()[MemberCulprit] > 0:
		colour = red
	case b.phase == BatchDone:
		colour = green
	}
	left := "  " + h + h + " CI " + h + h + h + " "
	tail := " " + right + " " + h + h
	fill := width - displayCells(left) - displayCells(verdict) - 1 - displayCells(tail)
	if fill < 3 {
		fill = 3
	}
	return Dim(w, left) + paint(w, colour, verdict) + " " +
		Dim(w, strings.Repeat(h, fill)) + Dim(w, tail)
}

// chain is the members riding on this build, one cell each, with one fill.
//
// Ejected and pending members are NOT in it: absence from the chain is the
// clearest statement available that a branch is not part of this run.
func chain(w io.Writer, b *liveBatch, ratio float64, cols int) string {
	var riding []batchMember
	for _, m := range b.members {
		switch m.state {
		case MemberEjected:
			continue
		case MemberPending:
			// Not yet in the ref while assembling; once a run has started,
			// everything that was not ejected is riding on it, whether or
			// not its merge was reported.
			if b.phase == BatchAssembling {
				continue
			}
		}
		riding = append(riding, m)
	}
	if len(riding) == 0 {
		return ""
	}
	heavy, light, lend, join, rend := "━", "─", "┝", "┿", "┥"
	if !glyphs() {
		heavy, light, lend, join, rend = "=", "-", "[", "+", "]"
	}
	width := liveRuleWidth
	if cols > 0 && cols-1 < width {
		width = cols - 1
	}
	// The cells share the rule's width evenly. Labels are the key, then the
	// bare number, then nothing, as the width allows -- every cell alike, so
	// a bar naming two of three members never reads as a rendering fault.
	avail := width - 2 - 2 - (len(riding) - 1)
	cell := avail / len(riding)
	label := func(m batchMember) string { return m.key }
	need := 0
	for _, m := range riding {
		if n := displayCells(m.key) + 6; n > need {
			need = n
		}
	}
	if cell < need {
		label = func(m batchMember) string { return shortKey(m.key) }
		need = 0
		for _, m := range riding {
			if n := displayCells(shortKey(m.key)) + 6; n > need {
				need = n
			}
		}
	}
	if cell < need {
		label = func(batchMember) string { return "" }
	}
	if cell < 2 {
		cell = 2
	}
	// Fill positions are the rule glyphs only -- not the labels, not the
	// joints -- so the sweep is a fraction of the bar's own length.
	positions := 0
	for _, m := range riding {
		positions += cell - displayCells(label(m))
		if label(m) != "" {
			positions -= 2 // the spaces around the label
		}
	}
	filled := int(ratio*float64(positions) + 0.5)
	if filled > positions {
		filled = positions
	}
	if filled < 0 {
		filled = 0
	}

	var out strings.Builder
	out.WriteString("  " + Dim(w, lend))
	pos := 0
	seg := func(n int, colour string) string {
		var s strings.Builder
		for i := 0; i < n; i++ {
			if pos < filled {
				s.WriteString(paint(w, colour, heavy))
			} else {
				s.WriteString(Dim(w, light))
			}
			pos++
		}
		return s.String()
	}
	for i, m := range riding {
		if i > 0 {
			out.WriteString(Dim(w, join))
		}
		// CYAN while the run is in flight and every cell fills alike; only
		// once a verdict exists do cells differ. Nothing is coloured on
		// prediction.
		colour := cyan
		switch {
		case m.state == MemberCulprit:
			colour = red
		case b.phase == BatchDone || b.phase == BatchIsolating:
			if m.state == MemberLanded || m.state == MemberMerged {
				colour = green
			}
		}
		l := label(m)
		if l == "" {
			out.WriteString(seg(cell, colour))
			continue
		}
		room := cell - displayCells(l) - 2
		left := room / 2
		out.WriteString(seg(left, colour))
		out.WriteString(" " + paint(w, colour, l) + " ")
		out.WriteString(seg(room-left, colour))
	}
	out.WriteString(Dim(w, rend))
	return out.String()
}

// jobsPerRow is how many checks share a line; jobCell is each one's width.
const (
	jobsPerRow = 3
	// Eighteen fits "analyze actions", the longest job name this project
	// has, with the glyph and a gap; three of them sit inside 76 columns.
	jobCell = 18
)

// jobRows is the jobs three per line with a glyph each, then the tally.
//
// On a terminal too narrow for three cells the list collapses to the tally
// alone with the failing job named in full, because that is the one a reader
// needs.
func jobRows(w io.Writer, b *liveBatch, now time.Time, cols int) []string {
	checks := b.checks
	if len(checks) == 0 {
		switch b.phase {
		case BatchAssembling:
			return []string{"     " + Dim(w, "no build yet; the push starts one")}
		case BatchTesting:
			return []string{"     " + Dim(w, "no checks reported yet")}
		}
		return nil
	}
	var out []string
	if cols <= 0 || cols >= 5+jobsPerRow*jobCell {
		var row []string
		for i, c := range checks {
			row = append(row, jobCellText(w, c, now))
			if len(row) == jobsPerRow || i == len(checks)-1 {
				out = append(out, strings.TrimRight("     "+strings.Join(row, ""), " "))
				row = nil
			}
		}
	}
	return append(out, "     "+jobTally(w, b, checks))
}

func jobCellText(w io.Writer, c Check, now time.Time) string {
	ascii := !glyphs()
	var mark string
	switch c.State {
	case CheckFailed:
		g := "✗"
		if ascii {
			g = "x"
		}
		mark = paint(w, red, g)
	case CheckRunning:
		mark = paint(w, cyan, spinner(now))
	default:
		g := "✓"
		if ascii {
			g = "+"
		}
		mark = paint(w, green, g)
	}
	return mark + " " + pad(clip(shortJob(c.Name), jobCell-3), jobCell-2)
}

// jobTally is the one line that survives a narrow terminal:
// `4 of 6 green · waiting on 2`, or the failing job named.
func jobTally(w io.Writer, b *liveBatch, checks []Check) string {
	green_, red_, running := 0, 0, 0
	var failed []string
	for _, c := range checks {
		switch c.State {
		case CheckFailed:
			red_++
			failed = append(failed, shortJob(c.Name))
		case CheckRunning:
			running++
		default:
			green_++
		}
	}
	n := len(checks)
	switch {
	case red_ > 0:
		s := paint(w, red, fmt.Sprintf("%d of %d red", red_, n)) + " " + liveSep + " " + strings.Join(failed, ", ")
		// The test the culprit's own row named, so the line beneath the jobs
		// says which test rather than only which platform.
		for _, m := range b.members {
			if m.state == MemberCulprit && m.detail != "" {
				s += "\n     " + Dim(w, m.detail)
				break
			}
		}
		return s
	case running > 0:
		return Dim(w, fmt.Sprintf("%d of %d green %s waiting on %d", green_, n, liveSep, running))
	}
	return paint(w, green, fmt.Sprintf("%d of %d green", n, n))
}

// shortJob is a check's name as a person says it: `go (ubuntu-latest)` is
// `go ubuntu`. The brackets and `-latest` are the runner's grammar, not the
// job's name.
func shortJob(name string) string {
	name = strings.NewReplacer("(", "", ")", "", "-latest", "").Replace(name)
	return strings.Join(strings.Fields(name), " ")
}
