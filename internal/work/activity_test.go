package work

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

// ActivityLogger is the one logger every supervised run wires OnActivity to
// (OR-176); cmd/orion/fix.go's fixActivity only wraps it. These cases pin
// the "start" and "text" activity kinds that the fix-loop test in
// cmd/orion/actorrun_test.go doesn't exercise, so a regression there -- e.g.
// dropping the run-start marker or the agent's own narration from the event
// log -- would still be caught here even if the fix loop's own test stayed
// green.
func TestActivityLoggerEmitsRunStartOnSessionOpen(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}

	var console strings.Builder
	activity := ActivityLogger(log, &console, "OR-176", events.ActorImplementer)
	activity(supervisor.Activity{Kind: "start", Model: "sonnet"})
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || logged[0].Kind != events.KindRunStart {
		t.Fatalf("events = %+v, want exactly one KindRunStart", logged)
	}
	if logged[0].Actor != events.ActorImplementer || logged[0].Model != "sonnet" {
		t.Errorf("run-start event = %+v, want actor/model attributed", logged[0])
	}
}

// OR-217, as amended by OR-338. The console is for attention and the log file
// is for evidence: the per-call TRANSCRIPT is printed only under --verbose --
// three tool calls, three lines -- while a quiet console gets the bounded
// heartbeat instead, one line however many calls fall inside one interval.
// The event log is complete at BOTH levels: OR-168's triage and OR-199's
// history read it, so a verbosity setting that reached the record would be a
// forensic gap rather than a quieter screen.
func TestToolCallsArePrintedOnlyWhenVerboseAndLoggedAlways(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })

	for _, tc := range []struct {
		name    string
		verbose bool
		lines   int
	}{
		{"quiet", false, 1},
		{"verbose", true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ui.SetVerbose(tc.verbose)
			ui.ConsoleReset()
			logPath := filepath.Join(t.TempDir(), "events.jsonl")
			log, err := events.Open(logPath, events.Event{})
			if err != nil {
				t.Fatal(err)
			}

			var console strings.Builder
			activity := ActivityLogger(log, &console, "OR-217", events.ActorImplementer)
			for _, detail := range []string{"git status", "git diff", "git log"} {
				activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
					Detail: detail, Model: "sonnet"})
			}
			ui.Flush(&console)
			log.Close()

			out := strings.TrimSpace(console.String())
			if got := strings.Count(out, "\n") + 1; got != tc.lines {
				t.Errorf("%s console printed %d lines for three tool calls, want %d:\n%s",
					tc.name, got, tc.lines, out)
			}

			logged, err := events.Read(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(logged) != 3 || logged[0].Kind != events.KindTool {
				t.Fatalf("events = %+v, want three KindTool whatever the level", logged)
			}
			if !strings.Contains(logged[0].Msg, "git status") {
				t.Errorf("the event log lost the tool detail at the %s level: %+v", tc.name, logged[0])
			}
		})
	}
}

// The run-start line must state what the run was GIVEN. Until OR-213 the
// only way to learn a run's toolset was to read its raw transcript, which is
// how 179 tools -- 148 of them MCP tools against the operator's own live
// accounts -- went unnoticed for the whole life of the project.
func TestRunStartRecordsTheToolsetAndTheMCPServers(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}

	var console strings.Builder
	activity := ActivityLogger(log, &console, "OR-213", events.ActorImplementer)
	activity(supervisor.Activity{Kind: "start", Model: "opus", Tools: 31,
		MCPServers: []string{"atlassian"}})
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 {
		t.Fatalf("events = %+v, want one", logged)
	}
	if !strings.Contains(logged[0].Msg, "31 tools") || !strings.Contains(logged[0].Msg, "atlassian") {
		t.Errorf("msg = %q, must name the tool count and the MCP servers", logged[0].Msg)
	}
	// Machine-readable too: "which runs had a write path to the tracker" is a
	// query, and prose cannot be filtered.
	if got := logged[0].Detail["tools"]; got != float64(31) {
		t.Errorf("detail.tools = %v, want 31", got)
	}
	servers, _ := logged[0].Detail["mcp_servers"].([]any)
	if len(servers) != 1 || servers[0] != "atlassian" {
		t.Errorf("detail.mcp_servers = %v, want [atlassian]", logged[0].Detail["mcp_servers"])
	}
}

