package ui

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

// quiet is the default level, restored after a test that changes it. Every
// case here states the level it is asserting about rather than inheriting
// one, because the whole subject of OR-217 is what the two levels differ by.
func atLevel(t *testing.T, on bool) {
	t.Helper()
	SetVerbose(on)
	t.Cleanup(func() { SetVerbose(false) })
}

// ONE TICKET, DEFAULT LEVEL. The measured screen was ~50 lines for two
// tickets, ~30 of them tool calls. What has to survive the filter is
// everything that decides whether a person acts: the stage boundaries, the
// outcomes, and anything awaiting somebody.
func TestTheDefaultLevelKeepsTheSignalAndDropsTheTranscript(t *testing.T) {
	atLevel(t, false)
	var b bytes.Buffer

	Stage(&b, nil, Handoff{Key: "OR-217", From: "routing", To: "implementing",
		By: events.ActorOrion, Next: events.ActorImplementer})
	// The transcript: what 60% of the real screen was.
	for i := 0; i < 30; i++ {
		Trace(&b, "OR-217", events.ActorImplementer, "sonnet", VerbWorking, "ran git status %d", i)
	}
	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "2 commit(s) on orion/or-217")
	Say(&b, "OR-217", events.ActorOrion, VerbWaiting, "opened PR #7, awaiting CI")
	Say(&b, "OR-217", events.ActorOrion, VerbWarn, "budget checkpoint at 80 percent")
	Stage(&b, nil, Handoff{Key: "OR-217", From: "implementing", To: "qa",
		By: events.ActorImplementer, Next: events.ActorQA})
	Flush(&b)

	got := b.String()
	lines := strings.Count(strings.TrimRight(got, "\n"), "\n") + 1
	if lines > 15 {
		t.Errorf("default output is %d lines for one ticket, want on the order of 15:\n%s", lines, got)
	}
	for _, want := range []string{
		"routing", "implementing", "qa", // both stage boundaries
		"2 commit(s) on orion/or-217",     // the outcome
		"opened PR #7, awaiting CI",       // awaiting a machine
		"budget checkpoint at 80 percent", // awaiting a person
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the default level dropped %q, which a reader has to act on:\n%s", want, got)
		}
	}
	if strings.Contains(got, "git status") {
		t.Errorf("the tool-call transcript reached the default console:\n%s", got)
	}
}

// --verbose is the escape hatch: nothing is withheld from it. A level that
// quietly dropped a line at BOTH settings would leave no way to see it at
// all, which is the one thing this change must not do.
func TestVerboseWithholdsNothing(t *testing.T) {
	atLevel(t, true)
	var b bytes.Buffer

	for i := 0; i < 4; i++ {
		Trace(&b, "OR-217", events.ActorImplementer, "sonnet", VerbWorking, "ran git status %d", i)
	}
	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "2 commit(s)")
	Flush(&b)

	got := b.String()
	for i := 0; i < 4; i++ {
		if !strings.Contains(got, "git status "+string(rune('0'+i))) {
			t.Errorf("--verbose withheld tool call %d:\n%s", i, got)
		}
	}
}

// Thirty characters of identical name and model on twenty-five consecutive
// lines carried nothing after the first. The columns stay -- they are what
// keeps the layout a layout -- they are simply not re-stated.
func TestIdentityIsPrintedOnlyWhenItChanges(t *testing.T) {
	atLevel(t, false)
	var b bytes.Buffer
	who := actors.Display(events.ActorImplementer)

	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "first")
	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "second")
	Say(&b, "OR-217", events.ActorQA, VerbOK, "third")
	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "fourth")
	Flush(&b)

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %d:\n%s", len(lines), b.String())
	}
	if !strings.Contains(lines[0], who) {
		t.Errorf("the first line of a run must name its actor:\n%s", lines[0])
	}
	if strings.Contains(lines[1], who) {
		t.Errorf("a consecutive line from the same actor re-stated the identity:\n%s", lines[1])
	}
	// The first line after ANY change states it again, or the reader has to
	// scroll to find out who is acting.
	if !strings.Contains(lines[2], actors.Display(events.ActorQA)) {
		t.Errorf("the line after an actor change lost its identity:\n%s", lines[2])
	}
	if !strings.Contains(lines[3], who) {
		t.Errorf("the line after changing back lost its identity:\n%s", lines[3])
	}
	// Blanked, not dropped: the message still starts in the same column, or
	// the run of lines becomes a ragged wall instead of a table.
	// Counted in RUNES: the separator between a name and a job title is
	// multibyte, so a byte offset would differ on the very line the blanked
	// column has to match.
	if column(lines[0], "first") != column(lines[1], "second") {
		t.Errorf("the message column moved when the identity was blanked:\n%s\n%s", lines[0], lines[1])
	}
}

// column is where a substring starts, in terminal columns rather than bytes.
func column(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return utf8.RuneCountInString(line[:i])
}

// Two tickets interleaving is the case the identity rule must not break: a
// developer line on one ticket followed by a developer line on the other is
// not a continuation of anything.
func TestTwoTicketsInFlightEachStateTheirOwnIdentity(t *testing.T) {
	atLevel(t, false)
	var b bytes.Buffer
	who := actors.Display(events.ActorImplementer)

	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "one")
	Say(&b, "OR-220", events.ActorImplementer, VerbOK, "two")
	Flush(&b)

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), b.String())
	}
	for i, l := range lines {
		if !strings.Contains(l, who) {
			t.Errorf("line %d belongs to a different ticket and must name its actor:\n%s", i, l)
		}
	}
	if !strings.Contains(lines[0], "OR-217") || !strings.Contains(lines[1], "OR-220") {
		t.Errorf("each line must carry its own ticket key:\n%s", b.String())
	}
}

