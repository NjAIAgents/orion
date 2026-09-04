package work

import (
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

// OR-217. The console is for attention and the log file is for evidence: a
// tool call is printed only under --verbose, and the event log is complete
// at BOTH levels -- OR-168's triage and OR-199's history read it, so a
// verbosity setting that reached the record would be a forensic gap rather
// than a quieter screen.
func TestToolCallsArePrintedOnlyWhenVerboseAndLoggedAlways(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })

	for _, tc := range []struct {
		name    string
		verbose bool
		console bool
	}{
		{"quiet", false, false},
		{"verbose", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ui.SetVerbose(tc.verbose)
			logPath := filepath.Join(t.TempDir(), "events.jsonl")
			log, err := events.Open(logPath, events.Event{})
			if err != nil {
				t.Fatal(err)
			}

			var console strings.Builder
			activity := ActivityLogger(log, &console, "OR-217", events.ActorImplementer)
			activity(supervisor.Activity{Kind: "tool", Tool: "Bash",
				Detail: "git status", Model: "sonnet"})
			ui.Flush(&console)
			log.Close()

			if got := strings.Contains(console.String(), "git status"); got != tc.console {
				t.Errorf("console carried the tool call = %v, want %v:\n%s",
					got, tc.console, console.String())
			}

			logged, err := events.Read(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(logged) != 1 || logged[0].Kind != events.KindTool {
				t.Fatalf("events = %+v, want exactly one KindTool whatever the level", logged)
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

// OR-338. Removing the live region (#419) left ui.LiveActivityNote a no-op
// and ui.Trace gated behind --verbose, so at default verbosity a working run
// printed nothing at all between "started" and its verdict -- a run of any
// length looked exactly like a hung one.
//
// The heartbeat is what replaces the region: throttled, so it is progress
// rather than the transcript OR-217 measured at 60% of a screen.
func TestAQuietRunStillReportsProgressPeriodically(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(false)

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var console strings.Builder
	activity := ActivityLogger(log, &console, "OR-338", events.ActorImplementer)

	// The first call must stay silent: "started" was printed a moment ago
	// and a heartbeat on its heels says nothing new.
	activity(supervisor.Activity{Kind: "tool", Tool: "Read", Detail: "a.go"})
	ui.Flush(&console)
	if strings.Contains(console.String(), "a.go") {
		t.Fatalf("the first tool call printed; it must not:\n%s", console.String())
	}

	// A call inside the interval is still silent -- this is the throttle,
	// and without it the screen is the transcript again.
	activity(supervisor.Activity{Kind: "tool", Tool: "Read", Detail: "b.go"})
	ui.Flush(&console)
	if strings.Contains(console.String(), "b.go") {
		t.Fatalf("a tool call inside the interval printed; the throttle is not holding:\n%s",
			console.String())
	}

	// Past the interval, it speaks. Reaching into the heartbeat rather than
	// sleeping a minute: what is under test is the rule, not the clock.
	advance(t, heartbeatEvery)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "go test ./..."})
	ui.Flush(&console)
	out := console.String()
	if !strings.Contains(out, "go test ./...") {
		t.Errorf("no progress line after the interval elapsed; a working run is "+
			"still indistinguishable from a hung one:\n%s", out)
	}
	if !strings.Contains(out, "OR-338") {
		t.Errorf("the progress line does not name its ticket:\n%s", out)
	}
}

// Under --verbose the Trace line already carries every tool call, so a
// heartbeat beside it would be the same fact twice.
func TestVerboseDoesNotAlsoPrintTheHeartbeat(t *testing.T) {
	t.Cleanup(func() { ui.SetVerbose(false) })
	ui.SetVerbose(true)

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var console strings.Builder
	activity := ActivityLogger(log, &console, "OR-338", events.ActorImplementer)
	activity(supervisor.Activity{Kind: "tool", Tool: "Read", Detail: "a.go"})
	advance(t, heartbeatEvery)
	activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "go test ./..."})
	ui.Flush(&console)

	if n := strings.Count(console.String(), "go test ./..."); n != 1 {
		t.Errorf("the tool call appeared %d times under --verbose, want 1 "+
			"(the Trace line only):\n%s", n, console.String())
	}
}

// advance moves the heartbeat's clock forward for the rest of the test, so a
// one-minute rule is exercised without a one-minute test.
func advance(t *testing.T, d time.Duration) {
	t.Helper()
	prev := heartbeatNow
	t.Cleanup(func() { heartbeatNow = prev })
	heartbeatNow = func() time.Time { return prev().Add(d) }
}
