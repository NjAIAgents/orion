package supervisor

import (
	"regexp"
	"strings"
	"sync"
	"testing"
)

// Four more OR-335 requirements not covered by fan_readable_test.go,
// fan_or335_test.go or fan_landing_test.go: a landing names how long its
// child ran, two fans dispatched at once for different tickets can still be
// told apart, and About is clipped and flattened before it ever reaches a
// log line.

// TestFanLandingLineShowsItsDuration proves a landing names how long its
// child ran ("in Xm Ys"), not merely whether it succeeded -- without it
// nothing says why one child took three times as long as another sharing
// the same roster.
func TestFanLandingLineShowsItsDuration(t *testing.T) {
	// A lone job never announces (announce := len(jobs) > 1 in Fan) -- that is
	// by design, since one child is not a fan-out -- so this needs two jobs to
	// produce any narration to assert on at all.
	fakeClaudeTree(t, "sleep 1\necho '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	Fan(ws(t, ""), []Options{
		{Stage: "qa", Prompt: "x", MaxMinutes: 1, MaxTurns: 1, Actor: "qa",
			Model: "sonnet", Key: "OR-335", About: "1 case(s)"},
		{Stage: "qa", Prompt: "y", MaxMinutes: 1, MaxTurns: 1, Actor: "qa",
			Model: "sonnet", Key: "OR-335", About: "2 case(s)"},
	})

	got := out.String()
	if !regexp.MustCompile(`in (\d+m)?\d+s`).MatchString(got) {
		t.Errorf("landing carries no duration in the \"in Xm Ys\" shape: %q", got)
	}
}

// TestFanConcurrentFansFromDifferentTicketsAreDistinguishable is OR-335's
// stated failure mode: at a concurrency of four, two fans running at once
// were indistinguishable because neither carried a ticket key or a
// timestamp. Two Fan calls for two different tickets run concurrently here,
// sharing the same captured writer the way two real fans would share the
// same log, and every line must still be attributable to exactly one
// ticket.
func TestFanConcurrentFansFromDifferentTicketsAreDistinguishable(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	jobsFor := func(key string) []Options {
		var jobs []Options
		for i := 0; i < 2; i++ {
			jobs = append(jobs, Options{Stage: "qa", Prompt: "x", MaxMinutes: 1, MaxTurns: 1,
				Actor: "qa", Model: "sonnet", Key: key})
		}
		return jobs
	}

	var wg sync.WaitGroup
	for _, key := range []string{"OR-100", "OR-200"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			Fan(ws(t, ""), jobsFor(key))
		}(key)
	}
	wg.Wait()

	got := out.String()
	if !strings.Contains(got, "OR-100") || !strings.Contains(got, "OR-200") {
		t.Fatalf("both tickets' fans did not both narrate: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if line == "" {
			continue
		}
		hasA, hasB := strings.Contains(line, "OR-100"), strings.Contains(line, "OR-200")
		if hasA == hasB {
			t.Errorf("line names both tickets or neither, so it cannot be attributed to "+
				"one fan: %q", line)
		}
		if !strings.Contains(line, ":") {
			t.Errorf("line carries no timestamp to tell one fan's moment from the other's: %q", line)
		}
	}
}

// TestAboutOfClipsToMaxFortyEightCharsWithEllipsis: a caller passing a whole
// question or file list must not push the rest of the log line off the
// screen.
func TestAboutOfClipsToMaxFortyEightCharsWithEllipsis(t *testing.T) {
	long := strings.Repeat("a", 60)

	got := aboutOf(Options{About: long})

	if r := []rune(got); len(r) != 48 {
		t.Errorf("clipped About is %d runes, want 48: %q", len(r), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("clipped About does not end with an ellipsis: %q", got)
	}
	if want := strings.Repeat("a", 47) + "…"; got != want {
		t.Errorf("clipped About = %q, want %q", got, want)
	}
}

// TestAboutOfFlattensMultipleSpacesToSingle: a caller should not have to
// know it is writing a log line, so stray whitespace from a pasted question
// or a joined list must not survive into it.
func TestAboutOfFlattensMultipleSpacesToSingle(t *testing.T) {
	got := aboutOf(Options{About: "foo   bar\t\tbaz  \n qux"})

	if want := "foo bar baz qux"; got != want {
		t.Errorf("aboutOf(%q) = %q, want %q", "foo   bar\t\tbaz  \n qux", got, want)
	}
}
