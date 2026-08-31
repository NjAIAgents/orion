package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// Qualified keys: the circuit breakers that do not live in the limits block.
//
// A fix-round ceiling bounds a loop that would otherwise run until it ran out
// of money, which is the definition the other nine keys meet, so it belongs
// under this command. It is reached by a QUALIFIED name -- block.field --
// rather than by moving the JSON, which would break every repository that has
// already set qa.max_rounds, in exchange for tidiness.
//
// In their own file rather than appended to configlimits_test.go: these share
// that file's helpers (registeredProject, readFile, lineWith) and adding two
// hundred lines to the middle of it would bury the tests already there.

// qaAndCIConfig is a project that states both fix-round ceilings outright.
const qaAndCIConfig = `{
  "version": 1,

  "_comment_limits": "A limit of 0 restores the default rather than meaning unlimited.",
  "limits": {
    "max_tool_calls": 400,
    "max_files_touched": 60
  },

  "_comment_qa": "max_rounds bounds the findings-fix-reverify exchange.",
  "qa": {
    "enabled": true,
    "max_rounds": 3,
    "e2e_base_url": ""
  },

  "_comment_ci": "max_fix_attempts is the outer bound; the repeat brake stops sooner.",
  "ci": {
    "auto_fix": true,
    "max_fix_attempts": 3
  },

  "tracker": {
    "enabled": true,
    "provider": "jira",
    "project_key": "OR"
  }
}
`

// The gap OR-226 is about: the listing named nine keys and neither fix-round
// ceiling, so an operator told to change one did not find it and concluded it
// was not configurable. Both must appear beside the nine, with the value
// actually in force and where it came from.
func TestShowListsBothFixRoundCeilingsBesideTheNine(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", qaAndCIConfig)

	var out bytes.Buffer
	if err := configLimits(home, src, &out, nil); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	got := out.String()

	// The nine are still there and still named the same. Nothing in the limits
	// block may be renamed or moved by adding these two.
	for _, name := range limitNames() {
		if lineWith(got, name) == "" {
			t.Errorf("the existing limit %q vanished from the listing:\n%s", name, got)
		}
	}
	for _, want := range []string{"qa.max_rounds", "ci.max_fix_attempts"} {
		line := lineWith(got, want)
		if line == "" {
			t.Fatalf("%q is missing from the listing:\n%s", want, got)
		}
		if !strings.Contains(line, "3") {
			t.Errorf("%q did not show its effective value: %q", want, line)
		}
		if !strings.Contains(line, "from orion.json") {
			t.Errorf("%q is set in the file but was not reported as such: %q", want, line)
		}
	}
}

// Provenance for the other half: a project that says nothing about either
// ceiling still runs on one, and the listing has to say the number came from
// the shipped default rather than from the file. Reporting "orion.json" for a
// value that is not in orion.json is what sends someone editing a key that
// does not exist.
func TestShowSeparatesAConfiguredFixCeilingFromAShippedDefault(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig) // no qa or ci block at all

	var out bytes.Buffer
	if err := configLimits(home, src, &out, nil); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	got := out.String()

	for _, want := range []string{"qa.max_rounds", "ci.max_fix_attempts"} {
		line := lineWith(got, want)
		if !strings.Contains(line, "not set in orion.json") {
			t.Errorf("%q is not in the file; it must read as a default: %q", want, line)
		}
		if !strings.Contains(line, strconv.Itoa(config.FixRounds)) {
			t.Errorf("%q must still show the ceiling in force: %q", want, line)
		}
	}
}

// The round trip that matters: what the command writes is what the stage
// reads. A setter that edits a file nobody loads reports success and changes
// nothing.
func TestSettingEachFixCeilingIsReadBackByItsStage(t *testing.T) {
	for _, tc := range []struct {
		name, key, block, value string
		read                    func(config.Config) int
	}{
		{"qa", "qa.max_rounds", "qa", "5", func(c config.Config) int { return c.QA.Rounds() }},
		{"ci", "ci.max_fix_attempts", "ci", "4", func(c config.Config) int { return c.CI.Attempts() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			src := registeredProject(t, home, "OR", qaAndCIConfig)

			var out bytes.Buffer
			if err := configLimits(home, src, &out, []string{tc.key, tc.value}); err != nil {
				t.Fatalf("configLimits: %v", err)
			}
			want, _ := strconv.Atoi(tc.value)
			if got := tc.read(config.Load(src)); got != want {
				t.Fatalf("the stage read %d after setting %s to %s", got, tc.key, tc.value)
			}
			// Old AND new, so a person can tell a change of one from a change
			// of six by reading the line they just caused.
			if !strings.Contains(out.String(), "was 3") {
				t.Errorf("the previous value was not reported:\n%s", out.String())
			}
			// It wrote into the right block. Writing max_rounds into "limits"
			// would be silently inert: nothing reads it there.
			body := readFile(t, filepath.Join(src, "orion.json"))
			if !strings.Contains(blockBody(t, body, tc.block), tc.value) {
				t.Errorf("%s was not written into its own block:\n%s", tc.key, body)
			}
		})
	}
}

