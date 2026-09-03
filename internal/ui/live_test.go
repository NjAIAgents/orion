package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// A run set up by hand, so a test can place it anywhere in its lifetime
// without waiting for one. Everything the region renders is derived from
// these fields, which is the point of keeping the renderer a pure function of
// a snapshot.
func run(key, actor, stage string, started time.Time) *liveRun {
	return &liveRun{
		key: key, actor: actor, stage: stage, started: started,
		newest: started.UnixNano() / int64(sparkBucket),
	}
}

func stateOf(now time.Time, runs ...*liveRun) liveState {
	st := liveState{}
	for _, r := range runs {
		st.rows = append(st.rows, *r)
	}
	return st
}

func regionOf(t *testing.T, st liveState, now time.Time, cols int) string {
	t.Helper()
	var b bytes.Buffer
	return strings.Join(renderRegion(&b, st, now, cols), "\n")
}

// The acceptance criterion in one assertion: with runs in flight the region
// shows a row per run carrying every element the display promises.
func TestRegionShowsEveryElementPerRun(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)

	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	a := run("OR-237", events.ActorImplementer, "implementing", now.Add(-6*time.Minute-2*time.Second))
	a.median = 11 * time.Minute
	a.calls = 84
	a.last = now.Add(-2 * time.Second)
	a.buckets[bucketIndex(a.newest)] = 9
	b := run("OR-238", events.ActorQA, "qa", now.Add(-2*time.Minute-11*time.Second))
	b.median = 8 * time.Minute
	b.calls = 31
	b.last = now.Add(-time.Second)

	got := regionOf(t, stateOf(now, a, b), now, 0)
	lines := strings.Split(got, "\n")

	// Located by CONTENT rather than by index. The status line moved to the
	// bottom of the region and the batch block below it (OR-264), and a test
	// that indexes rows by position fails on a layout change while saying
	// nothing about whether the display is still correct.
	find := func(want string) string {
		t.Helper()
		for _, l := range lines {
			if strings.Contains(l, want) {
				return l
			}
		}
		t.Fatalf("no line contains %q:\n%s", want, got)
		return ""
	}

	header := find("running")
	if !strings.Contains(header, "2 running") {
		t.Errorf("the header must say how many are running; got %q", header)
	}
	if !strings.Contains(header, "OR") {
		t.Errorf("the header must name the project; got %q", header)
	}

	row := find("OR-237")
	for _, want := range []string{
		"implementing", // the stage
		"6m02s",        // elapsed
		"84",           // the tool-call count
		barFullGlyph,   // progress against the median
	} {
		if !strings.Contains(row, want) {
			t.Errorf("row is missing %q: %q", want, row)
		}
	}
	if !strings.ContainsAny(row, spinnerGlyphs) {
		t.Errorf("row has no spinner: %q", row)
	}
	if !strings.ContainsAny(row, sparkGlyphs) {
		t.Errorf("row has no sparkline: %q", row)
	}
	// And the second run has its own row, which is the "per run" in the name.
	if second := find("OR-238"); !strings.Contains(second, "qa") {
		t.Errorf("the second run's row lost its stage: %q", second)
	}
}

// Past its median a run must NOT creep toward 100%: it fills, stops, and says
// so in words with the median beside it. The bar is a reference and the row
// has to admit when the reference has been passed.
func TestPastMedianFillsAndSaysRunningLong(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", now.Add(-24*time.Minute-time.Second))
	r.median = 11 * time.Minute
	r.last = now

	var b bytes.Buffer
	row := renderRow(&b, *r, now, 0)

	if !strings.Contains(row, "running long") {
		t.Errorf("a run past its median must say so: %q", row)
	}
	if !strings.Contains(row, "median 11m") {
		t.Errorf("a run past its median must name the median: %q", row)
	}
	// Filled and stopped: every cell heavy, and no head glyph implying more
	// to come.
	if !strings.Contains(row, strings.Repeat(barFullGlyph, liveBarWidth)) {
		t.Errorf("the bar should be full: %q", row)
	}
	if strings.Contains(row, barHeadGlyph) {
		t.Errorf("a full bar must not still show a head, which reads as progress: %q", row)
	}
	// And it can never render wider than the column, whatever the overrun.
	r2 := *r
	r2.started = now.Add(-10 * time.Hour)
	if got := strings.Count(renderRow(&b, r2, now, 0), barFullGlyph); got != liveBarWidth {
		t.Errorf("a bar 55x over its median rendered %d cells, want %d", got, liveBarWidth)
	}
}

// The sparkline is the element a progress bar cannot replace. A run whose
// tool calls have stopped must flatline AND say "quiet", because a stalled
// run and a busy one looking alike is the whole failure this prevents.
func TestQuietRunFlatlinesAndSaysSo(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", now.Add(-9*time.Minute-14*time.Second))
	r.median = 20 * time.Minute
	r.calls = 40
	// Busy until three minutes ago, then nothing.
	r.last = now.Add(-3*time.Minute - 12*time.Second)
	r.advance(r.last)
	r.buckets[bucketIndex(r.newest)] = 7
	r.advance(now)

	var b bytes.Buffer
	row := renderRow(&b, *r, now, 0)

	if !strings.Contains(row, "quiet 3m12s") {
		t.Errorf("a run with no tool call for over a minute must say how long: %q", row)
	}
	flat := strings.Repeat(string([]rune(sparkGlyphs)[0]), sparkBuckets)
	if !strings.Contains(row, flat) {
		t.Errorf("a quiet run's sparkline must be flat, not the last burst it made: %q", row)
	}

	// And a run that IS working is not reported quiet, or the word means
	// nothing.
	busy := *r
	busy.last = now.Add(-2 * time.Second)
	if strings.Contains(renderRow(&b, busy, now, 0), "quiet") {
		t.Error("a run that called a tool two seconds ago is not quiet")
	}
}

// A run with no median draws NO bar. An empty bar reads as "0% done", which
// is a claim; blank reads as "not applicable", which is the truth for a
// project with no completed runs to measure against.
func TestNoMedianDrawsNoBar(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	r := run("OR-240", events.ActorImplementer, "implementing", now.Add(-time.Minute))

	var b bytes.Buffer
	row := renderRow(&b, *r, now, 0)
	if strings.ContainsAny(row, barFullGlyph+barHeadGlyph+barEmptyGlyph) {
		t.Errorf("no median means no bar of any kind: %q", row)
	}
	if strings.Contains(row, "running long") {
		t.Error("a run cannot be past a median it does not have")
	}
}