// The curated case is the one that has to read unambiguously: no MCP servers
// is a fact worth stating, and it must not be confused with not knowing.
func TestRunStartSaysNoMCPServersRatherThanStayingSilent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}

	var console strings.Builder
	ActivityLogger(log, &console, "OR-213", events.ActorImplementer)(
		supervisor.Activity{Kind: "start", Model: "opus", Tools: 18})
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0].Msg, "no MCP servers") {
		t.Errorf("msg = %q, want it to state that the run got no MCP servers", logged[0].Msg)
	}
}

// An unreported toolset (an older CLI that never sends "tools" on its init
// frame) must read as "not reported", never as "no MCP servers": those are
// two different facts, and the latter is a claim this code has no evidence
// for when Tools is zero because nothing was said, not because nothing was
// given.
func TestRunStartWithNoToolsetReportedDoesNotClaimNoMCPServers(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}

	var console strings.Builder
	ActivityLogger(log, &console, "OR-213", events.ActorImplementer)(
		supervisor.Activity{Kind: "start", Model: "opus"}) // Tools left zero: unreported
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 {
		t.Fatalf("events = %+v, want one", logged)
	}
	if strings.Contains(logged[0].Msg, "no MCP servers") {
		t.Errorf("msg = %q, an unreported toolset must not be rendered as a claim of no MCP servers", logged[0].Msg)
	}
	if !strings.Contains(logged[0].Msg, "not reported") {
		t.Errorf("msg = %q, want it to say the toolset was not reported", logged[0].Msg)
	}
}

// OR-338. #419 removed the live region, whose row was the only console
// output a working run had at default verbosity -- ui.LiveActivityNote is now
// a no-op and ui.Trace is still gated behind --verbose, so between "started"
// and the terminal verdict a working agent printed NOTHING and a run of any
// length looked identical to a hung one.
//
// The four cases below are the ticket's done-when conditions, one each: a
// progress line exists at default verbosity, it is bounded per ticket, it
// does not double the transcript under --verbose, and a run that does nothing
// still prints nothing.
func TestQuietRunPrintsAThrottledHeartbeatNamingElapsedAndTheLastToolCall(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	// A clock that advances a full interval between calls, so every tool call
	// is due a beat.
	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(heartbeatEvery); return clock }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
		Detail: "go test ./internal/work", Model: "opus"})
	ui.Flush(&console)

	out := console.String()
	if !strings.Contains(out, "OR-338") || !strings.Contains(out, "go test ./internal/work") {
		t.Fatalf("quiet console = %q, want a progress line naming the ticket and what it last did", out)
	}
	if !strings.Contains(out, "30s") {
		t.Errorf("quiet console = %q, want the elapsed time on the progress line", out)
	}
}

// The existing "quiet" case above advances the clock a full interval before
// its one tool call, so it never distinguishes "beats because due" from
// "beats because first". This one pins the latter: lastBeat starts zero, and
// the FIRST call must speak even though barely a second has passed -- a run
// that waited a full interval before its first sign of life is the OR-338
// defect, one interval smaller.
func TestFirstToolCallHeartbeatsImmediatelyWithoutWaitingAFullInterval(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
		Detail: "go build ./...", Model: "opus"})
	ui.Flush(&console)

	if out := console.String(); out == "" {
		t.Error("console = \"\", want the first tool call to heartbeat immediately, not wait a full interval")
	}
}

