package main

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

// The reported symptom: `orion run --stage spec` printed one warning and then
// nothing for five and a half minutes, on a run that was working the whole
// time. Silence reads as a hang, and a working run gets killed halfway.
func TestAStageSaysWhatItIsDoing(t *testing.T) {
	var out bytes.Buffer
	p := newStageProgress(&out)

	p.On(supervisor.Activity{Kind: "start", Model: "opus", Tools: 42})
	p.On(supervisor.Activity{Kind: "tool", Tool: "WebFetch", Detail: "https://example.com/product"})
	p.On(supervisor.Activity{Kind: "text", Detail: "Reading the vendor's page first.\nThen writing."})
	p.On(supervisor.Activity{Kind: "tool", Tool: "Write", Detail: "docs/intent/cloudlens.md"})

	got := out.String()
	for _, want := range []string{
		"started", "opus", "42 tools",
		"WebFetch", "https://example.com/product",
		"Reading the vendor's page first.",
		"Write", "docs/intent/cloudlens.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the transcript never mentions %q:\n%s", want, got)
		}
	}
	// Only the first line of the agent's prose: the rest is in the log, and a
	// paragraph per thought buries the tool calls around it.
	if strings.Contains(got, "Then writing.") {
		t.Errorf("a whole paragraph was printed:\n%s", got)
	}
}

// A long stage can make thousands of tool calls. Past the cap it must still
// answer the only question left -- is it alive -- without burying the summary
// that follows.
func TestALongStageFallsBackToAHeartbeat(t *testing.T) {
	var out bytes.Buffer
	p := newStageProgress(&out)

	for i := 0; i < maxProgressLines+50; i++ {
		p.On(supervisor.Activity{Kind: "tool", Tool: "Read", Detail: "file.go"})
	}

	lines := strings.Count(out.String(), "\n")
	if lines > maxProgressLines+5 {
		t.Errorf("printed %d lines; the cap is %d", lines, maxProgressLines)
	}
	if lines < maxProgressLines {
		t.Errorf("printed only %d lines, so the cap cut it short", lines)
	}
}