// NO_COLOR asks for no colour, not for no display. The layout survives; the
// escape codes and the glyphs do not.
func TestNoColorKeepsTheLayoutAndDropsTheGlyphs(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", now.Add(-time.Minute))
	r.median = 10 * time.Minute
	r.calls = 5

	t.Setenv("NO_COLOR", "1")
	var b bytes.Buffer
	row := renderRow(&b, *r, now, 0)

	if strings.Contains(row, "\x1b[") {
		t.Errorf("NO_COLOR must leave no escape codes: %q", row)
	}
	if strings.ContainsAny(row, spinnerGlyphs+sparkGlyphs+barFullGlyph) {
		t.Errorf("NO_COLOR must degrade the glyphs to ASCII: %q", row)
	}
	if !strings.ContainsAny(row, spinnerASCII) {
		t.Errorf("the ASCII spinner must still be there: %q", row)
	}
	// The facts are all still in words, which is what makes the row readable
	// without any of the decoration.
	for _, want := range []string{"OR-237", "implementing", "1m00s"} {
		if !strings.Contains(row, want) {
			t.Errorf("row lost %q under NO_COLOR: %q", want, row)
		}
	}
}

// Narrow terminals drop columns right to left -- sparkline, then bar, then
// actor -- and never wrap. A wrapped row is two rows, and two rows per run is
// not a table.
func TestNarrowTerminalDropsColumnsRightToLeft(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", now.Add(-time.Minute))
	r.median = 10 * time.Minute

	// Derived, never spelled: a name written into a test is a name that
	// breaks the day somebody renames the agent (see internal/actors).
	who := actors.Get(events.ActorImplementer).Name

	var b bytes.Buffer
	has := func(row, glyphs string) bool { return strings.ContainsAny(row, glyphs) }

	// The sparkline is detected by the glyphs the BAR cannot draw: both use
	// the full block now, so testing for sparkGlyphs alone matches a bar.
	sparkOnly := strings.TrimRight(sparkGlyphs, barFullGlyph)
	full := renderRow(&b, *r, now, 200)
	if !has(full, sparkOnly) || !strings.Contains(full, barHeadGlyph) || !strings.Contains(full, who) {
		t.Fatalf("a wide terminal should keep every column: %q", full)
	}

	// One column narrower than the full row: the sparkline is the first thing
	// given up, and everything to its left survives.
	noSpark := renderRow(&b, *r, now, 79)
	if has(noSpark, sparkOnly) {
		t.Errorf("the sparkline goes first: %q", noSpark)
	}
	if !strings.Contains(noSpark, barHeadGlyph) || !strings.Contains(noSpark, who) {
		t.Errorf("only the sparkline should have gone: %q", noSpark)
	}

	noBar := renderRow(&b, *r, now, 65)
	if has(noBar, barFullGlyph+barHeadGlyph+barEmptyGlyph) {
		t.Errorf("the bar goes second: %q", noBar)
	}
	if !strings.Contains(noBar, who) {
		t.Errorf("the actor goes last, not second: %q", noBar)
	}

	noActor := renderRow(&b, *r, now, 49)
	if strings.Contains(noActor, who) {
		t.Errorf("the actor goes third: %q", noActor)
	}

	// The key, the stage and the elapsed are never dropped: they are what the
	// row is FOR. And no row ever exceeds the width it was given.
	for cols, row := range map[int]string{79: noSpark, 65: noBar, 49: noActor, 40: renderRow(&b, *r, now, 40)} {
		for _, want := range []string{"OR-237", "implementing", "1m00s"} {
			if !strings.Contains(row, want) {
				t.Errorf("a %d-column row must keep %q: %q", cols, want, row)
			}
		}
		if n := len([]rune(row)); n > cols {
			t.Errorf("a row rendered %d runes into %d columns: %q", n, cols, row)
		}
	}
}

// Four concurrent runs have to fit a standard terminal. A row that wraps has
// stopped being a row.
func TestFourRunsFitEightyColumns(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	var runs []*liveRun
	for _, k := range []string{"OR-237", "OR-238", "OR-241", "OR-242"} {
		r := run(k, events.ActorImplementer, "implementing", now.Add(-6*time.Minute))
		r.median = 11 * time.Minute
		r.calls = 1234
		r.last = now
		runs = append(runs, r)
	}
	t.Setenv("NO_COLOR", "1") // measure the text, not the escape codes
	for _, line := range strings.Split(regionOf(t, stateOf(now, runs...), now, 80), "\n") {
		if n := len([]rune(line)); n > 80 {
			t.Errorf("line is %d columns wide, which wraps an 80-column terminal:\n%q", n, line)
		}
	}
}

// Nothing running means nothing drawn. This is what makes a tick with nothing
// to do print nothing at all.
func TestEmptyRegionDrawsNothing(t *testing.T) {
	now := time.Now()
	if lines := regionOf(t, liveState{}, now, 80); lines != "" {
		t.Errorf("an empty region rendered %q", lines)
	}
	if lines := renderPlain(liveState{}, now); len(lines) != 0 {
		t.Errorf("an empty plain tick rendered %v", lines)
	}
}

// Off a terminal there is no cursor control at all, and the heartbeat is one
// plain line per run per tick. A redirected log has to stay a log.
func TestOffTerminalIsPlainLinesAndNoCursorControl(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)

	var b bytes.Buffer
	l := NewLive(&b)
	defer l.Close()

	LiveStart("OR-237")
	liveStage("OR-237", "implementing", events.ActorImplementer)
	LiveActivity("OR-237", events.ActorImplementer)

	if _, err := l.Write([]byte("a scrollback line\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	l.Tick()
	l.Tick()

	got := b.String()
	if strings.Contains(got, "\x1b[") {
		t.Errorf("a non-terminal destination must get no cursor control:\n%q", got)
	}
	if !strings.Contains(got, "a scrollback line\n") {
		t.Errorf("the scrollback line must pass through untouched:\n%q", got)
	}
	// Printed ONCE across two ticks, not once per tick: nothing about the row
	// changed between them, and an unchanged line re-printed on the minute
	// is the noise that buried a held run's fault line on the macOS runner
	// (OR-265). One plain line per run per CHANGE is the contract now.
	if n := strings.Count(got, "OR-237"); n != 1 {
		t.Errorf("expected the row printed once for two unchanged ticks; got %d:\n%s", n, got)
	}
	for _, want := range []string{"implementing", "1 calls"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plain line is missing %q:\n%s", want, got)
		}
	}
}

// TERM=dumb is a terminal saying it cannot render anything clever. Taking it
// at its word is the whole point of honouring it.
func TestTermDumbGetsNoCursorControl(t *testing.T) {
	t.Setenv("TERM", "dumb")
	var b bytes.Buffer
	if cursorControl(&b) {
		t.Error("a bytes.Buffer is not a terminal")
	}
	// And the decision is the same one colour makes, so there is one answer
	// to "what can this terminal do".
	if enabled(&b) {
		t.Error("colour must be off under TERM=dumb too")
	}
}