func TestHeartbeatIsBoundedToOneLinePerTicketPerInterval(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	// One second per tool call: twenty calls do not fill one interval, so
	// exactly one line -- the first -- may be printed. This is the OR-217
	// constraint restated: at concurrency 4 the transcript is unreadable, and
	// a heartbeat that fired per tool call would be that transcript again.
	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	for i := 0; i < 20; i++ {
		activity(supervisor.Activity{Kind: "tool", Tool: "Read",
			Detail: fmt.Sprintf("file-%d.go", i), Model: "opus"})
	}
	ui.Flush(&console)

	if got := strings.Count(strings.TrimSpace(console.String()), "\n") + 1; got != 1 {
		t.Errorf("20 tool calls inside one interval printed %d lines, want 1:\n%s",
			got, console.String())
	}
	// The record is not throttled: the console filter decides what is
	// PRINTED, never what happened.
	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 20 {
		t.Errorf("event log has %d entries, want all 20 -- the log was never the defect", len(logged))
	}
}

func TestVerboseKeepsTheTranscriptAndAddsNoHeartbeat(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(true)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(heartbeatEvery); return clock }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
		Detail: "go test ./internal/work", Model: "opus"})
	ui.Flush(&console)

	out := strings.TrimSpace(console.String())
	if !strings.Contains(out, "ran go test ./internal/work") {
		t.Fatalf("verbose console = %q, want the per-tool transcript line unchanged", out)
	}
	if got := strings.Count(out, "\n") + 1; got != 1 {
		t.Errorf("verbose console printed %d lines for one tool call, want 1 -- "+
			"--verbose already shows every call, so a summary of it is noise:\n%s", got, out)
	}
}

func TestARunThatDoesNothingPrintsNothing(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	// Hours pass. Nothing calls a tool. Silence has to keep meaning silence,
	// or the heartbeat would report a hung run as a working one.
	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Hour); return clock }

	var console strings.Builder
	activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	ui.Flush(&console)

	if out := console.String(); out != "" {
		t.Errorf("console = %q, want nothing: no activity is not progress", out)
	}
}

// OR-338 case 9. A run that never calls a tool -- only "start", "text", and
// an unrecognized "end" kind the switch has no case for -- must print
// nothing: the heartbeat is driven by tool activity, and none of these kinds
// is that.
func TestARunWithNoToolCallsProducesNoConsoleOutput(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Hour); return clock }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "start", Model: "opus"})
	activity(supervisor.Activity{Kind: "text", Detail: "planning the change", Model: "opus"})
	activity(supervisor.Activity{Kind: "end"})
	ui.Flush(&console)

	if out := console.String(); out != "" {
		t.Errorf("console = %q, want nothing: no tool call means no heartbeat", out)
	}
}

// OR-338 case 10. lastBeat lives in activityLoggerAt's closure, so each
// ActivityLogger call -- one per ticket in a concurrent run -- owns its own
// budget. A shared budget would mean the second ticket's agent goes quiet
// because the first one just spoke, which is the same "is it hung?" question
// this fix exists to answer.
func TestEachTicketsHeartbeatIsIndependentlyThrottled(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	// Same injected clock for both tickets: if the budget were shared, the
	// second ticket's call -- a second after the first -- would be swallowed.
	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }

	var console strings.Builder
	first := activityLoggerAt(log, &console, "OR-338-A", events.ActorImplementer, now)
	second := activityLoggerAt(log, &console, "OR-338-B", events.ActorQA, now)

	first(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "go build ./...", Model: "opus"})
	second(supervisor.Activity{Kind: "tool", Tool: "Read", Detail: "main.go", Model: "sonnet"})
	ui.Flush(&console)

	out := console.String()
	if !strings.Contains(out, "OR-338-A") || !strings.Contains(out, "OR-338-B") {
		t.Errorf("console = %q, want both tickets' first tool call to heartbeat -- one ticket's budget must not consume another's", out)
	}
	if got := strings.Count(strings.TrimSpace(out), "\n") + 1; got != 2 {
		t.Errorf("console printed %d lines for two tickets' first calls, want 2:\n%s", got, out)
	}
}

