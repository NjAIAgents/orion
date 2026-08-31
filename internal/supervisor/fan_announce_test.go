package supervisor

import (
	"strings"
	"testing"
)

// What a fan-out SAYS while it runs, per nj-agents CONVENTIONS-orchestration
// §C (cost shape before dispatch) and §R (roster before dispatch, each child
// marked as it lands).
//
// This stops being decoration the moment Fan has a real caller: OR-229 puts
// an implementer's exploration phase behind it, so several subagents now run
// where one used to, and a fan-out that says nothing is a silent gap ending
// in a wall of output -- from outside, indistinguishable from stuck.

// captureFanOut redirects the narration so a test can read what a dispatch
// actually said. Announcing is a behaviour of Fan, and a behaviour nothing
// observes is one that regresses silently.
func captureFanOut(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	old := fanOut
	fanOut = &b
	t.Cleanup(func() { fanOut = old })
	return &b
}

func TestFanAnnouncesTheCostShapeAndRosterBeforeDispatch(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	w := ws(t, `{"limits":{"max_concurrent_children":2}}`)
	var jobs []Options
	for i := 0; i < 3; i++ {
		jobs = append(jobs, Options{Stage: "explore", Prompt: "x", MaxMinutes: 1, MaxTurns: 1,
			Actor: "explore", Model: "haiku"})
	}
	Fan(w, jobs)

	got := out.String()
	if !strings.Contains(got, "3") || !strings.Contains(got, "cap 2") {
		t.Errorf("the cost shape never named the fleet size and its cap (§C): %q", got)
	}
	if !strings.Contains(got, "haiku") {
		t.Errorf("the cost shape never named the models the children run on (§C): %q", got)
	}
	// One roster line per child, so a reader sees WHAT is outstanding rather
	// than only how many things are.
	if n := strings.Count(got, "..."); n != 3 {
		t.Errorf("roster announced %d children, want one line per child (§R): %q", n, got)
	}
}

// §R's other half: mark each child AS IT LANDS, with its verdict and the
// running count. A roster with no landings says what was dispatched and never
// whether any of it came back, which is the state a reader cannot tell apart
// from stuck.
func TestFanMarksEachChildAsItLands(t *testing.T) {
	fakeClaudeTree(t, `for a in "$@"; do
  if [ "$a" = "FAIL" ]; then
    echo "boom" >&2
    exit 1
  fi
done
echo '`+fanResultJSON+`'
exit 0
`)
	out := captureFanOut(t)

	w := ws(t, "")
	Fan(w, []Options{
		{Stage: "explore", Prompt: "PASS", MaxMinutes: 1, MaxTurns: 1, Actor: "explore"},
		{Stage: "broken", Prompt: "FAIL", MaxMinutes: 1, MaxTurns: 1, Actor: "broken"},
	})

	got := out.String()
	if strings.Count(got, "1/2")+strings.Count(got, "2/2") != 2 {
		t.Errorf("landings are not counted against the total, so nothing ever says what is "+
			"still outstanding (§R): %q", got)
	}
	if !strings.Contains(got, "FAILED") {
		t.Errorf("the failing child landed without a failure verdict; a roster that reports "+
			"every child the same way reports nothing: %q", got)
	}
	// By name AND position. Five explores dispatched together share one name
	// and differ only in the question each was given, so a landing labelled
	// with the name alone cannot be matched back to a roster line.
	if !strings.Contains(got, "#2 broken") {
		t.Errorf("the landing line does not name WHICH child failed: %q", got)
	}
}