// Scrollback is the TERMINAL's, and a line written during a run stays in it.
//
// The frozen window used to hold these lines back and redraw a bounded five
// of them above the region (OR-248); they now go straight through, because
// each ticket's latest line rides on its own row and holding the rest back
// would only stop the operator scrolling up to read what happened (OR-265).
//
// What must still hold is that the region is erased before each write and
// redrawn after it, so a line lands in the scrollback rather than on top of
// the pinned block.
func TestScrollbackSurvivesTheRegion(t *testing.T) {
	t.Setenv("LINES", "24")
	t.Setenv("COLUMNS", "")
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	for _, line := range []string{"first", "second", "third"} {
		if _, err := fmt.Fprintln(l, line); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}
	l.Close()
	got := b.String()

	// Every line SURVIVES, which is the property that matters (OR-313).
	//
	// Not "appears exactly once": the window redraws, so a line is written
	// again on every tick and the terminal's erase removes the previous copy.
	// A byte-count assertion measures the buffer rather than the screen, and
	// it was what pinned the OR-265 behaviour of writing straight through --
	// the behaviour this ticket reverses.
	//
	// What Close() must guarantee is that nothing on screen is lost when the
	// region goes: commitWindowLocked prints the window into real scrollback
	// on the way out.
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q never reached the terminal:\n%q", want, got)
		}
	}
	// The window's own frame, so the pane reads as bounded rather than as a
	// log that stalled.
	if !strings.Contains(got, "recent") || !strings.Contains(got, "scrolls, then gone") {
		t.Errorf("the window was drawn without its frame:\n%q", got)
	}
	// The last thing written is the committed window, with no frame around
	// it: the frame belongs to the pinned pane and would be a border around
	// nothing once the region is gone.
	tail := got[strings.LastIndex(got, "\x1b[0J")+len("\x1b[0J"):]
	if strings.Contains(tail, "recent") {
		t.Errorf("the frame was committed to scrollback:\n%q", tail)
	}
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(tail, want) {
			t.Errorf("%q was not committed to scrollback on Close:\n%q", want, tail)
		}
	}
	// And each write erased the region before printing, or the line would
	// have landed on top of the pinned rows.
	if !strings.Contains(got, "\x1b[0J") {
		t.Errorf("the region was never erased around a write:\n%q", got)
	}
	// The region is gone at the end; the scrollback is not.
	if l.drawn != 0 {
		t.Errorf("Close left %d rows recorded as drawn", l.drawn)
	}
}

// A row must never interleave with a stage line mid-redraw. The renderer
// holds no lock and the writer holds one, so concurrent job output and the
// redraw timer cannot splice.
func TestConcurrentWritesAndRedrawsDoNotSplice(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	for _, k := range []string{"OR-237", "OR-238", "OR-241", "OR-242"} {
		LiveStart(k)
		liveStage(k, "implementing", events.ActorImplementer)
	}

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				_, _ = l.Write([]byte("SENTINEL-line\n"))
				LiveActivity("OR-237", events.ActorImplementer)
			}
		}(i)
	}
	// The redraw timer, running against the same writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			l.lock.Lock()
			l.eraseLocked()
			l.drawLocked()
			l.lock.Unlock()
		}
	}()
	wg.Wait()

	// Every sentinel came out whole, and none of them has a region row spliced
	// into the middle of it. A line is redrawn for as long as it stays in the
	// window, so the count is a floor rather than an equality -- but a line that
	// was ever spliced is a line that does not end where it should.
	got := b.String()
	if n := strings.Count(got, "SENTINEL-line\n"); n < 8*40 {
		t.Errorf("expected at least 320 whole sentinel lines, got %d", n)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "SENTINEL") && !strings.HasSuffix(line, "SENTINEL-line") {
			t.Fatalf("a sentinel was spliced: %q", line)
		}
	}
}

// The sparkline zeroes the buckets it skips. Without that, a run that stopped
// calling tools would keep showing its last burst -- which is exactly the
// "busy and stalled look identical" failure the sparkline exists to prevent.
func TestSparklineZeroesTheGap(t *testing.T) {
	start := time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", start)
	r.buckets[bucketIndex(r.newest)] = 5

	r.advance(start.Add(30 * time.Second)) // three buckets on
	if got := r.buckets[bucketIndex(r.newest)]; got != 0 {
		t.Errorf("the new bucket should be empty, got %d", got)
	}
	if got := r.buckets[bucketIndex(r.newest-1)]; got != 0 {
		t.Errorf("a skipped bucket should be zeroed, got %d", got)
	}

	// Past the whole window, everything is gone.
	r.advance(start.Add(sparkBucket*sparkBuckets + time.Minute))
	for i, n := range r.buckets {
		if n != 0 {
			t.Errorf("bucket %d survived a window-length gap with %d", i, n)
		}
	}
	if got := r.sparkline(); got != strings.Repeat(string([]rune(sparkGlyphs)[0]), sparkBuckets) {
		t.Errorf("a fully-lapsed sparkline should be flat, got %q", got)
	}
}

// A tool call in a quiet window has to be visibly different from no call at
// all, or one call a minute renders as a stall.
func TestOneCallIsNotFlat(t *testing.T) {
	start := time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", start)
	r.buckets[bucketIndex(r.newest)] = 1

	got := r.sparkline()
	flat := strings.Repeat(string([]rune(sparkGlyphs)[0]), sparkBuckets)
	if got == flat {
		t.Errorf("one call rendered as flat: %q", got)
	}
	if n := len([]rune(got)); n != sparkBuckets {
		t.Errorf("the sparkline is %d cells wide, want %d", n, sparkBuckets)
	}
}

