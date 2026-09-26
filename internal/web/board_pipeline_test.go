package web

// OR-273: the four scenarios the board's column derivation exists to
// satisfy -- a state added, removed, or reordered in the label state
// machine reaches the board with no edit to this package, and today's
// concrete machine draws exactly five columns in pipeline order.
//
// Columns(queueLabel) is `return tracker.QueueStates(queueLabel)` and
// nothing else (see board.go) -- no length constant, no allowlist, no
// sort. That is what makes add/remove/reorder free: a passthrough has no
// place to hold a stale count or a stale order, so whatever
// tracker.QueueStates returns tomorrow is what Columns returns tomorrow.
// The add/remove/reorder tests below confirm the function still has that
// shape -- the same technique TestNoQueueStateIsDeclaredInTheWebPackage
// already uses to read board.go's own source, because a runtime
// comparison against today's five states cannot, by itself, distinguish
// "derives from the machine" from "happens to match the machine right
// now."

import (
	"os"
	"regexp"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

func columnsFuncBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("board.go")
	if err != nil {
		t.Fatalf("read board.go: %v", err)
	}
	m := regexp.MustCompile(`(?s)func Columns\(queueLabel string\) \[\]tracker\.QueueState \{(.*?)\n\}`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("board.go has no Columns(queueLabel string) []tracker.QueueState function to inspect")
	}
	return m[1]
}

// Board displays five columns in pipeline order: queued, working, ci-wait,
// ready, failed -- today's concrete shape of the machine, spelled out here
// (in a test, not in board.go) so a regression that reordered or dropped one
// is caught even though board.go itself never names them.
func TestBoardDisplaysFiveColumnsInPipelineOrder(t *testing.T) {
	const queueLabel = "PIPELINE-ORDER-LABEL"
	want := []tracker.QueueState{
		{Name: "queued", Label: queueLabel},
		{Name: "working", Label: tracker.LabelWorking},
		{Name: "ci-wait", Label: tracker.LabelCIWait},
		{Name: "ready", Label: tracker.LabelReady},
		{Name: "failed", Label: tracker.LabelFailed},
	}

	got := Columns(queueLabel)
	if len(got) != len(want) {
		t.Fatalf("Columns() = %+v (%d columns), want %d: %+v", got, len(got), len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Adding a state to the state machine creates a new board column without web
// package changes. Columns has no fixed-size return (no array type, no
// length constant, no literal []tracker.QueueState of its own) -- it hands
// back whatever slice tracker.QueueStates produced, so a sixth element
// there is a sixth column here with nothing in this package to edit.
func TestAddingAStateToTheStateMachineCreatesANewColumnWithNoWebChange(t *testing.T) {
	body := columnsFuncBody(t)

	if regexp.MustCompile(`\[\d+\]tracker\.QueueState`).MatchString(body) {
		t.Fatalf("Columns has a fixed-size array return; a new state in the machine would not fit: %q", body)
	}
	if regexp.MustCompile(`\[\]tracker\.QueueState\{`).MatchString(body) {
		t.Fatalf("Columns declares its own []tracker.QueueState literal instead of returning the machine's: %q", body)
	}
	if !regexp.MustCompile(`return\s+tracker\.QueueStates\(queueLabel\)`).MatchString(body) {
		t.Fatalf("Columns no longer returns tracker.QueueStates(queueLabel) directly, so a new state added there is no longer guaranteed to reach the board: %q", body)
	}
}

// Removing a state from the state machine removes the board column without
// web package changes. The same passthrough that lets a new state through
// has no allowlist or leftover copy that could keep a removed state's
// column drawn after tracker.QueueStates stops returning it.
func TestRemovingAStateFromTheStateMachineRemovesTheColumnWithNoWebChange(t *testing.T) {
	body := columnsFuncBody(t)

	if regexp.MustCompile(`append\(`).MatchString(body) {
		t.Fatalf("Columns appends to the machine's result; that can retain an entry the machine no longer returns: %q", body)
	}
	if !regexp.MustCompile(`return\s+tracker\.QueueStates\(queueLabel\)`).MatchString(body) {
		t.Fatalf("Columns no longer returns tracker.QueueStates(queueLabel) directly, so a state removed there is not guaranteed to disappear from the board: %q", body)
	}
}

// Reordering states in the state machine reorders board columns to match.
// Columns does not sort or reindex the machine's slice -- it is the same
// slice, in the same order -- so the pipeline order is entirely
// tracker.QueueStates' to set.
func TestReorderingStatesInTheStateMachineReordersColumnsToMatch(t *testing.T) {
	body := columnsFuncBody(t)

	if regexp.MustCompile(`sort\.`).MatchString(body) {
		t.Fatalf("Columns sorts the machine's result; the board's order would stop being the machine's pipeline order: %q", body)
	}
	if !regexp.MustCompile(`return\s+tracker\.QueueStates\(queueLabel\)`).MatchString(body) {
		t.Fatalf("Columns no longer returns tracker.QueueStates(queueLabel) directly, so a reorder there is not guaranteed to reorder the board: %q", body)
	}

	// Cross-checked against the live machine: today's order is exactly
	// tracker.QueueStates' order, element for element, not a copy that
	// happens to agree right now.
	const queueLabel = "REORDER-CHECK-LABEL"
	want := tracker.QueueStates(queueLabel)
	got := Columns(queueLabel)
	if len(got) != len(want) {
		t.Fatalf("Columns() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d = %+v, want %+v -- the board's order must be the machine's order", i, got[i], want[i])
		}
	}
}