// "ran git status" three times in eight seconds. Four identical actions are
// one thing that happened four times, and cost four lines of a screen that
// had ten to spare.
func TestConsecutiveIdenticalActionsCollapseToACount(t *testing.T) {
	atLevel(t, true) // the transcript is where identical runs happen
	var b bytes.Buffer

	for i := 0; i < 4; i++ {
		Trace(&b, "OR-217", events.ActorImplementer, "sonnet", VerbWorking, "edited cmd/orion/aiops_test.go")
	}
	Flush(&b)

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("4 identical actions rendered as %d lines, want the action and its count:\n%s",
			len(lines), b.String())
	}
	if !strings.Contains(lines[1], "(x4)") {
		t.Errorf("the collapsed line does not say how many times it happened:\n%s", lines[1])
	}
	if !strings.Contains(lines[1], "edited cmd/orion/aiops_test.go") {
		t.Errorf("the collapsed line must still say what happened:\n%s", lines[1])
	}
}

// A different line ends the run and the count goes with it, in order. A
// count that arrived after the next thing happened would attach itself to
// the wrong line.
func TestACountIsPrintedBeforeTheLineThatBreaksTheRun(t *testing.T) {
	atLevel(t, false)
	var b bytes.Buffer

	Say(&b, "OR-217", events.ActorOrion, VerbWaiting, "waiting on CI")
	Say(&b, "OR-217", events.ActorOrion, VerbWaiting, "waiting on CI")
	Say(&b, "OR-217", events.ActorOrion, VerbOK, "checks pass")

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), b.String())
	}
	if !strings.Contains(lines[1], "(x2)") {
		t.Errorf("the count was not printed when the run ended:\n%s", b.String())
	}
	if !strings.Contains(lines[2], "checks pass") {
		t.Errorf("the count was printed after the line that broke the run:\n%s", b.String())
	}
}

// A boundary ends a run of lines: the count belongs above the rule, and the
// first line of the new stage names its actor again.
func TestAStageBoundaryEndsTheRunOfLines(t *testing.T) {
	atLevel(t, false)
	var b bytes.Buffer

	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "same")
	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "same")
	Stage(&b, nil, Handoff{Key: "OR-217", From: "implementing", To: "qa",
		By: events.ActorImplementer, Next: events.ActorQA})
	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "after")

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %d:\n%s", len(lines), b.String())
	}
	if !strings.Contains(lines[1], "(x2)") {
		t.Errorf("the repeat count was not flushed before the boundary:\n%s", b.String())
	}
	if !strings.Contains(lines[3], actors.Display(events.ActorImplementer)) {
		t.Errorf("the first line after a boundary must state its identity again:\n%s", lines[3])
	}
}

// The only useful token in a sandbox temp path is the file name, and it is
// the part the line clips away. The full path stays in the event log.
func TestALongAbsolutePathShortensToItsBaseName(t *testing.T) {
	long := "/private/tmp/claude-501/-Users-navjyotnishant--orion-projects-orion/bxpkzaome.output"
	if got := shortenPaths("ran cat " + long); got != "ran cat bxpkzaome.output" {
		t.Errorf("shortenPaths = %q, want the base name", got)
	}
	// Trailing punctuation is the sentence's, not the path's.
	if got := shortenPaths("left " + long + ": busy"); !strings.HasPrefix(got, "left bxpkzaome.output:") {
		t.Errorf("shortenPaths lost the sentence around the path: %q", got)
	}
	// The three things in these messages that look like a path and are not.
	for _, keep := range []string{
		"opened https://github.com/orion-sdlc/orion/pull/7",
		"branch orion/or-217-a-fairly-long-branch-name-that-goes-on",
		"edited cmd/orion/aiops_test.go",
		"read /etc/hosts",
	} {
		if got := shortenPaths(keep); got != keep {
			t.Errorf("shortenPaths cut something that was not a long absolute path:\n%q\n%q", keep, got)
		}
	}
}

// Inherited from OR-125/OR-163 and re-checked here because this change adds
// a second render path: whatever is printed still has to degrade.
func TestTheSuppressedIdentityColumnsDegrade(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	t.Setenv("LC_ALL", "C")
	atLevel(t, false)
	var b bytes.Buffer

	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "first")
	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "second")
	Say(&b, "OR-217", events.ActorImplementer, VerbOK, "second")
	Flush(&b)

	got := b.String()
	if strings.Contains(got, "\x1b[") {
		t.Errorf("escape codes survived NO_COLOR:\n%q", got)
	}
	for _, want := range []string{"OR-217", VerbOK, "first", "second", "(x2)"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q must be readable on a plain terminal:\n%s", want, got)
		}
	}
}

// Two destinations are two pages. Identity suppressed on one because of what
// was printed to the other would be a line missing its actor for a reason no
// reader of that page could see.
func TestASecondWriterStartsItsOwnPage(t *testing.T) {
	atLevel(t, false)
	var one, two bytes.Buffer

	Say(&one, "OR-217", events.ActorImplementer, VerbOK, "x")
	Say(&two, "OR-217", events.ActorImplementer, VerbOK, "x")
	Flush(&two)

	if !strings.Contains(two.String(), actors.Display(events.ActorImplementer)) {
		t.Errorf("the first line of a different writer lost its identity:\n%s", two.String())
	}
}
