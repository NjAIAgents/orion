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
//
// ON A TERMINAL the most recent line is LIVE: redrawn in place a few times a
// second with the working glyph turning and the elapsed time moving, so a
// silent generation still visibly moves. When the next activity arrives the
// live line is committed to the transcript behind a ✓ -- that action ended
// -- and the new one takes its place. Off a terminal every line is committed
// as it happens and the heartbeat speaks instead, which is the transcript a
// piped log wants.
type stageProgress struct {
	out   io.Writer
	start time.Time
	tty   bool

	mu     sync.Mutex
	lines  int
	lastAt time.Time
	// live is the current line's text (after the icon and elapsed), and
	// liveAt when it began; frame is the spinner's position.
	live   string
	liveAt time.Time
	frame  int

	done chan struct{}
	stop sync.Once

	// prevConsole and consoleWas restore the console writer on Close: while
	// the live line is up, every message another package sends to the
	// terminal goes through this progress so the line is cleared first and
	// redrawn after, instead of being written into the middle of it.
	prevConsole io.Writer
	consoleWas  bool
}

// spinEvery is how often the live line is redrawn on a terminal.
const spinEvery = 250 * time.Millisecond

// clearLine returns to the start of the line and erases it: how the live
// line is redrawn without scrolling.
const clearLine = "\r\033[2K"

// maxProgressLines caps the transcript.
//
// A long stage can make thousands of tool calls, and a screen of them buries
// the summary that follows. After the cap it falls back to a heartbeat, which
// answers the only question that still matters by then: is it still going.
const maxProgressLines = 200

// heartbeatEvery is how often the ticker speaks while nothing is happening.
//
// A long generation makes NO tool calls: the agent says "writing the plan
// now" and then composes a thousand-line document in one turn. Nine minutes
// of that is indistinguishable from a hang, and a hang is what it gets
// treated as -- the run gets killed a minute before it would have landed.
//
// So the heartbeat is driven by a TIMER, not by activity. Driving it from the
// activity callback was the first attempt and it cannot work: the callback is
// exactly what silence means the absence of.
const heartbeatEvery = 30 * time.Second

func newStageProgress(out io.Writer) *stageProgress {
	return newStageProgressTTY(out, ui.IsTerminal(out))
}

// newStageProgressTTY is newStageProgress with the terminal decision made
// by the caller, which is what lets a test drive the live line into a buffer.
func newStageProgressTTY(out io.Writer, tty bool) *stageProgress {
	p := &stageProgress{out: out, start: time.Now(), lastAt: time.Now(), tty: tty, done: make(chan struct{})}
	if tty {
		p.consoleWas, p.prevConsole = ui.ConsoleEngaged(), ui.Console()
		ui.SetConsole(consoleThrough{p})
		p.live, p.liveAt = "starting", p.start
		p.redraw()
	}
	go p.tick()
	return p
}

// consoleThrough is the console writer while a live line is up: clear the
// line, write the message on its own line, put the live line back.
type consoleThrough struct{ p *stageProgress }

func (c consoleThrough) Write(b []byte) (int, error) {
	c.p.mu.Lock()
	defer c.p.mu.Unlock()
	if c.p.live != "" {
		fmt.Fprint(c.p.prevConsole, clearLine)
	}
	n, err := c.p.prevConsole.Write(b)
	if len(b) > 0 && b[len(b)-1] != '\n' {
		fmt.Fprintln(c.p.prevConsole)
	}
	if c.p.live != "" {
		c.p.redraw()
	}
	return n, err
}

// tick says the run is alive while nothing is happening.
//
// Stops when Close is called. A goroutine per stage, and stages are
// sequential, so there is one of these at a time.
func (p *stageProgress) tick() {
	every := heartbeatEvery
	if p.tty {
		every = spinEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case now := <-t.C:
			p.mu.Lock()
			if p.tty {
				// The live line IS the heartbeat: its glyph turns and its
				// elapsed time moves whether or not anything happened.
				p.frame++
				p.redraw()
				p.mu.Unlock()
				continue
			}
			quiet := now.Sub(p.lastAt)
			if quiet >= heartbeatEvery {
				p.lastAt = now
				fmt.Fprintf(p.out, "  %s\n", ui.Dim(p.out, fmt.Sprintf(
					"still working -- %s elapsed, quiet %s",
					now.Sub(p.start).Round(time.Second), quiet.Round(time.Second))))
			}
			p.mu.Unlock()
		}
	}
}

// Close stops the heartbeat, and on a terminal commits the live line so the
// transcript keeps it and the outcome line that follows starts clean. Safe
// to call more than once.
func (p *stageProgress) Close() {
	p.stop.Do(func() {
		close(p.done)
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.tty && p.live != "" {
			p.commit()
		}
		if p.tty {
			if p.consoleWas {
				ui.SetConsole(p.prevConsole)
			} else {
				ui.SetConsole(nil)
			}
		}
	})
}

// redraw paints the live line in place. Caller holds the mutex.
func (p *stageProgress) redraw() {
	fmt.Fprintf(p.out, "%s  %s%s %s", clearLine, ui.Spinner(p.out, p.frame),
		ui.Dim(p.out, time.Since(p.start).Round(time.Second).String()), p.live)
}

// commit turns the live line into a transcript line behind a ✓: the action
// it named has ended. Caller holds the mutex.
func (p *stageProgress) commit() {
	fmt.Fprintf(p.out, "%s  %s%s %s\n", clearLine, ui.Icon(p.out, ui.VerbOK),
		ui.Dim(p.out, p.liveAt.Sub(p.start).Round(time.Second).String()), p.live)
	p.live = ""
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
	// Past the cap the ticker carries on alone: it is already saying the run
	// is alive, which is the only question left once the transcript is this
	// long.
	if p.lines >= maxProgressLines {
		p.lastAt = now
		return
	}

	line := progressLine(a)
	if line == "" {
		return
	}
	p.lines++
	p.lastAt = now
	if p.tty {
		if p.live != "" {
			p.commit()
		}
		p.live, p.liveAt = line, now
		p.redraw()
		return
	}
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
