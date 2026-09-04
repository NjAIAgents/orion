package main

import (
	"github.com/orion-sdlc/orion/internal/agentcfg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

// OR-133. The ci-fix run is attributed to the devops engineer in the event
// log and the cost report, so it has to RUN as the devops engineer too --
// otherwise configuring agents.devops.model moves the attribution and
// nothing else.
func TestTheCIFixRunCarriesTheDevOpsModelAndEffort(t *testing.T) {
	if err := actors.Configure(map[string]config.Agent{
		"devops": {Model: "haiku", Effort: "low"},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = actors.Configure(nil) })

	o := fixOptions("FCIA-6", "orion/FCIA-6", "test (failure)", config.Config{})
	if o.Actor != events.ActorDevOps {
		t.Errorf("actor = %q, want the devops engineer", o.Actor)
	}
	if o.Model != "haiku" || o.Effort != "low" {
		t.Errorf("ci-fix ran with model=%q effort=%q, want the configured haiku/low",
			o.Model, o.Effort)
	}
}

// And empty stays empty: the shipped roster sets no effort, and inventing one
// would override whatever the operator's own CLI is configured with.
func TestTheCIFixRunPassesNoEffortWhenNoneIsConfigured(t *testing.T) {
	if err := actors.Configure(nil); err != nil {
		t.Fatal(err)
	}
	if o := fixOptions("FCIA-6", "orion/FCIA-6", "boom", config.Config{}); o.Effort != "" {
		t.Errorf("effort = %q, want none", o.Effort)
	}
}

// OR-176. The fix loop hand-rolled its own OnActivity instead of the shared
// work.ActivityLogger every other supervised run uses, so its tool calls
// printed as unattributed console lines and never reached the event log at
// all. fixActivity is the exact callback fixRun wires up; driving it
// directly proves both halves are fixed without standing up a workspace, a
// worktree or the supervisor.
func TestFixActivityAttributesConsoleLinesAndLogsToolEvents(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var console strings.Builder
	activity := fixActivity(log, &console, "OR-173")

	// A tool call is transcript, so the console prints it only under
	// --verbose now (OR-217). What OR-176 is about is what the line SAYS
	// when it is printed, so this asserts at the level that prints it -- and
	// below, that the event log is complete at either level.
	ui.SetVerbose(true)
	t.Cleanup(func() { ui.SetVerbose(false) })

	activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: "go test ./...", Model: "sonnet"})
	log.Close()

	// The console line: no reader can tell who is acting or what it costs
	// from a bare "working   Bash go test ./..." -- the ticket key, the
	// devops actor and the model all have to be there.
	out := console.String()
	for _, want := range []string{"OR-173", actors.Display(events.ActorDevOps), "sonnet", "go test ./..."} {
		if !strings.Contains(out, want) {
			t.Errorf("fix activity console line missing %q, got: %q", want, out)
		}
	}

	// The event log: the hand-rolled version emitted nothing, so `orion logs`
	// had no record the devops engineer ran at all.
	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var sawTool bool
	for _, e := range logged {
		if e.Kind != events.KindTool {
			continue
		}
		sawTool = true
		if e.Actor != events.ActorDevOps {
			t.Errorf("tool event actor = %q, want %q", e.Actor, events.ActorDevOps)
		}
		if e.Model != "sonnet" {
			t.Errorf("tool event model = %q, want sonnet", e.Model)
		}
		if !strings.Contains(e.Msg, "Bash") || !strings.Contains(e.Msg, "go test ./...") {
			t.Errorf("tool event msg = %q, want it to name the tool and its detail", e.Msg)
		}
	}
	if !sawTool {
		t.Error("no KindTool event was logged for the fix run's tool call")
	}
}

// The console filter runs AFTER the event log Emit, so the JSONL has to be
// complete at the QUIET level too -- OR-217's whole premise is that the
// transcript is withheld from the console, never from the record. This is
// TestFixActivityAttributesConsoleLinesAndLogsToolEvents's companion at the
// level nobody explicitly asked for verbosity.
//
// Amended by OR-338. Withholding the transcript is not the same as printing
// nothing: with the live region gone, quiet meant a fix loop was silent
// between "started" and its verdict, which reads exactly like a hung one.
// What the quiet console owes the reader is the RATE the region had -- one
// bounded line saying it is working and what it last did -- so that is what
// is asserted here, alongside the record being complete regardless.
func TestFixActivityLogsToolEventsEvenWhenTheConsoleIsQuiet(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var console strings.Builder
	activity := fixActivity(log, &console, "OR-173")

	ui.SetVerbose(false)
	ui.ConsoleReset()
	t.Cleanup(func() { ui.SetVerbose(false) })

	// Three calls inside one heartbeat interval. The quiet console gets the
	// bounded heartbeat -- one line, naming what the run last did (OR-338) --
	// and NOT the per-call transcript, which is still what --verbose means.
	for _, detail := range []string{"go test ./...", "go vet ./...", "go build ./..."} {
		activity(supervisor.Activity{Kind: "tool", Tool: "Bash", Detail: detail, Model: "sonnet"})
	}
	ui.Flush(&console)
	log.Close()

	out := strings.TrimSpace(console.String())
	if got := strings.Count(out, "\n") + 1; got != 1 {
		t.Errorf("the quiet console printed %d lines for three tool calls, want the one "+
			"bounded heartbeat rather than the transcript:\n%s", got, out)
	}
	if !strings.Contains(out, "go test ./...") {
		t.Errorf("the quiet console said nothing about what the run was doing: %q -- "+
			"a working fix loop that prints nothing is indistinguishable from a hung one (OR-338)", out)
	}

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, e := range logged {
		if e.Kind == events.KindTool {
			tools = append(tools, e.Msg)
		}
	}
	// Every call, not just the one the console summarised: the heartbeat is a
	// console rate limit, never a filter on the record.
	if len(tools) != 3 {
		t.Fatalf("KindTool events = %q, want all three -- the record must not depend on "+
			"verbosity or on the console's heartbeat interval", tools)
	}
	for i, want := range []string{"go test ./...", "go vet ./...", "go build ./..."} {
		if !strings.Contains(tools[i], "Bash") || !strings.Contains(tools[i], want) {
			t.Errorf("tool event msg = %q, want it to name the tool and its detail %q", tools[i], want)
		}
	}
}

