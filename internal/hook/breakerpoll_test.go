package hook

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/state"
)

// Waiting is allowed (OR-207) but BOUNDED (OR-331). A QA session backgrounded
// the repository's suite and polled it for twenty minutes -- in a headless run,
// where a background command is never announced back -- and never produced the
// verdict that would have handed its ticket to the implementer.
func TestPollingWithNoProgressTripsTheBreaker(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	cfg.Limits.MaxConsecutivePolls = 12
	store := state.New(t.TempDir())

	post(store, cfg, "s", Input{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"./scripts/test.sh","run_in_background":true}`),
		ToolResponse: json.RawMessage(
			`{"stdout":"Running in background. Output: /tmp/claude/tasks/ab12.output","is_error":false}`),
	})

	var d Decision
	for i := 0; i < 20 && !d.Blocked(); i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "TaskOutput",
			ToolResponse: json.RawMessage(`{"stdout":"still running"}`),
		})
	}
	if !d.Blocked() {
		t.Fatal("twenty consecutive polls did not trip the breaker")
	}
	for _, want := range []string{"WAITING WITH NO PROGRESS", "HEADLESS", "FOREGROUND"} {
		if !strings.Contains(d.Msg, want) {
			t.Errorf("the message must say %q:\n%s", want, d.Msg)
		}
	}
}

// OR-207's case must still pass: eight polls of a not-yet-finished suite is
// waiting, not a stall. The budget sits above what a real wait costs.
func TestAShortWaitIsStillAllowed(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	cfg.Limits.MaxConsecutivePolls = 12
	store := state.New(t.TempDir())

	post(store, cfg, "s", Input{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"./scripts/test.sh","run_in_background":true}`),
		ToolResponse: json.RawMessage(
			`{"stdout":"Running in background. Output: /tmp/claude/tasks/ab12.output","is_error":false}`),
	})
	var d Decision
	for i := 0; i < 8; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "TaskOutput",
			ToolResponse: json.RawMessage(`{"stdout":""}`),
		})
	}
	if d.Blocked() {
		t.Fatalf("eight polls is a wait, not a stall: %s", d.Msg)
	}
}

// A poll between real work is not a stall: the counter resets on anything
// that is not a poll.
func TestPollsInterleavedWithWorkDoNotTrip(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	cfg.Limits.MaxConsecutivePolls = 12
	store := state.New(t.TempDir())

	for i := 0; i < 20; i++ {
		if d := post(store, cfg, "s", Input{
			ToolName:     "TaskOutput",
			ToolResponse: json.RawMessage(`{"stdout":"running"}`),
		}); d.Blocked() {
			t.Fatalf("tripped at poll %d despite work in between: %s", i, d.Msg)
		}
		post(store, cfg, "s", Input{
			ToolName:     "Bash",
			ToolInput:    json.RawMessage(`{"command":"go build ./..."}`),
			ToolResponse: json.RawMessage(`{"stdout":"","is_error":false}`),
		})
	}
}
