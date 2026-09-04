package supervisor

import (
	"strings"
	"testing"
)

// OR-335's own four requirements, checked directly rather than folded into
// the broader readability tests: the announcement line must carry every
// identity marker every other log line carries plus the fleet shape in its
// non-contradictory wording, and every roster child line must be indented
// under it and carry the same identity markers.

func or335Jobs() []Options {
	return []Options{
		{Stage: "qa", Prompt: "x", MaxMinutes: 1, MaxTurns: 1, Actor: "qa", Model: "sonnet", Key: "OR-335"},
		{Stage: "qa", Prompt: "y", MaxMinutes: 1, MaxTurns: 1, Actor: "qa", Model: "sonnet", Key: "OR-335"},
		{Stage: "qa", Prompt: "z", MaxMinutes: 1, MaxTurns: 1, Actor: "qa", Model: "sonnet", Key: "OR-335"},
	}
}

func TestFanAnnouncementLineHasKeyTimestampCountAndModels(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, `{"limits":{"max_concurrent_children":2}}`), or335Jobs())

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 {
		t.Fatal("Fan announced nothing at all")
	}
	first := lines[0]
	if !strings.Contains(first, "OR-335") {
		t.Errorf("announcement carries no ticket key: %q", first)
	}
	if !strings.Contains(first, ":") {
		t.Errorf("announcement carries no timestamp: %q", first)
	}
	if !strings.Contains(first, "3 children, 2 at a time") {
		t.Errorf("announcement does not say the fleet size and how many run at once: %q", first)
	}
	if !strings.Contains(first, "sonnet") {
		t.Errorf("announcement carries no model list: %q", first)
	}
}

func TestFanAnnouncementNeverSaysCap(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, `{"limits":{"max_concurrent_children":2}}`), or335Jobs())

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 {
		t.Fatal("Fan announced nothing at all")
	}
	if strings.Contains(lines[0], "cap") {
		t.Errorf("announcement still uses the word \"cap\": %q", lines[0])
	}
}

func TestFanRosterChildLinesIndentUnderAnnouncement(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), or335Jobs())

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 1+len(or335Jobs()) {
		t.Fatalf("want an announcement plus one roster line per child, got %d lines: %q",
			len(lines), out.String())
	}
	msgAt := strings.Index(lines[0], "fanning out")
	if msgAt < 0 {
		t.Fatalf("no announcement line to indent under: %q", lines[0])
	}
	parentCol := len([]rune(lines[0][:msgAt]))
	roster := lines[1 : 1+len(or335Jobs())]
	for _, line := range roster {
		r := []rune(line)
		if len(r) <= parentCol {
			t.Errorf("roster line does not reach its parent's message column: %q", line)
			continue
		}
		indent := strings.IndexFunc(string(r[parentCol:]), func(c rune) bool { return c != ' ' })
		if indent != 2 {
			t.Errorf("roster line indents %d columns under its announcement, want 2: %q", indent, line)
		}
	}
}

func TestFanRosterChildLinesHaveKeyAndTimestamp(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), or335Jobs())

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 1+len(or335Jobs()) {
		t.Fatalf("want an announcement plus one roster line per child, got %d lines: %q",
			len(lines), out.String())
	}
	roster := lines[1 : 1+len(or335Jobs())]
	for _, line := range roster {
		if !strings.Contains(line, "OR-335") {
			t.Errorf("roster child line carries no ticket key: %q", line)
		}
		if !strings.Contains(line, ":") {
			t.Errorf("roster child line carries no timestamp: %q", line)
		}
	}
}
