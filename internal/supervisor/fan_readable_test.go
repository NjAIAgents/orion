//go:build !windows

package supervisor

import (
	"strings"
	"testing"
)

// What a fan-out READS like (OR-335). OR-334 removed the live region, so this
// log is the interface now and its legibility is the whole surface.
//
// The faults these cover were all observed on one real watch: children flush
// against the same prefix as their parent, "5 children (cap 2)" read as a
// contradiction, five child lines identical but for an index, "exit 0" saying
// in machine vocabulary what "ok" already said, and no timestamp or ticket key
// on any of them -- so two fans running at once (the normal case at a
// concurrency of four) could not be told apart.

// fanJobs is three children of one ticket, each given something different.
func fanJobs() []Options {
	jobs := make([]Options, 0, 3)
	for _, about := range []string{"12 case(s)", "13 case(s)", "11 case(s)"} {
		jobs = append(jobs, Options{Stage: "qa", Prompt: "x", MaxMinutes: 1, MaxTurns: 1,
			Actor: "qa", Model: "sonnet", Key: "OR-302", About: about})
	}
	return jobs
}

func TestFanNarrationCarriesTheTicketAndTheTime(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), fanJobs())

	// Every line, not merely the first: identity columns are suppressed when
	// they repeat, but the key and the clock are on all of them, which is what
	// tells one ticket's fan from another's when both are in flight.
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.Contains(line, "OR-302") {
			t.Errorf("a fan line carries no ticket key, so two fans at once cannot be "+
				"told apart: %q", line)
		}
		if !strings.Contains(line, ":") {
			t.Errorf("a fan line carries no timestamp, unlike every other line in the "+
				"log: %q", line)
		}
	}
}

func TestFanChildLinesAreIndentedUnderTheirAnnouncement(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), fanJobs())

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("a fan of three printed %d lines: %q", len(lines), out.String())
	}
	// The announcement is the parent; everything under it is a child and says
	// so by sitting one level in. The identity columns are a fixed width, so
	// where a message starts within its line is comparable between the two.
	// In RUNES, not bytes: the verb icons are multi-byte and differ in length
	// between a working line and an ok one, so a byte offset would compare two
	// different columns.
	msg := strings.Index(lines[0], "fanning out")
	if msg < 0 {
		t.Fatalf("no announcement line to indent under: %q", lines[0])
	}
	parent := len([]rune(lines[0][:msg]))
	for _, line := range lines[1:] {
		r := []rune(line)
		if len(r) <= parent {
			t.Errorf("a child line is shorter than the column its parent's message starts "+
				"in: %q", line)
			continue
		}
		at := strings.IndexFunc(string(r[parent:]), func(c rune) bool { return c != ' ' })
		if at != 2 {
			t.Errorf("a child line's message starts %d columns in, want 2 -- flush with its "+
				"parent, a fan reads as a wall of siblings rather than a tree: %q", at, line)
		}
	}
}

func TestFanSaysHowManyRunAtOnceRatherThanACap(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, `{"limits":{"max_concurrent_children":2}}`), fanJobs())

	got := out.String()
	if !strings.Contains(got, "3 children, 2 at a time") {
		t.Errorf("the announcement does not say how many run at once: %q", got)
	}
	// "cap 2" over three children reads as a contradiction: the bound is on
	// concurrency, never on count, and the old line never said so.
	if strings.Contains(got, "cap") {
		t.Errorf("the fan still describes its concurrency as a cap: %q", got)
	}
}

func TestFanChildLinesAreDistinguishable(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), fanJobs())

	got := out.String()
	for _, about := range []string{"12 case(s)", "13 case(s)", "11 case(s)"} {
		if strings.Count(got, about) < 2 {
			t.Errorf("%q names no child on both its roster line and its landing, so "+
				"nothing says which child is which: %q", about, got)
		}
	}
}

func TestFanLandingSaysTheOutcomeOnce(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), fanJobs())

	got := out.String()
	// The verb column already says it worked. "exit 0" is the same fact in
	// machine vocabulary, on a line that has no room for either twice.
	if strings.Contains(got, "exit 0") {
		t.Errorf("a landing still reports exit 0 alongside the outcome word: %q", got)
	}
	if !strings.Contains(got, "ok") {
		t.Errorf("a successful landing never says it worked: %q", got)
	}
}
