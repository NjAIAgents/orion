package supervisor

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
// error naming the count otherwise. Fail closed: a missing verdict fails.
func analyzeGate(output string) error {
	n, err := analyzeVerdict(output)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("analyze: %d critical issue(s) in the spec, plan or tasks", n)
	}
	return nil
}