// The registry is fed from three packages that do not know about each other.
// This is the contract between them.
func TestRegistryTracksARunEndToEnd(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	LiveMedians(func(actor string) time.Duration {
		if actor == events.ActorImplementer {
			return 11 * time.Minute
		}
		return 0
	})

	at := time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)
	liveStart("OR-237", at)
	if got := liveSnapshot().rows; len(got) != 1 || got[0].stage != "starting" {
		t.Fatalf("a dispatched ticket should appear at once: %+v", got)
	}

	liveStage("OR-237", "implementing", events.ActorImplementer)
	liveActivity("OR-237", events.ActorImplementer, at.Add(time.Second))
	liveActivity("OR-237", events.ActorImplementer, at.Add(2*time.Second))

	row := liveSnapshot().rows[0]
	if row.stage != "implementing" || row.actor != events.ActorImplementer {
		t.Errorf("the stage boundary should set both stage and actor: %+v", row)
	}
	if row.calls != 2 {
		t.Errorf("calls = %d, want 2", row.calls)
	}
	if row.median != 11*time.Minute {
		t.Errorf("median = %v, want 11m -- resolved when the actor became known", row.median)
	}

	// The QA actor has no median, so the row loses its bar rather than
	// inheriting the implementer's.
	liveStage("OR-237", "qa", events.ActorQA)
	if got := liveSnapshot().rows[0].median; got != 0 {
		t.Errorf("median = %v after handing to an actor with no history, want 0", got)
	}

	// A reaped run KEEPS its row, marked done, rather than leaving the
	// region: work finishing is what the operator was waiting for, and it is
	// the worst moment to stop saying anything (OR-265).
	LiveEnd("OR-237")
	got := liveSnapshot().rows
	if len(got) != 1 {
		t.Fatalf("a reaped run should keep its row: %+v", got)
	}
	if !got[0].done {
		t.Errorf("a reaped run must be marked done: %+v", got[0])
	}
	if got[0].stage != "done" {
		t.Errorf("stage = %q, want the outcome", got[0].stage)
	}
}

// Rows are ordered by key, so four of them do not shuffle four times a
// second. A row that moves is unreadable however correct it is.
func TestRowOrderIsStable(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	for _, k := range []string{"OR-241", "OR-237", "OR-238"} {
		LiveStart(k)
	}
	want := []string{"OR-237", "OR-238", "OR-241"}
	for i := 0; i < 5; i++ {
		rows := liveSnapshot().rows
		for j, r := range rows {
			if r.key != want[j] {
				t.Fatalf("row %d is %s, want %s", j, r.key, want[j])
			}
		}
	}
}

// The header states what the session has spent, because a number that goes up
// is the least deniable evidence that work is being done.
func TestHeaderReportsSpendAndCI(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	LiveSpend(1.5)
	LiveSpend(2.62)
	LiveCI(1)
	LiveStart("OR-237")

	var b bytes.Buffer
	got := renderHeader(&b, liveSnapshot(), time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC))
	for _, want := range []string{"1 running", "1 in CI", "$4.12 this session"} {
		if !strings.Contains(got, want) {
			t.Errorf("header is missing %q: %q", want, got)
		}
	}
}

// A run that never made a tool call is quiet from DISPATCH time, not from
// some notion of "first activity" that never happened -- a run that has done
// nothing at all is the most suspicious kind of quiet, not the least.
func TestQuietMeasuredFromDispatchWhenNeverActive(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "starting", now.Add(-90*time.Second))
	// r.last is left at its zero value: no tool call has ever been recorded.

	// Asserted by presence rather than by position: this run has no median,
	// so it also carries "no baseline yet", and which note comes first is not
	// what this test is about.
	if notes := r.notes(now); !hasNote(notes, "quiet 1m30s") {
		t.Errorf("a run with no activity must be quiet from its dispatch time: %+v", notes)
	}
}

// hasNote reports whether any note contains want.
func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// The quiet threshold is exactly 60 seconds: one tick short must not say it,
// and the instant it is reached must.
func TestQuietThresholdIsExactlySixtySeconds(t *testing.T) {
	start := time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", start)
	r.last = start

	notAt59 := r.notes(start.Add(59 * time.Second))
	if hasNote(notAt59, "quiet") {
		t.Errorf("59 seconds of silence must not be reported quiet: %+v", notAt59)
	}
	atExactly60 := r.notes(start.Add(60 * time.Second))
	if !hasNote(atExactly60, "quiet") {
		t.Errorf("exactly 60 seconds of silence must be reported quiet: %+v", atExactly60)
	}
}

// NO_COLOR degrades every glyph, not just the spinner: the bar and the
// sparkline must also fall back to their ASCII forms.
func TestNoColorDegradesBarAndSparklineGlyphs(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", now.Add(-time.Minute))
	r.median = 10 * time.Minute
	r.calls = 3
	r.buckets[bucketIndex(r.newest)] = 2

	t.Setenv("NO_COLOR", "1")
	var b bytes.Buffer
	row := renderRow(&b, *r, now, 0)

	if strings.ContainsAny(row, barFullGlyph+barHeadGlyph+barEmptyGlyph+sparkGlyphs) {
		t.Errorf("NO_COLOR must drop the unicode bar and sparkline glyphs: %q", row)
	}
	if !strings.ContainsAny(row, barFullASCII+barHeadASCII) {
		t.Errorf("the bar must degrade to its ASCII form under NO_COLOR: %q", row)
	}
	if !strings.ContainsAny(row, sparkASCII) {
		t.Errorf("the sparkline must degrade to its ASCII form under NO_COLOR: %q", row)
	}
}

// An actor name longer than its column is clipped and marked, so the reader
// knows it was cut rather than that it is simply short.
func TestLongActorNameIsClippedWithAMarker(t *testing.T) {
	t.Cleanup(actors.Reset)
	long := "Alexandria-Superlongname"
	if err := actors.Configure(map[string]config.Agent{
		events.ActorImplementer: {Name: &long},
	}); err != nil {
		t.Fatalf("configuring the actor: %v", err)
	}

	now := time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC)
	r := run("OR-237", events.ActorImplementer, "implementing", now.Add(-time.Minute))

	var b bytes.Buffer
	row := renderRow(&b, *r, now, 200) // wide enough to keep every column
	if strings.Contains(row, long) {
		t.Errorf("an overlong actor name must be clipped, not shown in full: %q", row)
	}
	if !strings.Contains(row, "…") {
		t.Errorf("a clipped name must carry the ellipsis marker: %q", row)
	}

	t.Setenv("NO_COLOR", "1")
	rowNoColor := renderRow(&b, *r, now, 200)
	if !strings.Contains(rowNoColor, ".") {
		t.Errorf("under NO_COLOR the clip marker degrades to a period: %q", rowNoColor)
	}
	if strings.Contains(rowNoColor, "…") {
		t.Errorf("NO_COLOR must not still show the unicode ellipsis: %q", rowNoColor)
	}
}

// The header states the current time in HH:MM:SS, which is what makes a
// screenshot or a scrollback line dateable on its own.
func TestHeaderShowsTimeInHHMMSS(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	now := time.Date(2026, 8, 30, 9, 5, 3, 0, time.Local)
	got := renderHeader(&b, liveSnapshot(), now)
	if !strings.Contains(got, "09:05:03") {
		t.Errorf("header must show HH:MM:SS, got %q", got)
	}
}

