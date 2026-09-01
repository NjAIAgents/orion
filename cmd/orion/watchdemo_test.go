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