// OR-338 case 11. "start" and "text" go through their own log.Emit branches
// and never reach the heartbeat's `case "tool"` gate at all, so even a clock
// that would clear the throttle must not produce a console line for them.
func TestNonToolActivitiesDoNotTriggerAHeartbeat(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	// Advances a full interval per call, so if "start" or "text" were
	// mistakenly wired into the heartbeat gate, either would fire.
	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(heartbeatEvery); return clock }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "start", Model: "opus", Tools: 5})
	activity(supervisor.Activity{Kind: "text", Detail: "here is my plan", Model: "opus"})
	activity(supervisor.Activity{Kind: "end"})
	ui.Flush(&console)

	if out := console.String(); out != "" {
		t.Errorf("console = %q, want nothing: start/text/end must not drive the heartbeat", out)
	}

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 2 {
		t.Fatalf("events = %+v, want the start and text events logged (end has no case, logs nothing)", logged)
	}
}

// OR-338 case 12. The heartbeat is the only console line a quiet run gets, so
// it has to carry attribution -- which actor, on which model -- not just
// elapsed time and the last tool: a bare timer would answer "is it hung?" but
// not "which of my tickets is this?".
func TestHeartbeatLineIncludesActorAndModel(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(heartbeatEvery); return clock }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
		Detail: "go test ./internal/work", Model: "opus"})
	ui.Flush(&console)

	out := console.String()
	if !strings.Contains(out, actors.Display(events.ActorImplementer)) {
		t.Errorf("heartbeat = %q, want the actor's display name", out)
	}
	if !strings.Contains(out, "opus") {
		t.Errorf("heartbeat = %q, want the model", out)
	}
}

// OR-338 case 17. Four tickets running concurrently, each hammering tool
// calls, must still read as four heartbeats total inside one interval -- not
// forty. This is OR-217's readability constraint applied to the heartbeat
// itself: one line per ticket per interval, whatever the concurrency.
func TestConcurrentRunsHeartbeatAtMostOncePerTicketPerInterval(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	// One shared clock, advancing far slower than the interval per call, so
	// only the very first call of each ticket falls due.
	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }

	var console strings.Builder
	tickets := make([]func(supervisor.Activity), 4)
	for i := range tickets {
		tickets[i] = activityLoggerAt(log, &console,
			fmt.Sprintf("OR-338-%d", i), events.ActorImplementer, now)
	}

	for round := 0; round < 5; round++ {
		for i, activity := range tickets {
			activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
				Detail: fmt.Sprintf("ticket-%d round-%d", i, round), Model: "opus"})
		}
	}
	ui.Flush(&console)

	out := strings.TrimSpace(console.String())
	if got := strings.Count(out, "\n") + 1; got != len(tickets) {
		t.Errorf("4 tickets x 5 tool calls each inside one interval printed %d lines, want %d (one per ticket):\n%s",
			got, len(tickets), out)
	}
	for i := range tickets {
		want := fmt.Sprintf("OR-338-%d", i)
		if !strings.Contains(out, want) {
			t.Errorf("console = %q, want a heartbeat line for %s", out, want)
		}
	}
}

// OR-338 case 18. --verbose is a global toggle read fresh on every call
// (ui.Verbose(), not a value captured at ActivityLogger construction), so
// flipping it mid-run must change the very next tool call's output: the
// transcript line appears and the heartbeat stops, with no restart needed.
func TestSwitchingVerboseMidRunChangesOutputImmediately(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(heartbeatEvery); return clock }

	activity := activityLoggerAt(log, io.Discard, "OR-338", events.ActorImplementer, now)

	activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
		Detail: "go build ./...", Model: "opus"})

	ui.SetVerbose(true)
	var console strings.Builder
	activity = activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
		Detail: "go test ./...", Model: "opus"})
	ui.Flush(&console)

	out := strings.TrimSpace(console.String())
	if !strings.Contains(out, "ran go test ./...") {
		t.Errorf("console after flipping --verbose on = %q, want the per-tool transcript line", out)
	}
	if got := strings.Count(out, "\n") + 1; got != 1 {
		t.Errorf("console after flipping --verbose on printed %d lines, want 1 -- no heartbeat once verbose:\n%s", got, out)
	}
}

