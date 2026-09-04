package work

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
