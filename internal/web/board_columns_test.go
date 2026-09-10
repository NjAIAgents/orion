package web

// OR-273: coverage for four specific scenarios in the board's column
// derivation. Two of the four are written below; the other two are recorded
// here rather than duplicated, because writing them again would not add
// coverage:
//
//   - "State machine with only queued state still derives correctly to
//     board" cannot be tested as written. tracker.QueueStates has no
//     parameter for which states exist -- it always returns the five states
//     declared in internal/tracker/issues.go -- and Columns takes no state
//     list of its own to substitute one. Testing "only queued state" would
//     require changing the implementation to accept an injected state list,
//     which this task is explicitly not to do.
//
//   - "QueueStates with empty label and QueueStates with QueueLabelDefault
//     return identical results" is already asserted twice: fully, in
//     internal/tracker/issues_test.go's TestQueueStatesFallBackToTheDefaultLabel,
//     and again at the board level in board_test.go's
//     TestAnUnresolvedQueueLabelFallsBackToTheDefault. A third copy here
//     would restate the same comparison against the same function.

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// With today's state machine, the board draws one column per state and every
// column carries both a name and a label -- nothing renders blank.
func TestBoardRendersAllFiveStates(t *testing.T) {
	const queueLabel = "FIVE-STATE-LABEL"

	got := Columns(queueLabel)
	if len(got) != 5 {
		t.Fatalf("Columns() = %+v, want 5 columns for today's state machine", got)
	}

	wantNames := map[string]bool{
		"queued": false, "working": false, "ci-wait": false, "ready": false, "failed": false,
	}
	for _, c := range got {
		if c.Name == "" {
			t.Errorf("column %+v has no name", c)
		}
		if c.Label == "" {
			t.Errorf("column %+v has no label", c)
		}
		if _, known := wantNames[c.Name]; !known {
			t.Errorf("column named %q is not one of the five states the board is expected to draw: %+v", c.Name, got)
			continue
		}
		wantNames[c.Name] = true
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("state %q produced no column: %+v", name, got)
		}
	}
}

// Columns is a passthrough, so the state NAMES it reports for each position
// have to be exactly the names tracker.QueueStates reports for that same
// position -- not a web-side relabeling, not a subset, not a reordering.
func TestColumnNamesMatchQueueStateNames(t *testing.T) {
	const queueLabel = "NAME-MATCH-LABEL"

	columns := Columns(queueLabel)
	states := tracker.QueueStates(queueLabel)

	if len(columns) != len(states) {
		t.Fatalf("Columns() has %d entries, QueueStates() has %d: %+v vs %+v",
			len(columns), len(states), columns, states)
	}
	for i := range states {
		if columns[i].Name != states[i].Name {
			t.Errorf("column %d is named %q, tracker.QueueStates names it %q", i, columns[i].Name, states[i].Name)
		}
	}
}
