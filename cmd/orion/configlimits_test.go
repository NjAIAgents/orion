package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/watch"
)

// adoptedConfig is what an adopted repository's orion.json actually looks
// like: a trimmed copy of the template, carrying the "_comment_*" keys that
// explain the block and NOT carrying max_concurrent_tickets, because its
// author never touched that setting. This shape is the whole point of
// OR-198 -- the operator told to change the value does not find it.
const adoptedConfig = `{
  "version": 1,

  "_comment_limits": "A limit of 0 restores the default rather than meaning unlimited.",
  "limits": {
    "max_tool_calls": 400,
    "max_session_minutes": 90,
    "max_files_touched": 60
  },

  "tracker": {
    "enabled": true,
    "provider": "jira",
    "project_key": "OR"
  }
}
`

// registeredProject plants that config in a fake working copy and registers
// it, returning the working copy's path.
func registeredProject(t *testing.T, home, key, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := registry.Bind(home, registry.Entry{
		Key: key, Source: dir, Workspace: filepath.Join(t.TempDir(), "ws"),
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The common case, and the one a setter that could only UPDATE would refuse:
// the block has no max_concurrent_tickets at all, so the value in force comes
// from config.Defaults() and there is nothing in the file to edit.
func TestSetLimitWritesTheKeyWhenTheBlockLacksIt(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)
	path := filepath.Join(src, "orion.json")

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"max_concurrent_tickets", "4"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}

	if got := config.Load(src).Limits.ConcurrentTickets(); got != 4 {
		t.Fatalf("ConcurrentTickets() = %d after the set, want 4", got)
	}
	if !strings.Contains(readFile(t, path), `"max_concurrent_tickets": 4`) {
		t.Errorf("the key was not written into the file:\n%s", readFile(t, path))
	}
}

// A setter is allowed to touch exactly the field it was asked for. The
// "_comment_*" keys are the reason this is a text patch and not a JSON round
// trip: re-marshalling reorders keys and scatters each comment away from the
// setting it explains.
func TestSetLimitLeavesTheRestOfTheBlockAndItsCommentsAlone(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)
	path := filepath.Join(src, "orion.json")

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"max_concurrent_tickets", "3"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}

	got := readFile(t, path)
	for _, keep := range []string{
		`"_comment_limits": "A limit of 0 restores the default rather than meaning unlimited."`,
		`"max_tool_calls": 400`,
		`"max_session_minutes": 90`,
		`"max_files_touched": 60`,
		`"project_key": "OR"`,
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("the set disturbed %s\n%s", keep, got)
		}
	}
	// The comment must still sit immediately above the block it explains.
	if strings.Index(got, "_comment_limits") > strings.Index(got, `"limits"`) {
		t.Errorf("the comment moved away from its block:\n%s", got)
	}
	cfg := config.Load(src)
	if cfg.Limits.MaxToolCalls != 400 || cfg.Limits.MaxFilesTouched != 60 {
		t.Errorf("neighbouring limits changed: %+v", cfg.Limits)
	}
}

// Asking for forty gets a refusal naming the ceiling, not a file that says
// forty while the watcher runs five. A stored value the reader silently
// overrides is a file disagreeing with behaviour.
func TestSetLimitRefusesAboveTheCeilingRatherThanClampingSilently(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)
	path := filepath.Join(src, "orion.json")
	before := readFile(t, path)

	var out bytes.Buffer
	err := configLimits(home, src, &out, []string{"max_concurrent_tickets", "40"})
	if err == nil {
		t.Fatal("40 was accepted; the ceiling has to be reported, not applied behind the operator")
	}
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("the refusal must name the ceiling, got %q", err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("the file was written despite the refusal:\n%s", got)
	}
}

