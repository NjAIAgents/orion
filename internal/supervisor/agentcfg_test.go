package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/agentcfg"
)

// fakeClaudeRecordingArgsAndEnv is fakeClaudeRecordingArgs plus the child's
// environment, which is the other half of what decides a run's capabilities:
// the flags say which MCP servers it may use, CLAUDE_CONFIG_DIR says which
// plugins, subagents and commands it can even see.
func fakeClaudeRecordingArgsAndEnv(t *testing.T) (argsFile, envFile string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	argsFile = filepath.Join(dir, "args.txt")
	envFile = filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\n" +
		`echo "$@" > ` + argsFile + "\n" +
		"env > " + envFile + "\n" +
		`echo '{"type":"result","session_id":"abc","result":"done","total_cost_usd":0.1,"is_error":false}'` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile, envFile
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A supervised run must be launched with the configuration Orion decided on:
// its own config directory, and no MCP servers. Before OR-213 the child got
// the operator's entire ~/.claude -- 179 tools, 148 of them MCP tools with
// write access to whatever SaaS accounts were connected that day.
func TestASupervisedRunIsLaunchedWithOrionsOwnConfiguration(t *testing.T) {
	w := ws(t, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "operators-own"))
	argsFile, envFile := fakeClaudeRecordingArgsAndEnv(t)

	if _, err := Run(w, Options{Stage: "intent", Prompt: "do a thing",
		MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatal(err)
	}

	if got := read(t, argsFile); !strings.Contains(got, "--strict-mcp-config") {
		t.Errorf("the run must be given no MCP servers, got: %q", got)
	}
	want := "CLAUDE_CONFIG_DIR=" + filepath.Join(os.Getenv("ORION_HOME"), agentcfg.DirName)
	env := read(t, envFile)
	if !strings.Contains(env, want) {
		t.Errorf("child env missing %q", want)
	}
	if strings.Contains(env, "operators-own") {
		t.Error("the operator's own config directory reached the run")
	}
}

// The opt-in is the escape hatch, and it has to actually work: an operator
// who wants a plugin in one stage gets their whole configuration for that
// stage, flags included.
func TestAnOptedInStageKeepsTheOperatorsConfiguration(t *testing.T) {
	operator := filepath.Join(t.TempDir(), "operators-own")
	w := ws(t, `{"delegation":{"inherit_operator_config":["intent"]}}`)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", operator)
	argsFile, envFile := fakeClaudeRecordingArgsAndEnv(t)

	if _, err := Run(w, Options{Stage: "intent", Prompt: "do a thing",
		MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatal(err)
	}

	if got := read(t, argsFile); strings.Contains(got, "--strict-mcp-config") {
		t.Errorf("an opted-in run asked for its own MCP servers, got: %q", got)
	}
	if env := read(t, envFile); !strings.Contains(env, "CLAUDE_CONFIG_DIR="+operator) {
		t.Errorf("an opted-in run must keep the operator's config dir, got env: %q", env)
	}
}