// "X in CI" and the spend figure are both conditional: a fact that is not
// true must not be printed as though it were.
func TestHeaderOmitsCIAndSpendWhenZero(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	got := renderHeader(&b, liveSnapshot(), time.Date(2026, 8, 30, 23, 47, 12, 0, time.UTC))
	if strings.Contains(got, "in CI") {
		t.Errorf("with nothing pending in CI the header must not mention it: %q", got)
	}
	if strings.Contains(got, "this session") {
		t.Errorf("with no spend recorded the header must not print a dollar figure: %q", got)
	}
}

// A write that has not closed its line holds the redraw, and the region draws
// once the line is finally closed. Splicing a row into the middle of an
// unfinished write is exactly the corruption this guards against.
func TestPendingWriteHoldsRedrawUntilLineCloses(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}

	if _, err := l.Write([]byte("no newline yet")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if !l.pending {
		t.Fatal("a write with no trailing newline must be marked pending")
	}
	if l.drawn != 0 {
		t.Errorf("the region must not draw while a line is still open, got %d rows drawn", l.drawn)
	}

	if _, err := l.Write([]byte(" and now it closes\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if l.pending {
		t.Error("the pending flag must clear once the line closes")
	}
	if l.drawn == 0 {
		t.Error("the region should redraw once the line has closed")
	}
}

// Close is documented safe to call twice -- once when the watcher wraps up
// deliberately and once from a deferred cleanup that runs regardless.
func TestCloseIsIdempotent(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)

	var b bytes.Buffer
	l := NewLive(&b)
	l.Close()
	l.Close() // must not panic, block, or double-erase
}

func TestElapsedString(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{6*time.Minute + 2*time.Second, "6m02s"},
		{24*time.Minute + time.Second, "24m01s"},
		{time.Hour + 4*time.Minute, "1h04m"},
		{-time.Second, "0s"},
	} {
		if got := elapsedString(c.d); got != c.want {
			t.Errorf("elapsedString(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// The header's CI count answers "how many tickets are waiting". It cannot
// answer "what is that run doing" -- during a batch three tickets share ONE
// run, so a count says nothing about which platform is still going, and an
// operator watching "still running" for nine minutes has no way to see that
// only Windows is left (OR-264).
func TestTheChecksRowNamesEachCheckAndItsState(t *testing.T) {
	got := renderChecks(io.Discard, []Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (macos)", State: CheckRunning},
		{Name: "go (windows)", State: CheckFailed},
	}, 200)

	for _, want := range []string{"go (ubuntu)", "go (macos)", "go (windows)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the checks row does not name %q:\n%s", want, got)
		}
	}
	// One line, not a row each: they belong to a single run, and stacking
	// them would push the ticket rows off a short terminal.
	if strings.Contains(got, "\n") {
		t.Errorf("the checks must render as one line:\n%s", got)
	}
}

// Nothing to say, nothing drawn. A repository with no checks configured must
// not gain a blank row for them.
func TestNoChecksDrawsNoRow(t *testing.T) {
	if got := renderChecks(io.Discard, nil, 200); got != "" {
		t.Errorf("an empty check set must draw nothing, got %q", got)
	}
}

// On a real terminal the checks row spins and colours, the same as every
// other status in this package (color.go) -- a passed check is green, a
// failed one is red, and a running one carries the same braille spinner the
// ticket rows use, so "still going" reads identically wherever it appears
// (OR-310).
//
// isTerminal type-asserts *os.File, so a bytes.Buffer can never exercise this
// path: /dev/null is a real char device and reads as a terminal without
// needing an actual tty, the same trick TestAWrappedWriterIsStillTheTerminalItWraps
// uses.
func TestInteractiveChecksRowCarriesSpinnerAndColour(t *testing.T) {
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })

	got := renderChecks(f, []Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (windows)", State: CheckRunning},
		{Name: "go (macos)", State: CheckFailed},
	}, 200)

	if !strings.Contains(got, "\x1b[32m") {
		t.Errorf("a passed check must be painted green: %q", got)
	}
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("a failed check must be painted red: %q", got)
	}
	if !strings.ContainsAny(got, spinnerGlyphs) && !strings.ContainsAny(got, spinnerASCII) {
		t.Errorf("a running check must carry the same spinner the rows use: %q", got)
	}
}

// A rollup is a complete picture of one moment, so a later reading REPLACES
// the previous one. Merging would leave a finished check on screen after a
// re-run dropped it.
func TestChecksReplaceRatherThanAccumulate(t *testing.T) {
	LiveReset()
	defer LiveReset()
	LiveChecks([]Check{{Name: "go (ubuntu)", State: CheckRunning}, {Name: "go (macos)", State: CheckRunning}})
	LiveChecks([]Check{{Name: "go (ubuntu)", State: CheckPassed}})

	st := liveSnapshot()
	if len(st.checks) != 1 || st.checks[0].Name != "go (ubuntu)" || st.checks[0].State != CheckPassed {
		t.Errorf("the second reading must replace the first, got %+v", st.checks)
	}
}

// Off a terminal the checks reach the log too, and only when one of them
// moved (OR-310).
//
// The region draws them under its rule; a piped watch drew nothing at all, so
// the file a failure is read out of afterwards said "1 in CI" and never which
// platform that was. The repeat guard is the lastPlain contract the rows keep:
// a check that has said "running" for nine minutes must not say it once a
// minute for nine minutes.
func TestOffATerminalTheChecksArePrintedOnceUntilOneMoves(t *testing.T) {
	LiveReset()
	defer LiveReset()

	var buf bytes.Buffer
	live := NewLive(&buf)
	defer live.Close()
	LiveChecks([]Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (windows)", State: CheckRunning},
	})

	live.Tick()
	first := buf.String()
	if !strings.Contains(first, "go (windows) running") || !strings.Contains(first, "go (ubuntu) passed") {
		t.Fatalf("the plain path must name the checks and their states: %q", first)
	}
	// No spinner, no colour: a log is read afterwards, where a braille frame
	// is a character rather than motion.
	if strings.ContainsAny(first, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") || strings.Contains(first, "\x1b[") {
		t.Errorf("the plain form must carry no animation or colour: %q", first)
	}

	buf.Reset()
	for i := 0; i < 3; i++ {
		live.Tick()
	}
	if got := buf.String(); strings.Contains(got, "go (windows)") {
		t.Errorf("an unchanged rollup repeated, which buries the lines that matter:\n%q", got)
	}

	// A check that MOVES is news, and prints again.
	LiveChecks([]Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (windows)", State: CheckPassed},
	})
	buf.Reset()
	live.Tick()
	if got := buf.String(); !strings.Contains(got, "go (windows) passed") {
		t.Errorf("a check that changed state must print: %q", got)
	}
}