// A repository whose orion.json has never carried a "ci" block -- every
// adopted repo, until now -- must still be settable. Answering "add a ci block
// and re-run" would send the operator to hand-edit the file, which is the one
// thing this command exists so nobody has to do.
func TestSettingAFixCeilingCreatesAMissingBlock(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig) // has neither block
	path := filepath.Join(src, "orion.json")

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"ci.max_fix_attempts", "4"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	if got := config.Load(src).CI.Attempts(); got != 4 {
		t.Fatalf("CI.Attempts() = %d after the set, want 4", got)
	}

	// And it left the rest of the file exactly as it was, comments included.
	body := readFile(t, path)
	for _, keep := range []string{
		`"_comment_limits": "A limit of 0 restores the default rather than meaning unlimited."`,
		`"max_tool_calls": 400`,
		`"project_key": "OR"`,
	} {
		if !strings.Contains(body, keep) {
			t.Errorf("creating the block disturbed %s:\n%s", keep, body)
		}
	}
	if cfg := config.Load(src); cfg.Limits.MaxToolCalls != 400 || cfg.Tracker.ProjectKey != "OR" {
		t.Errorf("neighbouring settings changed: %+v", cfg.Limits)
	}
}

// Setting a qualified key must not reach into the limits block. The two live
// in different places on purpose -- that is what the qualified name says --
// and a setter that quietly wrote into limits would produce a file whose value
// nothing reads.
func TestSettingAFixCeilingLeavesTheLimitsBlockAlone(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", qaAndCIConfig)

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"qa.max_rounds", "4"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	limits := blockBody(t, readFile(t, filepath.Join(src, "orion.json")), "limits")
	if strings.Contains(limits, "max_rounds") {
		t.Errorf("qa.max_rounds was written into the limits block:\n%s", limits)
	}
	if !strings.Contains(limits, `"max_tool_calls": 400`) {
		t.Errorf("the limits block was disturbed:\n%s", limits)
	}
}

// The failure that made this gap visible in the first place: `orion config
// limits max_rounds 3` answered "not a limit" and then listed nine keys that
// did not include the thing being asked for. The list has to name every bound
// the command can set, or it sends the reader away confident it is impossible.
func TestAnUnknownKeyListsTheFixCeilingsToo(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)

	var out bytes.Buffer
	err := configLimits(home, src, &out, []string{"max_rounds", "3"})
	if err == nil {
		t.Fatal("an unqualified max_rounds is not a key this command sets; it must be refused")
	}
	for _, want := range []string{"max_concurrent_tickets", "qa.max_rounds", "ci.max_fix_attempts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the known-keys list omits %q: %v", want, err)
		}
	}
}

// Too high is the expensive direction, so a large ceiling is CONFIRMED rather
// than refused -- the answer max_concurrent_tickets settled on, and for the
// same reason: a repository whose failures genuinely take six exchanges to
// converge is not something Orion can tell apart from a typo, and the person
// typing the number can. Unanswered is not a yes.
func TestSettingALargeFixCeilingAsksFirst(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", qaAndCIConfig)
	path := filepath.Join(src, "orion.json")
	before := readFile(t, path)

	confirmIn = strings.NewReader("") // nothing on stdin: the prompt goes unanswered
	t.Cleanup(func() { confirmIn = os.Stdin })

	var out bytes.Buffer
	err := configLimits(home, src, &out, []string{"qa.max_rounds", "40"})
	if err == nil {
		t.Fatal("an unconfirmed 40 was accepted")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(config.FixRoundsWarnAbove)) {
		t.Errorf("the refusal must name the threshold it is above: %v", err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("the file was written despite the prompt going unanswered:\n%s", got)
	}
	if got := config.Load(src).QA.Rounds(); got != 3 {
		t.Errorf("QA.Rounds() = %d; nothing should have changed", got)
	}
}

// And once confirmed it is honoured, not clamped. A stored value the reader
// silently overrides is a file disagreeing with behaviour, with nothing in
// either place explaining the gap.
func TestALargeFixCeilingIsWrittenOnceConfirmed(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", qaAndCIConfig)

	confirmIn = strings.NewReader("y\n")
	t.Cleanup(func() { confirmIn = os.Stdin })

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"ci.max_fix_attempts", "40"}); err != nil {
		t.Fatalf("a confirmed value must be written: %v", err)
	}
	if got := config.Load(src).CI.Attempts(); got != 40 {
		t.Fatalf("CI.Attempts() = %d after confirming 40; it was clamped", got)
	}
}

// The default itself must never prompt. A threshold at or below the shipped
// value would ask a question every time somebody restated the number already
// in force, which is how a prompt becomes something people answer without
// reading.
func TestSettingAFixCeilingToTheDefaultDoesNotPrompt(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)

	confirmIn = strings.NewReader("") // an unanswered prompt would refuse
	t.Cleanup(func() { confirmIn = os.Stdin })

	var out bytes.Buffer
	if err := configLimits(home, src, &out,
		[]string{"qa.max_rounds", strconv.Itoa(config.FixRounds)}); err != nil {
		t.Fatalf("setting the shipped default must not need confirming: %v", err)
	}
}

// Zero means the shipped default here too, and the command has to say so --
// zero could otherwise read as "unlimited" or "no rounds at all".
func TestSettingAFixCeilingToZeroSaysItRestoresTheDefault(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", qaAndCIConfig)

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"qa.max_rounds", "0"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	if !strings.Contains(out.String(), "restores the shipped default") {
		t.Errorf("setting to zero must say it restores the default:\n%s", out.String())
	}
	if got := config.Load(src).QA.Rounds(); got != config.FixRounds {
		t.Errorf("QA.Rounds() = %d after setting zero, want the default %d", got, config.FixRounds)
	}
}

// blockBody returns the text between one top-level block's braces, so a test
// can assert a field landed in the right place rather than merely somewhere in
// the file.
func blockBody(t *testing.T, src, block string) string {
	t.Helper()
	_, after, ok := strings.Cut(src, `"`+block+`": {`)
	if !ok {
		t.Fatalf("no %q block in:\n%s", block, src)
	}
	body, _, ok := strings.Cut(after, "}")
	if !ok {
		t.Fatalf("the %q block never closes in:\n%s", block, src)
	}
	return body
}