// OR-338 case 19. This fix touches only what gets PRINTED to the console; it
// must not touch what gets EMITTED to the event log. A KindTool event's
// shape and content -- actor, model, message -- has to read exactly as it
// did before OR-338, at any verbosity.
func TestEventLogFormatAndContentAreUnchangedByTheHeartbeatFix(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })

	for _, tc := range []struct {
		name    string
		verbose bool
	}{
		{"quiet", false},
		{"verbose", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ui.SetVerbose(tc.verbose)
			ui.ConsoleReset()

			logPath := filepath.Join(t.TempDir(), "events.jsonl")
			log, err := events.Open(logPath, events.Event{})
			if err != nil {
				t.Fatal(err)
			}

			activity := ActivityLogger(log, io.Discard, "OR-217", events.ActorImplementer)
			activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
				Detail: "git status", Model: "sonnet"})
			log.Close()

			logged, err := events.Read(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(logged) != 1 {
				t.Fatalf("events = %+v, want exactly one event", logged)
			}
			got := logged[0]
			if got.Kind != events.KindTool {
				t.Errorf("Kind = %q, want %q", got.Kind, events.KindTool)
			}
			if got.Actor != events.ActorImplementer {
				t.Errorf("Actor = %q, want %q", got.Actor, events.ActorImplementer)
			}
			if got.Model != "sonnet" {
				t.Errorf("Model = %q, want %q", got.Model, "sonnet")
			}
			if !strings.Contains(got.Msg, "git status") {
				t.Errorf("Msg = %q, want it to contain the tool detail", got.Msg)
			}
		})
	}
}

// OR-338 case 20. lastBeat resets on every heartbeat, so the tool named on
// the line has to be the one that just fired, not whatever tool happened to
// be running when the PREVIOUS interval's beat printed. A stale tool name
// would misreport what the agent is doing right now.
func TestHeartbeatNamesTheToolThatTriggeredItNotAStaleOne(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	// First call fires immediately (lastBeat starts zero) and beats on
	// "Read". The next several calls land inside the same interval and must
	// not beat. Only once the clock clears the interval does "WebFetch"
	// beat -- and it must be WebFetch on the line, not the earlier Read.
	// The leading zero-delta entry is consumed by activityLoggerAt's own
	// `started := now()` call, which fires before the loop below makes any
	// activity() call -- omitting it here shifts every later entry by one
	// and runs the clock past the end of the slice.
	calls := []struct {
		tool  string
		delta time.Duration
	}{
		{"", 0},
		{"Read", 0},
		{"Bash", time.Second},
		{"Grep", time.Second},
		{"WebFetch", heartbeatEvery},
	}
	i := -1
	clock := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	now := func() time.Time {
		i++
		clock = clock.Add(calls[i].delta)
		return clock
	}

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	for _, c := range calls[1:] {
		activity(supervisor.Activity{Kind: "tool", Tool: c.tool,
			Detail: c.tool + "-detail", Model: "opus"})
	}
	ui.Flush(&console)

	out := strings.TrimSpace(console.String())
	if got := strings.Count(out, "\n") + 1; got != 2 {
		t.Fatalf("console printed %d lines, want 2 (first call, then the one past the interval):\n%s", got, out)
	}
	if strings.Contains(out, "Read-detail") == false {
		t.Errorf("console = %q, want the first beat to name Read (the first call)", out)
	}
	if strings.Contains(out, "Bash-detail") || strings.Contains(out, "Grep-detail") {
		t.Errorf("console = %q, want no line for the throttled Bash/Grep calls", out)
	}
	if !strings.Contains(out, "WebFetch-detail") {
		t.Errorf("console = %q, want the second beat to name WebFetch, the call that triggered it, not a stale prior tool", out)
	}
}

func TestActivityLoggerEmitsSayOnText(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}

	var console strings.Builder
	activity := ActivityLogger(log, &console, "OR-176", events.ActorQA)
	activity(supervisor.Activity{Kind: "text", Detail: "root cause is a hand-rolled logger", Model: "sonnet"})
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || logged[0].Kind != events.KindSay {
		t.Fatalf("events = %+v, want exactly one KindSay", logged)
	}
	if logged[0].Actor != events.ActorQA || logged[0].Msg != "root cause is a hand-rolled logger" {
		t.Errorf("say event = %+v, want the actor and the agent's own text carried through unedited", logged[0])
	}
}

