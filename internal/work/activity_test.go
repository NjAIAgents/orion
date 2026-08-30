package work

import (
	"path/filepath"
	"strings"
	"testing"

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