// Off a terminal the checks line is plain text: no spinner frame, no escape
// code, the state spelled out in words instead. A log is read afterwards,
// where a braille frame is a character rather than motion and an escape code
// is grit in the file (OR-310).
func TestOffTerminalChecksLineHasNoSpinnerOrColour(t *testing.T) {
	st := liveState{checks: []Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (windows)", State: CheckRunning},
		{Name: "go (macos)", State: CheckFailed},
	}}
	lines, _, _ := renderPlainTracked(st, time.Date(2026, 1, 1, 9, 41, 0, 0, time.Local))

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "go (windows) running") {
		t.Fatalf("the plain line must spell the state out in words: %q", joined)
	}
	if strings.ContainsAny(joined, spinnerGlyphs) || strings.ContainsAny(joined, spinnerASCII) {
		t.Errorf("the plain line must carry no spinner frame: %q", joined)
	}
	if strings.Contains(joined, "\x1b[") {
		t.Errorf("the plain line must carry no escape code: %q", joined)
	}
}

// Every off-terminal line in this package is timestamped, because a log is
// read after the fact and a line with no clock on it cannot be placed against
// the rest of the run. The checks line keeps that contract rather than being
// the one exception (OR-310).
func TestOffTerminalChecksLineCarriesATimestampPrefix(t *testing.T) {
	st := liveState{checks: []Check{{Name: "go (ubuntu)", State: CheckPassed}}}
	now := time.Date(2026, 1, 1, 9, 41, 0, 0, time.Local)
	lines, _, _ := renderPlainTracked(st, now)

	want := now.Local().Format("15:04") + "  ci  "
	found := false
	for _, l := range lines {
		if strings.HasPrefix(l, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("the checks line must start with the %q timestamp prefix, got %+v", now.Local().Format("15:04"), lines)
	}
}

// The very first tick has nothing recorded yet -- st.plainChecks is empty --
// so a freshly-read set of checks must print, not be mistaken for a repeat of
// nothing (OR-310).
func TestOffTerminalChecksLinePrintsOnceOnFirstRead(t *testing.T) {
	st := liveState{checks: []Check{{Name: "go (ubuntu)", State: CheckRunning}}}
	lines, _, checks := renderPlainTracked(st, time.Now())

	count := 0
	for _, l := range lines {
		if strings.Contains(l, "go (ubuntu) running") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("a first-read check must print exactly once, printed %d times in %+v", count, lines)
	}
	if checks == "" {
		t.Errorf("the checks body returned for recording must not be empty")
	}
}

// OR-310: a single ticket awaiting CI names each check and its state, rather
// than leaving the reader with only a ticket count. "1 in CI" says nothing
// about which platform is still going; the checks line is what closes that
// gap for an ordinary (non-batch) watch.
func TestPlainChecksBodyNamesTheCheckAndItsStateForASingleTicket(t *testing.T) {
	got := plainChecksBody([]Check{{Name: "go (windows)", State: CheckRunning}})

	if !strings.Contains(got, "go (windows) running") {
		t.Errorf("a single ticket's check must be named with its state, not just counted: %q", got)
	}
}

// Several checks belonging to the same ticket's CI run share one display
// line rather than one each -- they are all facts about a single run, and
// stacking them would push the ticket rows off a short terminal.
func TestPlainChecksBodyPutsSeveralChecksOnOneLine(t *testing.T) {
	got := plainChecksBody([]Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (macos)", State: CheckRunning},
		{Name: "go (windows)", State: CheckRunning},
	})

	if strings.Contains(got, "\n") {
		t.Errorf("several checks for one ticket must render as one line, got %q", got)
	}
	for _, want := range []string{"go (ubuntu) passed", "go (macos) running", "go (windows) running"} {
		if !strings.Contains(got, want) {
			t.Errorf("the shared line is missing %q: %q", want, got)
		}
	}
}

// A check moving from running to passed must be reflected the very next
// tick -- the display is pushed from the same read the verdict came from, so
// there is nothing stale to wait out.
func TestACheckMovingFromRunningToPassedUpdatesImmediately(t *testing.T) {
	LiveReset()
	defer LiveReset()

	var buf bytes.Buffer
	live := NewLive(&buf)
	defer live.Close()

	LiveChecks([]Check{{Name: "go (ubuntu)", State: CheckRunning}})
	live.Tick()
	if got := buf.String(); !strings.Contains(got, "go (ubuntu) running") {
		t.Fatalf("expected the running state on the first tick: %q", got)
	}

	LiveChecks([]Check{{Name: "go (ubuntu)", State: CheckPassed}})
	buf.Reset()
	live.Tick()
	if got := buf.String(); !strings.Contains(got, "go (ubuntu) passed") {
		t.Errorf("a check that passed must be reported on the very next tick: %q", got)
	}
}

// A check moving from running to failed is exactly as urgent as one moving
// to passed -- a repeat guard that only recognised success would bury the
// one outcome an operator most needs to see.
func TestACheckMovingFromRunningToFailedUpdatesImmediately(t *testing.T) {
	LiveReset()
	defer LiveReset()

	var buf bytes.Buffer
	live := NewLive(&buf)
	defer live.Close()

	LiveChecks([]Check{{Name: "go (windows)", State: CheckRunning}})
	live.Tick()
	if got := buf.String(); !strings.Contains(got, "go (windows) running") {
		t.Fatalf("expected the running state on the first tick: %q", got)
	}

	LiveChecks([]Check{{Name: "go (windows)", State: CheckFailed}})
	buf.Reset()
	live.Tick()
	if got := buf.String(); !strings.Contains(got, "go (windows) failed") {
		t.Errorf("a check that failed must be reported on the very next tick: %q", got)
	}
}

// With every check landed, the line says so for all of them -- no check is
// left showing a stale "running" once the run as a whole is done.
func TestAllChecksPassingDisplaysAllStatesAsPassed(t *testing.T) {
	got := plainChecksBody([]Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (macos)", State: CheckPassed},
		{Name: "go (windows)", State: CheckPassed},
	})

	for _, want := range []string{"go (ubuntu) passed", "go (macos) passed", "go (windows) passed"} {
		if !strings.Contains(got, want) {
			t.Errorf("every landed check must say passed: missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "running") || strings.Contains(got, "failed") {
		t.Errorf("with everything passed, no check may still read running or failed: %q", got)
	}
}

// An unchanged checks line is silent on every redraw after its first print,
// not merely on the second one -- five ticks with nothing new to say must
// leave nothing in the log (OR-310).
func TestUnchangedCheckLineIsNotRepeatedAcrossRedraws(t *testing.T) {
	LiveReset()
	defer LiveReset()

	var buf bytes.Buffer
	live := NewLive(&buf)
	defer live.Close()
	LiveChecks([]Check{{Name: "go (ubuntu)", State: CheckRunning}})

	live.Tick()
	if !strings.Contains(buf.String(), "go (ubuntu) running") {
		t.Fatalf("expected the check on its first tick: %q", buf.String())
	}

	buf.Reset()
	for i := 0; i < 5; i++ {
		live.Tick()
	}
	if got := buf.String(); strings.TrimSpace(got) != "" {
		t.Errorf("an unchanged checks line repeated across redraws, which buries the lines that matter: %q", got)
	}
}

// When two checks are on the line and only ONE of them moves, the line
// reprints with the new state -- the repeat guard is on the line as a
// whole, not on each check individually, so a single mover is enough to
// re-earn a print (OR-310).
func TestCheckLineReprintsWhenAnIndividualCheckChangesState(t *testing.T) {
	LiveReset()
	defer LiveReset()

	var buf bytes.Buffer
	live := NewLive(&buf)
	defer live.Close()
	LiveChecks([]Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (windows)", State: CheckRunning},
	})
	live.Tick()
	buf.Reset()

	LiveChecks([]Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (windows)", State: CheckFailed},
	})
	live.Tick()
	if got := buf.String(); !strings.Contains(got, "go (windows) failed") {
		t.Errorf("a single check changing state must reprint the line: %q", got)
	}
}