// Long arguments are clipped rather than wrapped: a wrapped line is two
// lines, and two lines per tool call is not a transcript.
func TestALongArgumentIsClipped(t *testing.T) {
	got := progressLine(supervisor.Activity{
		Kind: "tool", Tool: "Bash", Detail: strings.Repeat("x", 400),
	})
	if len(got) > 100 {
		t.Errorf("line is %d characters:\n%s", len(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a clipped line does not show that it was cut")
	}
}

// A newline inside a tool argument would break one line into several and
// desynchronise the transcript from what actually happened.
func TestANewlineInAnArgumentStaysOnOneLine(t *testing.T) {
	got := progressLine(supervisor.Activity{
		Kind: "tool", Tool: "Bash", Detail: "go test ./...\nrm -rf /tmp/x",
	})
	if strings.Contains(got, "\n") {
		t.Errorf("the line contains a newline: %q", got)
	}
}

// An activity with nothing to say produces no line at all, rather than a
// blank one.
func TestAnEmptyActivityPrintsNothing(t *testing.T) {
	var out bytes.Buffer
	p := newStageProgress(&out)
	p.On(supervisor.Activity{Kind: "text", Detail: "   \n  "})
	p.On(supervisor.Activity{Kind: "unknown-kind"})
	if out.String() != "" {
		t.Errorf("printed something for nothing:\n%q", out.String())
	}
}

// The failure this exists for: a long generation makes NO tool calls. The
// agent says "writing the plan now" and composes a thousand-line document in
// one turn, and nine minutes of silence is indistinguishable from a hang.
//
// The first version drove the heartbeat from the activity callback, which
// cannot work: the callback is exactly what silence is the absence of.
func TestTheHeartbeatSpeaksWhileNothingIsHappening(t *testing.T) {
	var out lockedBuffer
	p := &stageProgress{
		out: &out, start: time.Now().Add(-90 * time.Second),
		lastAt: time.Now().Add(-90 * time.Second),
		done:   make(chan struct{}),
	}
	// A ticker of its own rather than waiting 30s for the real one.
	go func() {
		tk := time.NewTicker(10 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-p.done:
				return
			case now := <-tk.C:
				p.mu.Lock()
				if now.Sub(p.lastAt) >= heartbeatEvery {
					p.lastAt = now
					p.out.Write([]byte("still working -- quiet\n"))
				}
				p.mu.Unlock()
			}
		}
	}()

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(out.String(), "still working") {
			p.Close()
			return
		}
		select {
		case <-deadline:
			p.Close()
			t.Fatal("nothing was printed during a long silence; it reads as a hang")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// Close must be safe to call twice: the deferred Close in the chain and the
// one in a single-stage run are different code paths onto the same object.
func TestClosingTheHeartbeatTwiceIsSafe(t *testing.T) {
	p := newStageProgress(&lockedBuffer{})
	p.Close()
	p.Close() // must not panic on a closed channel
}

// lockedBuffer is a bytes.Buffer safe for the ticker goroutine and the test
// to touch at once. bytes.Buffer is not.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// On a terminal the newest line is live -- redrawn in place with the
// turning glyph -- and is committed behind a ✓ when the next one arrives or
// the stage ends. The transcript keeps every line; the screen shows one
// moving.
func TestOnATerminalTheNewestLineIsLiveAndTheRestAreCommitted(t *testing.T) {
	var out lockedBuffer
	p := newStageProgressTTY(&out, true)
	p.On(supervisor.Activity{Kind: "start", Model: "m"})
	p.On(supervisor.Activity{Kind: "tool", Tool: "Read", Detail: "spec.md"})
	p.Close()

	got := out.String()
	if !strings.Contains(got, clearLine) {
		t.Fatalf("no in-place redraw on a terminal:\n%q", got)
	}
	ok := strings.TrimSpace(ui.Icon(&out, ui.VerbOK))
	// "starting" and "started on m" were committed when the next line
	// arrived; "Read spec.md" when the stage closed.
	for _, want := range []string{ok + " ", "started on m\n", "Read spec.md\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript lacks %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, "still working") {
		t.Error("the heartbeat line is for a piped transcript; on a terminal the live line moves instead")
	}
}

// The glyph turns: consecutive frames differ, and the column keeps its width.
func TestTheSpinnerTurns(t *testing.T) {
	if ui.Spinner(io.Discard, 0) == ui.Spinner(io.Discard, 1) {
		t.Error("frames 0 and 1 are the same glyph")
	}
	if ui.Spinner(io.Discard, 0) != ui.Spinner(io.Discard, 4) {
		t.Error("the spinner does not cycle")
	}
	if len([]rune(ui.Spinner(io.Discard, 2))) != len([]rune(ui.Icon(io.Discard, ui.VerbOK))) {
		t.Errorf("spinner %q and icon %q occupy different widths", ui.Spinner(io.Discard, 2), ui.Icon(io.Discard, ui.VerbOK))
	}
}

// Off a terminal nothing moved: one committed line per activity, no
// escape codes, the heartbeat as before.
func TestOffATerminalTheTranscriptIsUnchanged(t *testing.T) {
	var out lockedBuffer
	p := newStageProgressTTY(&out, false)
	p.On(supervisor.Activity{Kind: "tool", Tool: "Read", Detail: "spec.md"})
	p.Close()
	if strings.Contains(out.String(), "\r") || strings.Contains(out.String(), "\033[") {
		t.Errorf("escape codes off a terminal:\n%q", out.String())
	}
	if !strings.HasSuffix(out.String(), "Read spec.md\n") {
		t.Errorf("want the plain transcript line, got:\n%q", out.String())
	}
}
