package ui

import (
	"io"
	"os"
	"testing"
)

// `orion watch` printed no colour at all while `orion logs` printed it, for
// the same events, through the same renderer.
//
// The cause was structural, not cosmetic. watch wraps its writer -- Live over
// syncWriter over os.Stdout -- and isTerminal walks the Unwrap chain looking
// for an *os.File. A wrapper without Unwrap ends the walk, the assertion
// fails, and every line the watcher writes is decided to be non-terminal
// output. logs wraps nothing, so it kept its colour, and the two commands
// disagreed about what a run looks like.
//
// This is OR-184's bug one layer out: that fix added Unwrap to syncWriter,
// and when OR-334 replaced the live region with a pass-through stub the
// method was not carried over.
func TestAWrappedTerminalIsStillATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// DevNull is a character device, which is what isTerminal tests for --
	// so it stands in for a terminal without needing a pty.
	if !isTerminal(f) {
		t.Skip("os.DevNull is not a character device on this platform")
	}

	if !isTerminal(NewLive(f)) {
		t.Error("a Live wrapper hides the terminal underneath it, so every line " +
			"orion watch prints loses its colour")
	}
}

// The chain watch actually builds is two deep. One Unwrap is not enough if
// the walk stops at the first wrapper that lacks it, so the test asserts on
// the shape watch uses rather than on a single layer.
func TestTheWalkReachesThroughEveryWrapper(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !isTerminal(f) {
		t.Skip("os.DevNull is not a character device on this platform")
	}

	// Live over an unwrappable middle layer, as internal/watch composes it.
	if !isTerminal(NewLive(&passThrough{w: f})) {
		t.Error("the walk stopped before reaching the file; colour is decided by " +
			"the outermost wrapper rather than by the terminal")
	}
}

// A wrapper that does NOT name what it wraps must still read as not-a-terminal:
// unwrapping is a claim about the thing underneath, and inventing one would be
// worse than the bug it fixes.
func TestAnOpaqueWrapperIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(&opaque{w: f}) {
		t.Error("a writer that does not name what it wraps was treated as a terminal")
	}
}

type passThrough struct{ w io.Writer }

func (p *passThrough) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *passThrough) Unwrap() io.Writer           { return p.w }

type opaque struct{ w io.Writer }

func (o *opaque) Write(b []byte) (int, error) { return o.w.Write(b) }