// fakeClaude puts a `claude` on PATH that records its own argv, so a test can
// assert on exactly what the describer invoked the CLI with.
func fakeClaude(t *testing.T) (argsFile string) {
	t.Helper()
	// The args file lives in the SAME directory as the fake, because callers
	// derive further paths from filepath.Dir(argsFile) and extend the fake
	// in place -- see the changelog test below.
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\n" +
		`echo "$@" > ` + argsFile + "\n" +
		`echo '{"result":"{\"title\":\"T\",\"body\":\"B\"}","is_error":false}'` + "\n" +
		"exit 0\n"
	writeFakeBinIn(t, dir, "claude", script)
	// The describer builds its own curated config directory under ORION_HOME
	// (OR-213), and reads the operator's home to discover nj-agents. Both are
	// redirected so a unit test writes nothing into the real one and asserts
	// nothing about whatever the developer happens to have installed.
	t.Setenv("ORION_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	return argsFile
}

// The describer builds its own argv rather than going through
// internal/supervisor, so it needs the same curation -- and would otherwise
// keep the write handle to the tracker that OR-213 exists to remove.
func TestTheDescriberGetsNoMCPServers(t *testing.T) {
	if !agentcfg.CurationAuthenticates() {
		// A curated config directory cannot authenticate on this platform, so
		// there is no curated run to assert (OR-239). Skipped rather than
		// softened: this assertion is still exactly right on Linux and in CI.
		t.Skip("curation unavailable on this platform (OR-239)")
	}
	argsFile := fakeClaude(t)

	if _, err := describeRunner(t.TempDir(), "", "", "describe it"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "--strict-mcp-config") {
		t.Errorf("describe invocation missing --strict-mcp-config, got: %q", got)
	}
}

func TestTheDescriberPassesItsModelAndEffortToClaude(t *testing.T) {
	argsFile := fakeClaude(t)

	if _, err := describeRunner(t.TempDir(), "haiku", "low", "describe it"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "--model haiku") {
		t.Errorf("describe invocation missing --model haiku, got: %q", got)
	}
	if !strings.Contains(string(got), "--effort low") {
		t.Errorf("describe invocation missing --effort low, got: %q", got)
	}
}

// An unset field passes no flag at all rather than an empty one -- `--model ""`
// is not the same request as leaving the CLI's own setting alone.
func TestTheDescriberPassesNoFlagForAnUnsetModelOrEffort(t *testing.T) {
	argsFile := fakeClaude(t)

	if _, err := describeRunner(t.TempDir(), "", "", "describe it"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "--model") || strings.Contains(string(got), "--effort") {
		t.Errorf("an unset model or effort must add no flag, got: %q", got)
	}
}

// changelogRunner builds its own argv exactly like describeRunner, and the
// same OR-213 curation has to apply to it: a generator with write access to
// CHANGELOG.md is not the write handle this ticket is about, but it still
// runs with the operator's whole plugin surface and 148 unused MCP tool
// definitions re-sent on every turn unless it goes through agentcfg too.
func TestTheChangelogRunnerGetsNoMCPServersAndACuratedConfigDir(t *testing.T) {
	if !agentcfg.CurationAuthenticates() {
		// A curated config directory cannot authenticate on this platform, so
		// there is no curated run to assert (OR-239). Skipped rather than
		// softened: this assertion is still exactly right on Linux and in CI.
		t.Skip("curation unavailable on this platform (OR-239)")
	}
	argsFile := fakeClaude(t)
	operator := filepath.Join(t.TempDir(), "operators-own")
	if err := os.MkdirAll(operator, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", operator)

	envFile := filepath.Join(filepath.Dir(argsFile), "env.txt")
	// fakeClaude's script only records argv; extend it here to also capture
	// the child's environment, which is where CLAUDE_CONFIG_DIR shows up.
	script := "#!/bin/sh\n" +
		`echo "$@" > ` + argsFile + "\n" +
		"env > " + envFile + "\n" +
		`echo '{"result":"did the changelog","is_error":false}'` + "\n" +
		"exit 0\n"
	// Through the helper, not os.WriteFile: on Windows the .bat shim beside
	// the script is what PATH actually resolves, so replacing only the script
	// would leave the shim pointing at the old body.
	writeFakeBinIn(t, filepath.Dir(argsFile), "claude", script)

	if _, err := changelogRunner(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "--strict-mcp-config") {
		t.Errorf("changelog invocation missing --strict-mcp-config, got: %q", got)
	}

	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "operators-own") {
		t.Error("the operator's own config directory reached the changelog run")
	}
	if !strings.Contains(string(env), "CLAUDE_CONFIG_DIR="+filepath.Join(os.Getenv("ORION_HOME"), "agent-config")) {
		t.Errorf("changelog run must get the curated CLAUDE_CONFIG_DIR, got env: %q", env)
	}
}