// A WRAPPED writer is still the terminal it wraps.
//
// internal/watch puts every line through a syncWriter so two agents cannot
// interleave mid-word, and it does that BEFORE handing the writer to NewLive.
// isTerminal type-asserted *os.File, a wrapper is not one, so cursorControl
// said "not a terminal" and the pinned region never engaged on a real
// `orion watch` at all -- since OR-184. It was invisible because the
// fallback, one plain line per tick, is a legitimate display in its own
// right, so nothing looked broken; the batch view was simply never seen and
// was believed unimplemented for weeks (OR-265).
func TestAWrappedWriterIsStillTheTerminalItWraps(t *testing.T) {
	// A char device rather than a tty: what is under test is whether the
	// unwrapping REACHES the *os.File, not whether that file is a terminal.
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })

	if !isTerminal(f) {
		t.Fatal("a char device must read as a terminal, or this test proves nothing")
	}
	if !isTerminal(&testWrap{f}) {
		t.Error("a wrapped terminal must still be a terminal, or the region never engages")
	}
	if !isTerminal(&testWrap{&testWrap{f}}) {
		t.Error("the chain must be followed to the end, not one link")
	}
	// A wrapper that will not name what it wraps is opaque by construction,
	// and guessing past it would be worse than the fallback.
	if isTerminal(&testOpaque{f}) {
		t.Error("a writer that cannot be unwrapped must not be assumed to be a terminal")
	}
}

type testWrap struct{ w io.Writer }

func (x *testWrap) Write(p []byte) (int, error) { return x.w.Write(p) }
func (x *testWrap) Unwrap() io.Writer           { return x.w }

type testOpaque struct{ w io.Writer }

func (x *testOpaque) Write(p []byte) (int, error) { return x.w.Write(p) }

// The row carries the ticket's TITLE as well as its identifier, so "what is
// OR-135 again" is answered on screen rather than in the tracker.
//
// Capped, and it yields to the note when both cannot fit: squeezed to a few
// characters a title identifies nothing, while a note stays useful much
// narrower -- and the note is the half that answers "is this moving"
// (OR-265).
func TestTheRowCarriesTheTitleAndYieldsItBeforeTheNote(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	now := time.Now()
	var b bytes.Buffer

	// A long title, and deliberately NOT a real ticket's: the registry's rule
	// is that a default agent name appears nowhere but actors.go, and using a
	// live summary as sample data smuggled one into this file
	// (TestNoDefaultNameAppearsOutsideTheRegistry catches it).
	const title = "Add a schema review stage that runs at planning time and reports"
	const note = "Edit internal/advise/advise.go"
	liveStart("OR-135", now.Add(-4*time.Minute))
	liveStage("OR-135", "implementing", events.ActorImplementer)
	LiveTitle("OR-135", title)
	liveActivityNote("OR-135", events.ActorImplementer, note, now)
	row := func(cols int) string { return renderRow(&b, liveSnapshot().rows[0], now, cols) }

	// Wide: both, and the title is capped rather than printed in full.
	wide := row(160)
	if !strings.Contains(wide, "Add a schema review") {
		t.Errorf("the row lost its title:\n%s", wide)
	}
	if !strings.Contains(wide, note) {
		t.Errorf("the row lost its note:\n%s", wide)
	}
	if strings.Contains(wide, title) {
		t.Errorf("a long title must be clipped, not printed whole:\n%s", wide)
	}

	// Narrow: the title goes, the note stays and stays READABLE -- the whole
	// point of dropping the title rather than sharing the space.
	narrow := row(110)
	if strings.Contains(narrow, "Add a schema review") {
		t.Errorf("the title must yield when the note cannot fit beside it:\n%s", narrow)
	}
	// "Edit internal/advis" rather than "...advise": OR-308 reserved eight
	// fixed cells for the median suffix so every row's later columns share a
	// left edge, and those cells come out of what the note has left. The
	// PROPERTY under test is unchanged and still checked -- the title yields
	// entirely and the note stays legible -- one character narrower.
	if !strings.Contains(narrow, "Edit internal/advis") {
		t.Errorf("the note must survive the squeeze:\n%s", narrow)
	}
}

// A ticket that FINISHES keeps its row, carrying the outcome.
//
// The row used to be deleted the moment the job returned, so an operator
// watching two tickets saw one of them vanish with no statement of what
// became of it -- at the exact moment the thing they were waiting for
// happened. It stops spinning and stops counting as running, because a
// header that claimed otherwise would be lying (OR-265).
func TestAFinishedRunKeepsItsRowAndSaysWhatBecameOfIt(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	now := time.Now()
	var b bytes.Buffer

	liveStart("OR-92", now.Add(-40*time.Minute))
	liveStage("OR-92", "implementing", events.ActorImplementer)
	liveStart("OR-135", now.Add(-38*time.Minute))
	liveStage("OR-135", "implementing", events.ActorImplementer)
	LiveDone("OR-92", "ready")

	st := liveSnapshot()
	if len(st.rows) != 2 {
		t.Fatalf("a finished run lost its row: %d row(s)", len(st.rows))
	}
	var done liveRun
	for _, r := range st.rows {
		if r.key == "OR-92" {
			done = r
		}
	}
	row := renderRow(&b, done, now, 150)
	if !strings.Contains(row, "ready") {
		t.Errorf("the row does not say what became of the ticket: %q", row)
	}
	if strings.ContainsAny(row, spinnerGlyphs) {
		t.Errorf("a finished run must not spin: %q", row)
	}
	// The header counts it as done rather than running, or it claims work
	// that has stopped.
	header := renderHeaderAt(&b, st, now, false)
	if !strings.Contains(header, "1 running") {
		t.Errorf("want exactly one running: %q", header)
	}
	if !strings.Contains(header, "1 done") {
		t.Errorf("the header does not report the finished run: %q", header)
	}
}

