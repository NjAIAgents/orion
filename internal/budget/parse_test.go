package budget

import (
	"strings"
	"testing"
)

// The bug OR-219 was filed on, from the real stream that produced it.
//
// With --output-format stream-json the CLI keeps emitting background-task
// frames AFTER its result frame. Every one is a well-formed JSON object, so
// the old "last JSON object" rule handed the parser a frame with no usage on
// it and the run reported as having consumed nothing. It only ever hit LONG
// runs -- a run long enough to spawn background work is the one with frames
// after its result -- which is why the OR-168 cost report lost its implementer
// entirely while the short QA runs kept reporting correctly.
//
// Trimmed from the real capture in
// ~/.orion/projects/orion-83d87b/.orion/logs/20260830-190008-ticket-a1.log:
// the 26-minute implementer run the report said cost nothing, and which had
// in fact cost $13.40 over 108 turns.
const streamWithTrailingBackgroundFrames = `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":3,"output_tokens":9}}}
{"type":"result","subtype":"success","is_error":false,"num_turns":108,"session_id":"ef17a0a3","result":"done","total_cost_usd":13.397707,"usage":{"input_tokens":187,"cache_creation_input_tokens":237153,"cache_read_input_tokens":18262885,"output_tokens":70909},"modelUsage":{"claude-opus-5":{"contextWindow":1000000}}}
{"type":"system","subtype":"background_tasks_changed","tasks":[],"session_id":"07798e8a"}
{"type":"system","subtype":"task_updated","task_id":"bjgcdq8","session_id":"07798e8a"}
{"type":"system","subtype":"task_notification","task_id":"bjgcdq8","message":"agent finished","session_id":"07798e8a"}`

func TestResultSurvivesTrailingBackgroundTaskFrames(t *testing.T) {
	r, ok := FromResultJSON(streamWithTrailingBackgroundFrames)
	if !ok {
		t.Fatal("a completed run reported no usage: the result frame was not the " +
			"last JSON object on the stream, and the parser stopped at the last object")
	}
	if r.Turns != 108 {
		t.Errorf("Turns = %d, want 108", r.Turns)
	}
	if r.CostUSD != 13.397707 {
		t.Errorf("CostUSD = %v, want 13.397707", r.CostUSD)
	}
	if want := 187 + 237153 + 18262885; r.InputTokens != want {
		t.Errorf("InputTokens = %d, want %d", r.InputTokens, want)
	}
}

// The trailing frames carry a SUBAGENT's session id. Picking the wrong line
// does not merely lose the numbers: it would resume the wrong conversation.
func TestResultLineIsTheRunsOwnFrameNotTheLastOne(t *testing.T) {
	line, ok := ResultLine(streamWithTrailingBackgroundFrames)
	if !ok {
		t.Fatal("no result frame found")
	}
	if !strings.Contains(line, "ef17a0a3") {
		t.Errorf("picked %q, want the frame carrying the run's own session id", line)
	}
	if strings.Contains(line, "07798e8a") {
		t.Error("picked a background-task frame, whose session id belongs to a subagent")
	}
}

// --output-format json predates the type field, and the capture is a bounded
// tail that can begin mid-object. Neither may defeat the search.
func TestResultLineFindsTheUntypedLegacyObject(t *testing.T) {
	in := `ns":6}},"service_tier":"standard"}` + "\n" +
		`{"is_error":false,"num_turns":3,"session_id":"abc-123","total_cost_usd":0.4,` +
		`"usage":{"input_tokens":10,"output_tokens":2}}`
	r, ok := FromResultJSON(in)
	if !ok || r.Turns != 3 {
		t.Fatalf("FromResultJSON(legacy tail) = %+v, ok = %v", r, ok)
	}
}

// A stream that carries system frames and nothing else never began: there is
// no result to find, and inventing one from a notification frame is exactly
// the failure above pointed the other way.
func TestResultLineRefusesSystemFramesAlone(t *testing.T) {
	in := `{"type":"system","subtype":"init","session_id":"abc"}` + "\n" +
		`{"type":"system","subtype":"task_notification","session_id":"def"}`
	if line, ok := ResultLine(in); ok {
		t.Errorf("ResultLine found %q in a stream with no result frame", line)
	}
}