// OR-338. The heartbeat's elapsed time is t.Sub(started).Round(time.Second):
// a sub-second offset must not leak onto the line, or two runs a millisecond
// apart would print different, meaningless elapsed times.
func TestHeartbeatElapsedTimeIsRoundedToTheNearestSecond(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	started := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	// 30.6s past start rounds up to 31s; the first call always heartbeats
	// (lastBeat starts zero), so this alone pins the rounding.
	called := false
	now := func() time.Time {
		if !called {
			called = true
			return started
		}
		return started.Add(30*time.Second + 600*time.Millisecond)
	}

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
		Detail: "go vet ./...", Model: "opus"})
	ui.Flush(&console)

	out := console.String()
	if !strings.Contains(out, "31s") {
		t.Errorf("console = %q, want elapsed time rounded to the nearest second (31s)", out)
	}
	if strings.Contains(out, "30.6s") {
		t.Errorf("console = %q, elapsed time leaked a sub-second fraction", out)
	}
}

// OR-338. A tool call landing exactly on the interval boundary must still
// beat: the guard is `>= heartbeatEvery`, not `>`, so a ticket firing tool
// calls at a steady 30s cadence never goes a beat longer silent than the
// interval it promises.
func TestToolCallExactlyThirtySecondsAfterLastHeartbeatBeatsAgain(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	start := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	// A leading entry for activityLoggerAt's own `started := now()` call,
	// which fires before either activity() call below.
	calls := []time.Time{start, start, start.Add(30 * time.Second)}
	i := 0
	now := func() time.Time { t := calls[i]; i++; return t }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "first", Model: "opus"})
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "second", Model: "opus"})
	ui.Flush(&console)

	if got := strings.Count(strings.TrimSpace(console.String()), "\n") + 1; got != 2 {
		t.Errorf("a tool call exactly 30s after the last heartbeat produced %d lines, want 2:\n%s",
			got, console.String())
	}
}

// OR-338. One second short of the interval must NOT beat -- the throttle's
// whole point is a bound on how often the console speaks, and firing early
// on a near-miss is the same defect as not throttling at all.
func TestToolCallTwentyNineSecondsAfterLastHeartbeatDoesNotBeatAgain(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	start := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	// A leading entry for activityLoggerAt's own `started := now()` call,
	// which fires before either activity() call below.
	calls := []time.Time{start, start, start.Add(29 * time.Second)}
	i := 0
	now := func() time.Time { t := calls[i]; i++; return t }

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "first", Model: "opus"})
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "second", Model: "opus"})
	ui.Flush(&console)

	if got := strings.Count(strings.TrimSpace(console.String()), "\n") + 1; got != 1 {
		t.Errorf("a tool call 29s after the last heartbeat produced %d lines, want 1 (no new beat):\n%s",
			got, console.String())
	}
}

// OR-338. A run spanning hours must still print a readable elapsed time
// rather than an unbroken run of seconds -- Duration.Round(time.Second)'s own
// String() carries the hour/minute breakdown, which is what a person waiting
// on a multi-hour ticket reads.
func TestVeryLongRunDisplaysElapsedTimeInMinutesSecondsFormat(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)
	ui.ConsoleReset()

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	start := time.Date(2026, 9, 4, 16, 25, 0, 0, time.UTC)
	called := false
	now := func() time.Time {
		if !called {
			called = true
			return start
		}
		// Two hours and thirty minutes in.
		return start.Add(2*time.Hour + 30*time.Minute)
	}

	var console strings.Builder
	activity := activityLoggerAt(log, &console, "OR-338", events.ActorImplementer, now)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "long build", Model: "opus"})
	ui.Flush(&console)

	out := console.String()
	if !strings.Contains(out, "2h30m0s") {
		t.Errorf("console = %q, want the elapsed time on an hours-long run rendered as h/m/s, not raw seconds", out)
	}
	if strings.Contains(out, "9000s") {
		t.Errorf("console = %q, elapsed time printed as raw seconds instead of a broken-down duration", out)
	}
}
