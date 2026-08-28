package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
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

// fakeClaude puts a `claude` on PATH that records its own argv, so a test can
// assert on exactly what the describer invoked the CLI with.
func fakeClaude(t *testing.T) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\n" +
		`echo "$@" > ` + argsFile + "\n" +
		`echo '{"result":"{\"title\":\"T\",\"body\":\"B\"}","is_error":false}'` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
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
