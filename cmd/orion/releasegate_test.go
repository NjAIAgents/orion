package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OR-259. The release gate stands between a merged promotion and a tag, so
// when it fails the repository is in the one state that needs a human
// decision -- and the whole of what the operator got was "exit status 1".
//
// These tests run the gate for real, against modules that are deliberately
// broken one step at a time. That is the only way to prove the wording: the
// gate used to be buried inside release.sh behind a preflight wanting gh, a
// network and a clean tree, which is why nothing covered it.

// gateModule writes a throwaway Go module and returns its directory. Every
// file is given as a name/content pair, so a test breaks exactly one step.
func gateModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{"go.mod": "module gatecanary\n\ngo 1.22\n"}
	for name, content := range files {
		all[name] = content
	}
	for name, content := range all {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runGate runs scripts/release-gate.sh with dir as the working directory, the
// way release.sh runs it from the repository root.
func runGate(t *testing.T, dir string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "release-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The acceptance criterion, proved the way the ticket asked for it: break one
// test on purpose and read what the operator gets.
//
// Two things have to be there. Which step failed -- a four-command gate
// reporting a bare status leaves four guesses (flaky test, parallel-test
// collision, stale build cache, environment difference) that call for
// different responses. And enough of the output to tell them apart, because
// `go test ./... >/dev/null` threw away the evidence at the exact moment it
// existed, and by the time anyone re-ran it the tree was green again.
func TestTheGateNamesTheFailingTestStepAndShowsItsOutput(t *testing.T) {
	dir := gateModule(t, map[string]string{
		"m.go": "package gatecanary\n",
		"m_test.go": `package gatecanary

import "testing"

func TestDeliberatelyBroken(t *testing.T) {
	t.Fatal("GATE-CANARY-a-transient-failure")
}
`,
	})

	out, err := runGate(t, dir)
	if err == nil {
		t.Fatalf("a red test suite must fail the gate:\n%s", out)
	}
	if !strings.Contains(out, "FAILED: go test") {
		t.Errorf("the gate does not name the step that failed, so the operator is "+
			"back to guessing which of four commands it was:\n%s", out)
	}
	if !strings.Contains(out, "GATE-CANARY-a-transient-failure") {
		t.Errorf("the failing test's own output was discarded, which is the whole of "+
			"OR-259: a release stops, the evidence exists for one second, and nobody "+
			"can reconstruct it afterwards:\n%s", out)
	}
	if !strings.Contains(out, "TestDeliberatelyBroken") {
		t.Errorf("the output does not name the test that failed:\n%s", out)
	}
}

// The other three steps named themselves before this ticket and must keep
// doing so. Table rather than one test because the property is the gate's,
// not any one command's: whichever step fails, its name is on the line above
// its output.
func TestTheGateNamesWhicheverStepFails(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		step  string
		want  string
	}{
		{
			name:  "a build error",
			files: map[string]string{"m.go": "package gatecanary\n\nfunc X() { return 1 }\n"},
			step:  "FAILED: go build",
			want:  "too many return values",
		},
		{
			name: "an unformatted file",
			// Formatted enough to build and vet cleanly, so this reaches the
			// gofmt step rather than dying before it.
			files: map[string]string{"m.go": "package gatecanary\n\nfunc  X()  {}\n"},
			step:  "FAILED: gofmt",
			want:  "gofmt would change",
		},
		{
			name: "a vet finding",
			files: map[string]string{"m.go": `package gatecanary

import "fmt"

func X() string { return fmt.Sprintf("%d", "not a number") }
`},
			step: "FAILED: go vet",
			want: "Sprintf",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := runGate(t, gateModule(t, c.files))
			if err == nil {
				t.Fatalf("the gate passed a tree it should have refused:\n%s", out)
			}
			if !strings.Contains(out, c.step) {
				t.Errorf("want the gate to name %q:\n%s", c.step, out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("want the step's own output, containing %q:\n%s", c.want, out)
			}
		})
	}
}

// The other half of the fix, and the easier half to lose: keeping the failing
// output means nothing if a passing run now buries the release in a thousand
// lines of test output. Success says one thing, exactly as it did before.
func TestASuccessfulGateStaysQuiet(t *testing.T) {
	dir := gateModule(t, map[string]string{
		"m.go": "package gatecanary\n\nfunc X() int { return 1 }\n",
		"m_test.go": `package gatecanary

import "testing"

func TestX(t *testing.T) {
	if X() != 1 {
		t.Fatal("no")
	}
}
`,
	})

	out, err := runGate(t, dir)
	if err != nil {
		t.Fatalf("a clean tree must pass the gate: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "all green" {
		t.Errorf("a successful gate must stay as quiet as it was, got:\n%s", out)
	}
}

// A cached pass and a fresh failure are indistinguishable after the fact, and
// a release is the wrong place to be reading a cache. A source check because
// proving the absence of caching end to end would mean poisoning GOCACHE,
// which is slower and less direct than reading the one flag that matters.
func TestTheGateRunsTestsWithoutTheCache(t *testing.T) {
	src := repoFile(t, "scripts", "release-gate.sh")
	if !strings.Contains(src, "go test -count=1 ./...") {
		t.Error("the release gate runs `go test` without -count=1, so a green gate can " +
			"be a cached green from an hour ago against a tree that has since moved")
	}
	// Comment lines excluded: the script's header quotes the old command as
	// the thing that went wrong, and a check that cannot tell a warning from
	// the mistake it warns about would fire on the fix.
	for i, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, ">/dev/null") {
			t.Errorf("line %d discards a gate step's output, which is OR-259 exactly: "+
				"the slow step is the only one likely to fail non-obviously, and it was "+
				"the only one whose evidence was thrown away: %s", i+1, strings.TrimSpace(line))
		}
	}
}

