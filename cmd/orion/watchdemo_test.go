package main

import (
	"bytes"
	"strings"
	"testing"
)

// The demo must reach every state without a network, a credential or a
// configured project. That is the whole point of it: --dry-run already
// rehearses a tick, and what it cannot do is SHOW the display.
func TestTheDemoRunsWithNoNetworkAndReachesEveryState(t *testing.T) {
	// Wide, so the assertions below read whole lines: the console clips a
	// message to COLUMNS, and a test that asserts on the tail of a long
	// sentence is asserting on the terminal width that ran it.
	t.Setenv("COLUMNS", "200")
	var b bytes.Buffer
	runWatchDemo(&b, []string{"--demo-step=10ms"})
	got := b.String()

	for _, want := range []string{
		"claimed OR-223",       // agents working
		"assembling",           // the batch forming
		"returns to the queue", // an ejection, distinct from a failure
		"waiting on checks",    // testing
		"isolating",            // the search
		"demo complete",        // and it ends rather than looping
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the demo never reached %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "nothing was started and nothing was written") {
		t.Errorf("the demo must say it changed nothing:\n%s", got)
	}
}

// Off a terminal it degrades like everything else: no cursor control, no
// escape codes. A demo that only worked on a TTY could not be checked in CI.
func TestTheDemoDegradesOffATerminal(t *testing.T) {
	var b bytes.Buffer
	runWatchDemo(&b, []string{"--demo-step=10ms"})
	if strings.Contains(b.String(), "\x1b[0J") {
		t.Error("the demo emitted cursor control to a non-terminal")
	}
}

// The demo's whole subject in its second state is the frozen window, and a
// window only demonstrates a cap if something fills it. The first cut wrote
// every line to the ORIGINAL writer rather than to the Live wrapper, so the
// chatter bypassed the window entirely and there was nothing to scroll --
// which is exactly what the display looks like when it is not working.
func TestTheDemoWritesThroughTheLiveWriterSoTheWindowFills(t *testing.T) {
	t.Setenv("COLUMNS", "200")
	var b bytes.Buffer
	runWatchDemo(&b, []string{"--demo-step=400ms"})
	got := b.String()

	// Agent chatter, not just the orchestrator's own lines: this is the
	// stream the window exists to bound.
	var chatter int
	for _, line := range demoChatter {
		if strings.Contains(got, line) {
			chatter++
		}
	}
	if chatter < 3 {
		t.Errorf("only %d chatter line(s) reached the output; the window has nothing to hold:\n%s",
			chatter, got)
	}
	// And it is attributed to a ticket, so the window shows work rather than
	// a bare list of commands.
	if !strings.Contains(got, "OR-223") {
		t.Errorf("the chatter is not attributed to a ticket:\n%s", got)
	}
}