// The watcher resolves the cap with config.Load(e.Source) -- the user's
// working copy, not the sandbox clone a run executes in. A setter run from
// inside the clone must still write the file the watcher will read, or it
// reports success and changes nothing.
func TestSetLimitWritesTheFileTheWatcherReadsNotTheClone(t *testing.T) {
	home := t.TempDir()
	registeredProject(t, home, "OR", adoptedConfig)

	clone := t.TempDir()
	if err := os.WriteFile(filepath.Join(clone, "orion.json"), []byte(adoptedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cloneBefore := readFile(t, filepath.Join(clone, "orion.json"))

	var out bytes.Buffer
	if err := configLimits(home, clone, &out, []string{"max_concurrent_tickets", "5"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}

	if n, _ := watch.Concurrency(home, []string{"OR"}); n != 5 {
		t.Fatalf("the watcher still reads %d; the setter wrote a file it does not read", n)
	}
	if got := readFile(t, filepath.Join(clone, "orion.json")); got != cloneBefore {
		t.Errorf("the sandbox clone was edited instead of the working copy:\n%s", got)
	}
}

// What the command reports has to be the same number, from the same source
// string, that `orion watch` prints before it spends anything. Two
// implementations of "where did this come from" eventually disagree, and the
// one an operator checks is not the one that decides.
func TestShowReportsTheSameEffectiveValueAndSourceAsTheWatchBanner(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"max_concurrent_tickets", "3"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	out.Reset()
	if err := configLimits(home, src, &out, nil); err != nil {
		t.Fatalf("configLimits: %v", err)
	}

	n, from := watch.Concurrency(home, []string{"OR"})
	var banner bytes.Buffer
	watchBanner(&banner, []string{"OR"}, time.Minute, 1, n, from, false)

	want := atOnceLine(t, banner.String())
	if got := atOnceLine(t, out.String()); got != want {
		t.Fatalf("config limits says %q; the watch banner says %q", got, want)
	}
	if !strings.Contains(want, "3 ticket(s)") || !strings.Contains(want, "max_concurrent_tickets") {
		t.Errorf("neither the value nor its source is legible in %q", want)
	}
}

// Every limit reads as configured unless the command distinguishes what the
// file states from what config.Load fills in afterwards -- which is exactly
// the confusion this ticket is about.
func TestShowSeparatesAConfiguredLimitFromAShippedDefault(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)

	var out bytes.Buffer
	if err := configLimits(home, src, &out, nil); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "max_tool_calls") || !strings.Contains(lineWith(got, "max_tool_calls"), "from orion.json") {
		t.Errorf("a limit the file states must say so:\n%s", got)
	}
	line := lineWith(got, "max_concurrent_tickets")
	if !strings.Contains(line, "default; not set in orion.json") {
		t.Errorf("an absent limit must say the value is the shipped default, got %q", line)
	}
}

// Editing the file under a live watcher does nothing until it restarts. Left
// unsaid, an operator waits for a third agent that is never coming and
// concludes the setting is broken.
func TestSetLimitSaysARunningWatcherKeepsItsOwnValue(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"max_concurrent_tickets", "2"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	if !strings.Contains(out.String(), "restart") {
		t.Errorf("nothing said the change does not reach a running watcher:\n%s", out.String())
	}
}

// The other limits with no surface at all are covered by the same command:
// a setter built for exactly one key is how a second bespoke setter gets
// written next quarter.
func TestSetLimitCoversTheOtherLimitsToo(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"max_session_minutes", "45"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	if got := config.Load(src).Limits.MaxSessionMinutes; got != 45 {
		t.Fatalf("max_session_minutes = %d, want 45", got)
	}

	err := configLimits(home, src, &out, []string{"max_sesion_minutes", "45"})
	if err == nil || !strings.Contains(err.Error(), "max_session_minutes") {
		t.Fatalf("a mistyped key must be refused and the known ones listed, got %v", err)
	}
}

// lineWith returns the first line of out containing s.
func lineWith(out, s string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

// atOnceLine pulls the "at once ..." line out of either rendering, trimmed of
// the indentation and colour that differ between the two.
func atOnceLine(t *testing.T, out string) string {
	t.Helper()
	l := lineWith(out, "at once")
	if l == "" {
		t.Fatalf("no concurrency line in:\n%s", out)
	}
	return strings.TrimSpace(l)
}
