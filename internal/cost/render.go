package cost

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// ReadAll reads a workspace's event log and every rotated generation of it.
//
// Rotation is the reason this is not just events.Read: a ticket that ran for
// hours can push its own early runs into events.jsonl.1, and a cost report
// that silently started from wherever the current file happens to begin would
// undercount exactly the long expensive tickets the report exists for.
// Oldest first, so the runs come back in the order they happened.
func ReadAll(path string) []events.Event {
	var out []events.Event
	for n := events.MaxFiles - 1; n >= 1; n-- {
		if evs, err := events.Read(fmt.Sprintf("%s.%d", path, n)); err == nil {
			out = append(out, evs...)
		}
	}
	if evs, err := events.Read(path); err == nil {
		out = append(out, evs...)
	}
	return out
}

// Render turns a report into the text posted to the tracker AND printed to
// the console.
//
// ONE renderer, two sinks, deliberately. Two formatters for the same numbers
// drift -- one grows a column the other does not, and then the terminal and
// the ticket disagree about what a ticket cost, with nothing to say which is
// right.
//
// Plain text rather than tracker markup, because the console is the other
// sink and markup there is noise.
//
// Bounded top and bottom by ui.BlockStart/BlockEnd. A table is not a status
// line, and in a concurrent watch log an unbounded one ran straight into
// whatever printed either side of it (OR-219). The boundary carries its words
// in text and degrades to ASCII, so it survives both sinks and a non-UTF-8
// terminal -- see internal/ui/block.go.
func Render(r Report) string {
	title := "cost report " + r.Key
	var b strings.Builder
	b.WriteString(ui.BlockStart(title) + "\n\n")

	if r.Empty() {
		b.WriteString("No per-run usage was recorded for this ticket, so Orion cannot say " +
			"what it cost. That means the runs predate usage recording, or the event log " +
			"they were written to is gone.\n")
		b.WriteString("\n" + ui.BlockEnd(title) + "\n")
		return b.String()
	}

	head := []string{"actor", "runs", "turns", "in", "out", "cache w", "cache r", "wall", "est. cost"}
	rows := [][]string{head}
	for _, row := range r.Rows {
		rows = append(rows, cells(row))
	}
	rows = append(rows, cells(r.Total))
	writeTable(&b, rows)

	// Per run, because "how long did it take" always follows "what did it
	// cost", and because a failed run has to be visible as a run rather than
	// as an unexplained line in an actor's total.
	b.WriteString("\nruns\n")
	for i, run := range r.Runs {
		fmt.Fprintf(&b, "  %d. %s %s  %s  %s  %s\n", i+1,
			padRight(displayOf(actors.Display(run.Actor)), 28),
			padLeft(plural(run.Turns, "turn"), 9),
			padLeft(dur(run.Seconds), 8), padLeft(usd(run.CostUSD), 8), status(run))
	}

	fmt.Fprintf(&b, "\nwall time %s across %s. %s\n",
		dur(r.Total.Seconds), plural(r.Total.Runs, "run"), provenance(r))

	// Said before the floor warning, because it SUBTRACTS from the run count
	// the reader has just been given. A run that never opened a session is
	// not work the ticket attempted and failed at; leaving it in the total
	// makes both the run count and the failure count read high.
	if n := r.Total.NeverStarted; n > 0 {
		fmt.Fprintf(&b, "\n%s never started -- the runner exited before opening a session, "+
			"so nothing was attempted and nothing was spent. Counted apart from the failed "+
			"runs, and %s of genuine work above.\n",
			plural(n, "run"), plural(r.Total.Runs-n, "run"))
	}

	if r.Total.Missing > 0 {
		fmt.Fprintf(&b, "\nUsage is missing for %s, so every number above is a FLOOR "+
			"rather than the total: the ticket cost at least this much.\n",
			plural(r.Total.Missing, "run"))
	}
	b.WriteString("\n" + ui.BlockEnd(title) + "\n")
	return b.String()
}

// provenance says where the money figure came from. Always said, because
// "$4.25" with no source reads as authoritative whichever way it was derived,
// and only the runner's own per-session figure is worth that reading.
func provenance(r Report) string {
	if r.Total.CostUSD > 0 {
		return "Costs are estimates, as reported by the runner per session."
	}
	return "The runner reported no cost for these runs, so no price is shown."
}

func cells(row Row) []string {
	runs := strconv.Itoa(row.Runs)
	// Both annotations, separately, never merged into one "n bad". They are
	// different facts about different money: a failed run spent what it spent
	// before it died, a never-started one spent nothing at all.
	var notes []string
	if row.Failed > 0 {
		notes = append(notes, fmt.Sprintf("%d failed", row.Failed))
	}
	if row.NeverStarted > 0 {
		notes = append(notes, fmt.Sprintf("%d never started", row.NeverStarted))
	}
	if len(notes) > 0 {
		runs += " (" + strings.Join(notes, ", ") + ")"
	}
	return []string{
		displayOf(row.Actor), runs, commas(row.Turns), commas(row.Prompt),
		commas(row.Output), commas(row.CacheW), commas(row.CacheR),
		dur(row.Seconds), usd(row.CostUSD),
	}
}

func displayOf(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func status(r Run) string {
	switch {
	// Checked FIRST. Such a run is also failed and also has no usage, and
	// either of those words in this column would be the misreading the row
	// exists to prevent.
	case r.NeverStarted && r.Reason != "":
		return "never started (" + r.Reason + ")"
	case r.NeverStarted:
		return "never started"
	case !r.HaveUsage && r.Reason != "":
		return "usage missing (" + r.Reason + ")"
	case !r.HaveUsage:
		return "usage missing"
	case r.Failed && r.Reason != "":
		return "failed: " + r.Reason
	case r.Failed:
		return "failed"
	}
	return "ok"
}

// writeTable pads every column to its widest cell. Computed rather than
// fixed: an actor's display name is user-configurable, so a hardcoded width
// would either waste half the line or ragged-wrap somebody's roster.
//
// Padded by RUNE count, not by byte count. The separator between a name and
// its job title is a multi-byte middle dot and the placeholder for "no cost
// reported" is an em dash, so byte widths put every column after the first
// one out by two or three spaces -- in a table whose only job is to line
// numbers up under each other.
func writeTable(b *strings.Builder, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	width := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if i < len(width) && runes(c) > width[i] {
				width[i] = runes(c)
			}
		}
	}
	for _, r := range rows {
		var line strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				line.WriteString(c)
				break
			}
			line.WriteString(c + strings.Repeat(" ", width[i]-runes(c)+2))
		}
		b.WriteString(strings.TrimRight(line.String(), " ") + "\n")
	}
}

func runes(s string) int { return utf8.RuneCountInString(s) }

func padLeft(s string, n int) string {
	if runes(s) >= n {
		return s
	}
	return strings.Repeat(" ", n-runes(s)) + s
}

func padRight(s string, n int) string {
	if runes(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-runes(s))
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return commas(n) + " " + unit + "s"
}

// commas groups thousands. Token counts run to seven figures, and 1203554 is
// a number a reader has to count digits on.
func commas(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func usd(v float64) string {
	if v == 0 {
		return "—"
	}
	return fmt.Sprintf("$%.2f", v)
}

// dur formats seconds the way a person says them: 14m 12s, 1h 02m.
func dur(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second)).Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