// TestTheActivityNoteIsVisibleOffATerminal.
//
// The note is how a stage says what it is doing right now -- "authoring x5",
// "running the suite" (OR-305, OR-306). The terminal path drew it and the
// plain path did not, so a watch piped to a log showed a row that had gone
// quiet with no explanation, while the same run on a terminal explained
// itself. The log is the one read after something has gone wrong, which is
// the worse half to leave silent.
func TestTheActivityNoteIsVisibleOffATerminal(t *testing.T) {
	LiveReset()
	var buf bytes.Buffer
	live := NewLive(&buf)
	LiveStart("OR-1")
	LiveStage("OR-1", "qa", "qa")
	LiveActivityNote("OR-1", "qa", "authoring x5")
	live.Tick()

	if !strings.Contains(buf.String(), "authoring x5") {
		t.Errorf("the activity note is missing from the plain output:\n%s", buf.String())
	}
}

// TestAFinishedRowDrawsAFullBar is OR-307.
//
// bar() returned blank whenever there was no median, and a finished ticket
// with no history therefore drew fourteen empty cells -- indistinguishable
// from a row that had not started. Completion is a FACT rather than an
// estimate, so it needs no baseline and does not touch OR-250's rule against
// inventing one.
func TestAFinishedRowDrawsAFullBar(t *testing.T) {
	LiveReset()
	LiveStart("OR-1")
	LiveDone("OR-1", "ready")

	var b bytes.Buffer
	row := renderRow(&b, liveSnapshot().rows[0], time.Now(), 200)

	if !strings.Contains(row, strings.Repeat(barFullGlyph, 4)) {
		t.Errorf("a finished row drew no bar:\n%s", row)
	}
}

// TestARunningRowWithNoBaselineStillDrawsNothing. OR-250 forbids inventing a
// baseline, and the blank bar plus "no baseline yet" is how that rule is
// expressed. OR-307 must not become a way around it.
func TestARunningRowWithNoBaselineStillDrawsNothing(t *testing.T) {
	LiveReset()
	LiveStart("OR-1")

	var b bytes.Buffer
	row := renderRow(&b, liveSnapshot().rows[0], time.Now(), 200)

	if strings.Contains(row, barFullGlyph) {
		t.Errorf("a running row with no median drew a bar:\n%s", row)
	}
	if !strings.Contains(row, "no baseline yet") {
		t.Errorf("the row stopped explaining its empty bar:\n%s", row)
	}
}

// TestColumnsShareALeftEdgeWithAndWithoutAMedian is OR-308.
//
// The median suffix was emitted only when a median existed, and sized to
// whatever it printed as -- so a row with a baseline pushed the sparkline,
// count and notes right while a row without one did not. Four rows in one
// region formed a ragged edge rather than columns.
func TestColumnsShareALeftEdgeWithAndWithoutAMedian(t *testing.T) {
	LiveReset()
	LiveStart("OR-1")
	LiveStart("OR-2")
	// Same actor, so the only difference between the rows is the median.
	LiveMedians(func(string) time.Duration { return 4 * time.Minute })
	liveActivity("OR-1", "qa", time.Now())
	LiveReset()

	// Row one HAS a median; row two does not. Built separately so nothing
	// else about them differs.
	var b bytes.Buffer
	now := time.Now()

	LiveStart("OR-1")
	LiveMedians(func(string) time.Duration { return 4 * time.Minute })
	liveActivity("OR-1", "qa", now)
	withMedian := plain(renderRow(&b, liveSnapshot().rows[0], now, 200))

	LiveReset()
	LiveStart("OR-2")
	LiveMedians(func(string) time.Duration { return 0 })
	liveActivity("OR-2", "qa", now)
	without := plain(renderRow(&b, liveSnapshot().rows[0], now, 200))

	// The sparkline is the first thing after the median suffix, so where it
	// starts is what proves the columns line up.
	// Counted in RUNES, not bytes. The bar is multi-byte glyphs, so a byte
	// index says two aligned rows are 28 apart and the test fails on its own
	// arithmetic rather than on the thing it is checking.
	runeIndex := func(s string, r rune) int {
		for i, c := range []rune(s) {
			if c == r {
				return i
			}
		}
		return -1
	}
	a := runeIndex(withMedian, '▁')
	c := runeIndex(without, '▁')
	if a < 0 || c < 0 {
		t.Fatalf("no sparkline to measure against:\n%s\n%s", withMedian, without)
	}
	if a != c {
		t.Errorf("the sparkline starts at %d with a median and %d without:\n%s\n%s",
			a, c, withMedian, without)
	}
}

// The terminal region (renderChecks) and the off-terminal log
// (plainChecksBody) draw from the SAME []Check, and a reader who follows a
// run from a terminal into its piped log must find the same checks in the
// same order in both -- OR-310's row is one source of truth rendered twice,
// not two renderers that can quietly drift apart.
//
// The two forms are deliberately NOT identical text: the region uses a
// glyph and colour, the log spells the state out in words because a log is
// read afterwards, where a spinner frame is just a character. What must
// match is which checks appear, and in what order.
func TestCheckNamesAndStatesAreOrderedConsistentlyAcrossBothPaths(t *testing.T) {
	checks := []Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (windows)", State: CheckRunning},
		{Name: "go (macos)", State: CheckFailed},
	}

	var w bytes.Buffer
	terminal := plain(renderChecks(&w, checks, 200))
	flat := plainChecksBody(checks)

	names := []string{"go (ubuntu)", "go (windows)", "go (macos)"}
	var lastT, lastF int = -1, -1
	for _, name := range names {
		it := strings.Index(terminal, name)
		ip := strings.Index(flat, name)
		if it < 0 || ip < 0 {
			t.Fatalf("%q missing from one of the two renderings:\nterminal=%q\nplain=%q", name, terminal, flat)
		}
		if it < lastT || ip < lastF {
			t.Errorf("the two paths disagree on check order:\nterminal=%q\nplain=%q", terminal, flat)
		}
		lastT, lastF = it, ip
	}

	// The state each path spells out for the SAME check must agree: a
	// passing check must not read "passed" in the log while the region
	// (via its glyph) means anything but.
	if !strings.Contains(flat, "go (ubuntu) passed") ||
		!strings.Contains(flat, "go (windows) running") ||
		!strings.Contains(flat, "go (macos) failed") {
		t.Errorf("the plain path must spell out each check's actual state: %q", flat)
	}
}
