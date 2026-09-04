package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Replays a watch against a simulated terminal and counts the frame's top
// border. There must be exactly one on screen: every redraw erases the block
// it drew, and a border left behind means the erase counted short (OR-317).
//
// The writes are shaped like the real ones -- stage lines, a two-line
// warning whose second line is a bare path, lines longer than the frame --
// and the rows are built to fill the terminal exactly, which is the width
// where a miscount is most likely.
func TestNoTopBorderIsEverStrandedInScrollback(t *testing.T) {
	for _, cols := range []int{80, 120, 200} {
		t.Run(fmt.Sprintf("%dcols", cols), func(t *testing.T) {
			t.Setenv("COLUMNS", fmt.Sprint(cols))
			t.Setenv("LINES", "40")
			LiveReset()
			t.Cleanup(LiveReset)

			sim := newTermSim(cols, 40)
			l := &Live{w: sim, cursor: true}
			now := time.Now()
			for i, k := range []string{"OR-295", "OR-297", "OR-300", "OR-301"} {
				liveStart(k, now.Add(-time.Duration(30+i)*time.Minute))
				liveStage(k, "qa", "brandon")
				LiveTitle(k, "Rename internal/njagents to internal/toolkit with no behaviour change")
				LiveActivityNote(k, "brandon", "Bash cd /Users/navjyotnishant/.orion/projects/orion-83d87b/worktrees/orion-or-295 git status --porcelain && go test ./...")
				for j := 0; j < 5; j++ {
					LiveAgents(k)
				}
			}
			LiveSpend(43.01)
			LiveBatchStart("orion/batch", "develop", []string{"OR-300"})
			LiveBatchMedian(time.Second)
			liveBatchPhase(BatchTesting, now.Add(-7*time.Minute))

			redraw := func() {
				l.lock.Lock()
				l.eraseLocked()
				l.drawLocked()
				l.lock.Unlock()
			}
			lines := []string{
				"19:09:09 OR-295   ✓  ok       orion                      -        nothing in this change touches the data model, so there is no database review to pay for",
				"19:09:09 OR-295   ══ stage ══ implementing → qa  Mahesh · backend developer hands to Brandon · QA engineer; 1 commit(s) on orion/or-295",
				"WARNING   the worktree is dirty\n           /Users/navjyotnishant/.orion/projects/orion-83d87b/worktrees/orion-or-295",
				"19:09:38 OR-295   ◐  working  Brandon · QA engineer      sonnet   writing 32 case(s) across 5 authors",
				strings.Repeat("x", cols),   // exactly the terminal's width
				strings.Repeat("y", cols+1), // one past it
				"waiting   the batch is green; waiting on approval (2m0s ago) -- nobody has approved it yet",
			}
			for round := 0; round < 6; round++ {
				for _, line := range lines {
					fmt.Fprintln(l, line)
					redraw()
					redraw()
				}
			}
			if n := sim.count("recent "); n != 1 {
				t.Errorf("%d top borders on screen, want 1:\n%s", n, sim.screen())
			}
			if n := sim.count("scrolls, then gone"); n != 1 {
				t.Errorf("%d bottom borders on screen, want 1", n)
			}
		})
	}
}

// The same replay with the width UNKNOWN: no COLUMNS, and no terminal to ask
// (a test process has none, which is also what a failed `stty size` looks
// like for the second until the next poll). The terminal is still 120 wide
// and still wraps; the erase has to count for that or the top of the block
// is left in scrollback on every redraw.
func TestAnUnknownWidthDoesNotStrandTheFrame(t *testing.T) {
	t.Setenv("COLUMNS", "")
	t.Setenv("LINES", "")
	invalidateTerminalSize()
	LiveReset()
	t.Cleanup(LiveReset)

	sim := newTermSim(120, 40)
	l := &Live{w: sim, cursor: true}
	now := time.Now()
	for i, k := range []string{"OR-295", "OR-297", "OR-300", "OR-301"} {
		liveStart(k, now.Add(-time.Duration(30+i)*time.Minute))
		liveStage(k, "qa", "brandon")
		LiveTitle(k, "Rename internal/njagents to internal/toolkit with no behaviour change")
		LiveActivityNote(k, "brandon", "Bash cd /Users/navjyotnishant/.orion/projects/orion-83d87b/worktrees/orion-or-295 git status --porcelain && go test ./...")
	}
	redraw := func() {
		l.lock.Lock()
		l.eraseLocked()
		l.drawLocked()
		l.lock.Unlock()
	}
	for round := 0; round < 6; round++ {
		fmt.Fprintln(l, "19:09:38 OR-295   ◐  working  Brandon · QA engineer      sonnet   writing 32 case(s) across 5 authors")
		redraw()
	}
	if n := sim.count("recent "); n != 1 {
		t.Errorf("%d top borders on screen with the width unknown, want 1:\n%s", n, sim.screen())
	}
}

// A poll that fails keeps the last size rather than forgetting it: the
// terminal did not change width because stty could not be forked once.
func TestAFailedSizeReadKeepsTheLastKnownSize(t *testing.T) {
	sttyOnce.Lock()
	sttyRowsCached, sttyColsCached, sttyAsked = 40, 132, true
	sttyOnce.Unlock()
	t.Cleanup(func() {
		sttyOnce.Lock()
		sttyRowsCached, sttyColsCached, sttyAsked = 0, 0, false
		sttyOnce.Unlock()
	})
	invalidateTerminalSize()
	// No terminal in a test process, so the re-read fails.
	if rows, cols := sttySize(); rows != 40 || cols != 132 {
		t.Errorf("a failed re-read forgot the size: got %dx%d, want 40x132", cols, rows)
	}
}
