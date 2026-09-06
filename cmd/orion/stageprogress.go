package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

// Showing a planning stage doing its work.
//
// `orion run --stage spec` printed one configuration warning and then nothing
// at all until the stage ended -- five and a half minutes of silence on a run
// that was working the whole time. It reads as a hang, which is how a working
// command gets killed halfway.
//
// The supervisor has ALWAYS offered this: Options.OnActivity is called for
// every observable thing the agent does. Every work-stage caller passes one,
// which is why `orion watch` narrates; the planning path was simply the one
// caller that never did.
//
// This is deliberately not the live region from internal/ui. That region
// draws a table of concurrent runs and repaints it, which needs a terminal it
// controls. A planning stage is ONE run in an ordinary terminal, so this is
// one line per action, scrolling -- readable when piped to a file, and
// leaving a transcript of what the stage did rather than a display that
// erases itself.

// stageProgress prints what a stage is doing, one line at a time.
type stageProgress struct {
	out   io.Writer
	start time.Time

	mu     sync.Mutex
	lines  int
	lastAt time.Time
}

// maxProgressLines caps the transcript.
//
// A long stage can make thousands of tool calls, and a screen of them buries
// the summary that follows. After the cap it falls back to a heartbeat, which
// answers the only question that still matters by then: is it still going.
const maxProgressLines = 200

// heartbeatEvery is how often the fallback speaks. Long enough not to be
// noise, short enough that silence never looks like death.
const heartbeatEvery = 30 * time.Second

func newStageProgress(out io.Writer) *stageProgress {
	return &stageProgress{out: out, start: time.Now(), lastAt: time.Now()}
}

// On is the supervisor callback.
//
// Called from the process's output goroutine, so it must not block: the agent
// being reported on waits for it. One formatted write, under a mutex held for
// that write only.
func (p *stageProgress) On(a supervisor.Activity) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	if p.lines >= maxProgressLines {
		// Past the cap: a heartbeat, and only when one is due.
		if now.Sub(p.lastAt) < heartbeatEvery {
			return
		}
		p.lastAt = now
		fmt.Fprintf(p.out, "  %s\n", ui.Dim(p.out, fmt.Sprintf(
			"still working -- %s elapsed", now.Sub(p.start).Round(time.Second))))
		return
	}

	line := progressLine(a)
	if line == "" {
		return
	}
	p.lines++
	p.lastAt = now
	fmt.Fprintf(p.out, "  %s %s\n",
		ui.Dim(p.out, now.Sub(p.start).Round(time.Second).String()), line)
}

// progressLine words one activity, or "" for one not worth a line.
func progressLine(a supervisor.Activity) string {
	switch a.Kind {
	case "start":
		// What the run was actually GIVEN, which is the one thing worth
		// saying before any work happens.
		s := "started"
		if a.Model != "" {
			s += " on " + a.Model
		}
		if a.Tools > 0 {
			s += fmt.Sprintf(" with %d tools", a.Tools)
		}
		return s
	case "tool":
		if a.Detail == "" {
			return a.Tool
		}
		return a.Tool + " " + clip(a.Detail, 88)
	case "text":
		// The agent's own words, which is what says WHY it is doing the
		// tool calls around it. One line of it: the full text is in the log.
		if t := firstLine(a.Detail); t != "" {
			return clip(t, 88)
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n-1]) + "…"
}
