package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/cost"
	"github.com/orion-sdlc/orion/internal/events"
)

// fakeClaudeScript puts a `claude` stub with the given shell body onto PATH.
func fakeClaudeScript(t *testing.T, body string) {
	t.Helper()
	writeFakeBin(t, "claude", "#!/bin/sh\n"+body)
}

// OR-212 as the supervisor sees it: an expired login makes the CLI complain on
// stderr and exit before it emits a single stream frame. Nothing was sent, no
// session was opened, and the run must be recorded as one that never started
// rather than one that failed.
func TestARunThatEmitsNoStreamIsRecordedAsNeverStarted(t *testing.T) {
	w := ws(t, "")
	fakeClaudeScript(t, "echo 'Invalid API key · Please run /login' >&2\nexit 1\n")

	res, err := Run(w, Options{Stage: "ticket", Actor: events.ActorImplementer,
		Key: "OR-9", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1})
	if err == nil {
		t.Fatal("a CLI that exited 1 must still be reported as a failure to the caller")
	}
	if res.Started {
		t.Error("Started is true for a run that emitted no stream frame at all")
	}

	rep := cost.Aggregate(cost.ReadAll(events.Path(w.Dir)), "OR-9")
	if rep.Total.NeverStarted != 1 || rep.Total.Failed != 0 || rep.Total.Missing != 0 {
		t.Errorf("never started %d / failed %d / missing %d, want 1 / 0 / 0",
			rep.Total.NeverStarted, rep.Total.Failed, rep.Total.Missing)
	}
}

// The inverse, and the one that matters most: a run that emitted frames and
// then died mid-stream DID start and DID spend. Recording it as a fault would
// hide real money.
func TestARunCutOffMidStreamStillCountsAsStarted(t *testing.T) {
	w := ws(t, "")
	fakeClaudeScript(t,
		`echo '{"type":"system","subtype":"init","model":"claude-opus-5"}'`+"\n"+
			`echo '{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"working"}]}}'`+"\n"+
			"exit 0\n")

	res, err := Run(w, Options{Stage: "ticket", Actor: events.ActorImplementer,
		Key: "OR-9", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1})
	if err == nil {
		t.Fatal("a stream with no result frame is still a failed run (OR-127)")
	}
	if !res.Started {
		t.Error("a run that emitted frames was recorded as never having started")
	}

	rep := cost.Aggregate(cost.ReadAll(events.Path(w.Dir)), "OR-9")
	if rep.Total.NeverStarted != 0 || rep.Total.Failed != 1 || rep.Total.Missing != 1 {
		t.Errorf("never started %d / failed %d / missing %d, want 0 / 1 / 1",
			rep.Total.NeverStarted, rep.Total.Failed, rep.Total.Missing)
	}
	if out := cost.Render(rep); !strings.Contains(out, "FLOOR") {
		t.Errorf("the floor warning stopped firing where usage IS genuinely absent:\n%s", out)
	}
}

// OR-219, first and substantive problem, end to end. A COMPLETED run whose
// result frame is followed by background-task frames must still contribute its
// turns and tokens -- and its own session id, not a subagent's.
func TestACompletedRunContributesUsagePastTrailingBackgroundFrames(t *testing.T) {
	w := ws(t, "")
	fakeClaudeScript(t,
		`echo '{"type":"system","subtype":"init","model":"claude-opus-5"}'`+"\n"+
			`echo '{"type":"result","subtype":"success","is_error":false,"num_turns":108,"session_id":"ef17a0a3","result":"done","total_cost_usd":13.4,"usage":{"input_tokens":187,"cache_creation_input_tokens":237153,"cache_read_input_tokens":18262885,"output_tokens":70909}}'`+"\n"+
			`echo '{"type":"system","subtype":"background_tasks_changed","tasks":[],"session_id":"07798e8a"}'`+"\n"+
			`echo '{"type":"system","subtype":"task_notification","task_id":"b1","session_id":"07798e8a"}'`+"\n"+
			"exit 0\n")

	res, err := Run(w, Options{Stage: "ticket", Actor: events.ActorImplementer,
		Key: "OR-9", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if res.SessionID != "ef17a0a3" {
		t.Errorf("SessionID = %q, want the run's own id -- a background-task "+
			"frame carries a SUBAGENT's session and resuming it continues the "+
			"wrong conversation", res.SessionID)
	}

	rep := cost.Aggregate(cost.ReadAll(events.Path(w.Dir)), "OR-9")
	if rep.Total.Turns != 108 {
		t.Errorf("turns = %d, want 108: a completed run reported none", rep.Total.Turns)
	}
	if rep.Total.CostUSD != 13.4 {
		t.Errorf("cost = %v, want 13.4", rep.Total.CostUSD)
	}
	if rep.Total.Missing != 0 {
		t.Errorf("a completed run with a result frame was counted as missing usage")
	}
}
