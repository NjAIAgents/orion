package supervisor

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// criticalRe finds the one line the analyze prompt asks for: "Critical
// Issues Count: N". Case-insensitive, and tolerant of the markdown a report
// puts around the label -- a bullet, bold, a table cell -- but the gap
// between label and digits is bounded so a label with no count beside it
// cannot borrow a number from later in the report. The prompt writes the
// slot as <N>, which no digit matches, so the prompt can never pass its own
// gate if it is echoed.
var criticalRe = regexp.MustCompile(`(?i)critical issues count[^0-9\n]{0,12}(\d+)`)

// analyzeVerdict reads the critical-issue count from an analyze report.
// First match wins; no match is an error, because a report that never
// states the count is a report Orion cannot gate on, and the stage that
// produced it did not do what it was asked.
func analyzeVerdict(output string) (int, error) {
	m := criticalRe.FindStringSubmatch(output)
	if m == nil {
		return 0, errors.New("analyze: the report never states \"Critical Issues Count: N\", so there is no verdict to gate on")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("analyze: unreadable count %q", m[1])
	}
	return n, nil
}

// analyzeGate is the stage's pass/fail: nil for zero critical issues, an
// error naming the count and the findings otherwise. Fail closed: a
// missing verdict fails.
func analyzeGate(output string) error {
	n, err := analyzeVerdict(output)
	if err != nil {
		return err
	}
	if n > 0 {
		msg := fmt.Sprintf("analyze: %d critical issue(s) in the spec, plan or tasks", n)
		if rows := criticalRows(output); len(rows) > 0 {
			msg += ", each with what the report says would fix it:\n\n    " +
				strings.Join(rows, "\n\n    ")
		}
		return errors.New(msg)
	}
	return nil
}

// maxCriticalRows bounds what a block prints; the log has the rest.
const maxCriticalRows = 10

// criticalRows lists the CRITICAL rows of the report's findings table as a
// block per finding: the id and where it is, WHAT is wrong, and the
// report's own recommendation of what to do about it. The recommendation is
// the half that makes the block actionable -- a reader who has only the
// summary knows something is broken and still has to open a nine-minute log
// to learn what would fix it. Columns come from the table's own header when
// it has one, and from spec-kit's documented order otherwise.
func criticalRows(output string) []string {
	// The captured output is the CLI's stream-json, so the report's lines
	// arrive as \n escapes inside one JSON string; unescape before reading
	// it as lines. Harmless on already-plain text.
	output = strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\\`, `\`).Replace(output)
	idCol, sevCol, locCol, sumCol, recCol := 0, 2, 3, 4, 5
	var rows []string
	// The report reaches the stream twice -- as the assistant's text and
	// again in the CLI's final result event -- so a row is kept once by id.
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(t, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) > 2 && strings.EqualFold(cells[0], "ID") {
			for i, c := range cells {
				switch strings.ToLower(c) {
				case "severity":
					sevCol = i
				case "location(s)", "location":
					locCol = i
				case "summary":
					sumCol = i
				case "recommendation", "fix", "action":
					recCol = i
				}
			}
			continue
		}
		if sevCol >= len(cells) || !strings.EqualFold(strings.Trim(cells[sevCol], "*` "), "CRITICAL") {
			continue
		}
		get := func(i int) string {
			if i < len(cells) {
				return cells[i]
			}
			return ""
		}
		id := strings.Trim(get(idCol), "*` ")
		if seen[id] {
			continue
		}
		seen[id] = true
		clean := func(i int) string { return strings.TrimSpace(strings.ReplaceAll(get(i), "`", "")) }
		row := id
		if loc := clipTo(clean(locCol), 120); loc != "" {
			row += "  " + loc
		}
		if sum := clipTo(clean(sumCol), 400); sum != "" {
			row += "\n      " + sum
		}
		// What to do about it, in the report's own words.
		if rec := clipTo(clean(recCol), 400); rec != "" {
			row += "\n      fix: " + rec
		}
		rows = append(rows, row)
		if len(rows) == maxCriticalRows {
			break
		}
	}
	return rows
}

// clipTo shortens at a word boundary, so a clipped sentence ends on a word
// rather than mid-syllable.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n-1]
	if i := strings.LastIndexAny(cut, " ,;"); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;") + "…"
}
