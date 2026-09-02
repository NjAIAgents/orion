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
// gateScript is the absolute path to the gate, so a test can run it from a
// temporary module of its own.
func gateScript(t *testing.T) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "release-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func runGate(t *testing.T, dir string) (string, error) {
	t.Helper()
	script := gateScript(t)
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// OR-259's acceptance criterion, applied to the step that now stands where
// the test run used to.
//
// The gate no longer runs the suite: CI runs it on three platforms for every
// push, and running it again here added only this machine's flakiness --
// v0.8.10 failed four times on four such artefacts and none on a real defect.
// What the gate asks now is whether CI passed on the exact commit being
// tagged, which is the stronger claim.
//
// OR-259's rule is unchanged and is what this test still proves: a gate that
// stops must say WHICH step stopped it and WHY. A bare status leaves the
// operator guessing, and the evidence exists for one second.
func TestTheGateNamesTheFailingCIStepAndSaysWhy(t *testing.T) {
	// A module with no git repository at all: `git rev-parse HEAD` fails, so
	// there is no commit to ask CI about. That is the same refusal path as an
	// unbuilt commit and needs no network.
	dir := gateModule(t, map[string]string{"m.go": "package gatecanary\n"})

	out, err := runGate(t, dir)
	if err == nil {
		t.Fatalf("a commit CI has not reported on must fail the gate:\n%s", out)
	}
	if !strings.Contains(out, "FAILED: CI green for HEAD") {
		t.Errorf("the gate does not name the step that failed, so the operator is "+
			"back to guessing which of four commands it was:\n%s", out)
	}
	// The words matter as much as the refusal: "nothing reported" and "CI
	// said failure" call for different responses, and so does "gh is not
	// installed" -- which must never be mistaken for a green build.
	if !strings.Contains(out, "CI has reported nothing") &&
		!strings.Contains(out, "gh is not installed") {
		t.Errorf("the gate refused without saying why, which is the whole of OR-259:\n%s", out)
	}
}

// The escape hatch is LOUD. A release that skipped its only verification must
// say so on the way past, or the flag becomes a habit nobody remembers using.
func TestSkippingTheCICheckIsAnnounced(t *testing.T) {
	dir := gateModule(t, map[string]string{"m.go": "package gatecanary\n"})

	cmd := exec.Command("bash", gateScript(t))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ORION_SKIP_CI_CHECK=1")
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("the skip must let the gate pass, not merely change its message:\n%s", out)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "without a CI verdict") {
		t.Errorf("a gate that shipped without verification must say so:\n%s", out)
	}
}

// The other three steps named themselves before this ticket and must keep
// doing so.// The other three steps named themselves before this ticket and must keep
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

// The failure message must name the actual unformatted file, not just the
// word "gofmt" -- "gofmt would change" alone still leaves the operator
// grepping a possibly-large tree for what to run `gofmt -w` on.
func TestTheGofmtFailureListsTheUnformattedFile(t *testing.T) {
	out, err := runGate(t, gateModule(t, map[string]string{
		"m.go": "package gatecanary\n\nfunc  X()  {}\n",
	}))
	if err == nil {
		t.Fatalf("the gate passed a tree it should have refused:\n%s", out)
	}
	if !strings.Contains(out, "m.go") {
		t.Errorf("the gofmt failure does not name which file is unformatted:\n%s", out)
	}
}

// The gate must stop at the first failing step rather than running the rest
// and reporting only the last. A build error means the tree cannot even be
// vetted or tested, so a gate that pressed on would either crash confusingly
// in a later step or -- worse -- silently skip it and still report only one
// failure, hiding that the later steps never ran at all.
func TestTheGateStopsAtTheFirstFailure(t *testing.T) {
	dir := gateModule(t, map[string]string{
		// Fails to build AND is unformatted, so if the gate did not stop at
		// go build, gofmt would also fail and be reported.
		"m.go": "package gatecanary\n\nfunc  X()  { return 1 }\n",
	})

	out, err := runGate(t, dir)
	if err == nil {
		t.Fatalf("the gate passed a tree it should have refused:\n%s", out)
	}
	if !strings.Contains(out, "FAILED: go build") {
		t.Fatalf("want the build failure reported:\n%s", out)
	}
	if strings.Contains(out, "FAILED: go vet") || strings.Contains(out, "FAILED: gofmt") ||
		strings.Contains(out, "FAILED: go test") {
		t.Errorf("the gate ran a step past the first failure, so a build error no longer "+
			"tells the operator the tree didn't even compile:\n%s", out)
	}
}

// The other half of the fix, and the easier half to lose: keeping the failing
// output means nothing if a passing run now buries the release in a thousand
// lines of test output. Success says one thing, exactly as it did before.
// A clean tree passes quietly. The CI check is skipped here on purpose: this
// test is about the gate's VOLUME on success, and a temporary module has no
// commit for CI to have an opinion about.
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

	cmd := exec.Command("bash", gateScript(t))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ORION_SKIP_CI_CHECK=1")
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("a clean tree must pass the gate: %v\n%s", err, out)
	}
	// The skip's own warning is expected and is asserted by
	// TestSkippingTheCICheckIsAnnounced; what this test cares about is that
	// nothing ELSE is printed on success.
	quiet := strings.TrimSpace(strings.ReplaceAll(out, "\n", " "))
	if !strings.Contains(quiet, "all green") {
		t.Errorf("a successful gate must say it passed, got:\n%s", out)
	}
	if strings.Count(out, "\n") > 3 {
		t.Errorf("a successful gate must stay as quiet as it was, got:\n%s", out)
	}
}

// The gate VERIFIES CI rather than re-running the suite, and refuses when
// there is no verdict to read.
//
// A source check because the property is the script's shape: it must ask CI
// about the exact commit, and it must treat silence as a refusal. Proving
// that end to end would mean standing up a fake forge, which is slower and
// less direct than reading the three lines that matter.
//
// It replaces a check that the gate ran `go test -count=1`. That was the
// right contract while the gate ran the suite; it ran it a third time after
// CI had already done so on three platforms, and every one of v0.8.10's four
// gate failures was an artefact of running it here rather than a defect.
func TestTheGateVerifiesCIRatherThanRunningTheSuite(t *testing.T) {
	src := repoFile(t, "scripts", "release-gate.sh")

	if !strings.Contains(src, "gh run list --commit") {
		t.Error("the gate does not ask CI about the commit being tagged")
	}
	// Silence is a refusal. An unbuilt commit is exactly what this gate
	// exists to stop, so "no answer" must never read as "green".
	if !strings.Contains(src, "CI has reported nothing") {
		t.Error("the gate does not refuse a commit CI has said nothing about")
	}
	// And a missing gh is a refusal too, for the same reason.
	if !strings.Contains(src, "gh is not installed") {
		t.Error("a missing gh could be mistaken for a green build")
	}

	// Comment lines excluded: the script's header quotes the old command as
	// the thing that went wrong, and a check that cannot tell a warning from
	// the mistake it warns about would fire on the fix.
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "go test") {
			t.Errorf("line %d still runs the suite locally, which is the twenty minutes "+
				"this change removed: %s", i+1, trimmed)
		}
	}
}

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