// release.sh must keep delegating to the gate script rather than growing its
// own copy. Two gates drift, and the direction they drift in is the one that
// discards output again.
func TestTheReleaseScriptDelegatesToTheGate(t *testing.T) {
	src := repoFile(t, "scripts", "release.sh")
	if !strings.Contains(src, "release-gate.sh") {
		t.Error("scripts/release.sh no longer calls scripts/release-gate.sh, so the gate " +
			"the release actually runs is not the gate any test covers")
	}
}

// The output must reach the event log, not only the terminal. The terminal is
// gone by the time anyone asks why a release stopped; the log is what they
// open.
func TestAFailedReleaseScriptCarriesItsOutput(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "release.sh")
	if err := os.WriteFile(script, []byte(
		"#!/usr/bin/env bash\necho '    FAILED: go test -count=1 ./...'\n"+
			"echo 'GATE-CANARY-on-stderr' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := releaseScript(root, "v9.9.9")
	if err == nil {
		t.Fatal("a script exiting 1 must be an error")
	}
	var f *scriptFailure
	if !errors.As(err, &f) {
		t.Fatalf("the failure does not carry the script's output, so the event log gets "+
			"a bare status: %v", err)
	}
	// Both streams, because the gate writes its naming line and the captured
	// output to stderr while the script's own progress goes to stdout.
	if !strings.Contains(f.output, "FAILED: go test") {
		t.Errorf("stdout was not kept: %q", f.output)
	}
	if !strings.Contains(f.output, "GATE-CANARY-on-stderr") {
		t.Errorf("stderr was not kept, and that is where the gate reports: %q", f.output)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("the wrapped error must still say what the exit status was: %v", err)
	}
}

// A cross-compile and a full test run print more than belongs in a log line,
// so the copy is bounded -- and it keeps the END, which is where the reason
// is.
func TestTheKeptOutputIsBoundedAndKeepsTheEnd(t *testing.T) {
	var tail tailWriter
	for i := 0; i < 400; i++ {
		if _, err := tail.Write([]byte(strings.Repeat("noise\n", 20))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tail.Write([]byte("GATE-CANARY-the-reason\n")); err != nil {
		t.Fatal(err)
	}
	got := tail.String()
	if len(got) > gateTailBytes {
		t.Errorf("kept %d bytes, over the %d-byte ceiling", len(got), gateTailBytes)
	}
	if !strings.HasSuffix(got, "GATE-CANARY-the-reason") {
		t.Errorf("the end was dropped instead of the beginning: %q", got[max(0, len(got)-80):])
	}
}

// OR-256 made a refused release leave a record. OR-259 is that principle one
// level down: the record has to say why, or it records that something
// happened and nothing about what.
func TestTheStoppedEventCarriesTheGateOutput(t *testing.T) {
	failed := &scriptFailure{
		err:    errors.New("exit status 1"),
		output: "    FAILED: go test -count=1 ./...\n--- FAIL: TestThing\n    GATE-CANARY",
	}
	ev := stoppedEvent("v0.8.9", failed)

	if !strings.Contains(ev.Msg, "v0.8.9") || !strings.Contains(ev.Msg, "promotion merged") {
		t.Errorf("the message no longer names the state the repository is in: %q", ev.Msg)
	}
	if strings.Contains(ev.Msg, "GATE-CANARY") {
		t.Error("the output is inlined into the message; a log being scanned should stay " +
			"one line per event, with the evidence in Detail for whoever opens it")
	}
	got, _ := ev.Detail["output"].(string)
	if !strings.Contains(got, "GATE-CANARY") {
		t.Errorf("the gate's output did not reach the event, so the only account of the "+
			"failure that outlives the terminal is still a bare status: %#v", ev.Detail)
	}

	// A failure with no output of its own -- an interrupt, a failed checkout
	// already summarised by firstLineOf -- must not grow an empty field.
	plain := stoppedEvent("v0.8.9", errors.New("interrupted during the cut"))
	if plain.Detail != nil {
		t.Errorf("an errorless-output failure gained a Detail field: %#v", plain.Detail)
	}
	if !strings.Contains(plain.Msg, "interrupted during the cut") {
		t.Errorf("the message lost the reason: %q", plain.Msg)
	}
}
