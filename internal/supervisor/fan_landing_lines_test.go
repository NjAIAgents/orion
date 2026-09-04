//go:build !windows

package supervisor

import (
	"strings"
	"testing"
)

// Landing lines specifically (OR-335). fan_readable_test.go already checks
// every line and the whole indented block together; these isolate the
// landing half of that block on its own, since a landing is printed by a
// different function (announceLanded) than the roster (announceFan) and
// nothing guarantees the two stay in step just because they happen to today.

// TestFanLandingLinesAreIndentedLikeRosterLines. A landing that sat flush
// with its announcement while its roster line was indented would read as
// belonging to a different fan than the one that dispatched it.
func TestFanLandingLinesAreIndentedLikeRosterLines(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), fanJobs())

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	msg := strings.Index(lines[0], "fanning out")
	if msg < 0 {
		t.Fatalf("no announcement line to indent under: %q", lines[0])
	}
	parent := len([]rune(lines[0][:msg]))

	var landings int
	for _, line := range lines[1:] {
		if !strings.Contains(line, "/3 ") {
			continue // a roster line, not a landing
		}
		landings++
		r := []rune(line)
		if len(r) <= parent {
			t.Errorf("a landing line is shorter than the column its parent's message "+
				"starts in: %q", line)
			continue
		}
		at := strings.IndexFunc(string(r[parent:]), func(c rune) bool { return c != ' ' })
		if at != 2 {
			t.Errorf("a landing line's message starts %d columns in, want 2 -- the same "+
				"indent its roster line used: %q", at, line)
		}
	}
	if landings != 3 {
		t.Fatalf("expected 3 landing lines for 3 children, found %d in: %q", landings, out.String())
	}
}

// TestFanLandingLinesCarryTheTicketAndTimestamp. Every other line in the log
// carries its ticket key and a timestamp; a landing line is the one place a
// reader learns whether a specific child came back, so it cannot be the
// exception.
func TestFanLandingLinesCarryTheTicketAndTimestamp(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), fanJobs())

	var landings int
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.Contains(line, "/3 ") {
			continue // a roster line, not a landing
		}
		landings++
		if !strings.Contains(line, "OR-302") {
			t.Errorf("a landing line carries no ticket key: %q", line)
		}
		if !strings.Contains(line, ":") {
			t.Errorf("a landing line carries no timestamp: %q", line)
		}
	}
	if landings != 3 {
		t.Fatalf("expected 3 landing lines for 3 children, found %d in: %q", landings, out.String())
	}
}
