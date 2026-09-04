package supervisor

import (
	"fmt"
	"strings"
	"testing"
)

// Three more OR-335 requirements: a child with no About must not grow a
// dangling separator, and a fan's landings must be complete and unique --
// every child accounted for, none counted twice.

// TestLabelOfWithNoAboutIsPositionAndActorOnly. aboutOf appends " · <about>"
// only when About is non-empty; a child that never set it (most callers,
// before OR-335's three fan sites started setting it) must fall back to the
// bare "#N actor" label rather than a label with a trailing separator and
// nothing after it.
func TestLabelOfWithNoAboutIsPositionAndActorOnly(t *testing.T) {
	got := labelOf(0, Options{Actor: "qa"})
	if got != "#1 qa" {
		t.Errorf("labelOf with no About = %q, want %q", got, "#1 qa")
	}
	if strings.Contains(got, "·") {
		t.Errorf("labelOf with no About carries a separator with nothing after it: %q", got)
	}
}

// TestFanAllChildrenEventuallyLand: a fan of five must produce five landing
// lines once it returns -- Fan already guarantees this for FanResult (proven
// by TestFanReturnsResultsInInputOrder), but the narration is a separate code
// path (announceLanded, under its own mutex) and nothing ties the two
// together except this behaviour.
func TestFanAllChildrenEventuallyLand(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	const n = 5
	var jobs []Options
	for i := 0; i < n; i++ {
		jobs = append(jobs, Options{Stage: "qa", Prompt: fmt.Sprintf("q%d", i),
			MaxMinutes: 1, MaxTurns: 1, Actor: "qa", Key: "OR-335"})
	}
	Fan(ws(t, ""), jobs)

	got := out.String()
	for i := 1; i <= n; i++ {
		want := fmt.Sprintf("%d/%d", i, n)
		if !strings.Contains(got, want) {
			t.Errorf("no landing line reached count %s -- a child never landed: %q", want, got)
		}
	}
}

// TestFanNoChildLandsTwice: the running "landed" counter is incremented under
// a lock once per goroutine, so every count from 1/N to N/N must appear
// EXACTLY once -- a duplicate would mean two goroutines both announced the
// same landing slot, double-reporting one child or losing another.
func TestFanNoChildLandsTwice(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	const n = 5
	var jobs []Options
	for i := 0; i < n; i++ {
		jobs = append(jobs, Options{Stage: "qa", Prompt: fmt.Sprintf("q%d", i),
			MaxMinutes: 1, MaxTurns: 1, Actor: "qa", Key: "OR-335"})
	}
	Fan(ws(t, ""), jobs)

	got := out.String()
	for i := 1; i <= n; i++ {
		want := fmt.Sprintf("%d/%d", i, n)
		if c := strings.Count(got, want); c != 1 {
			t.Errorf("landing count %s appears %d times, want exactly 1: %q", want, c, got)
		}
	}
}
