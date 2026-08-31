package ui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
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
	// rule, header, blank, two rows.
	if len(lines) != 5 {
		t.Fatalf("expected a rule, a header, a blank and two rows; got %d lines:\n%s", len(lines), got)
	}
	if !strings.Contains(lines[1], "2 running") {
		t.Errorf("the header must say how many are running; got %q", lines[1])
	}
	if !strings.Contains(lines[1], "OR") {
		t.Errorf("the header must name the project; got %q", lines[1])
	}
	for _, want := range []string{
		"OR-237",                           // the ticket
		"implementing",                     // the stage
		"6m02s",                            // elapsed
		"84",                               // the tool-call count
		barFullGlyph,                       // progress against the median
		string([]rune(spinnerGlyphs)[0:1]), // any spinner frame is one of these
	} {
		if want == string([]rune(spinnerGlyphs)[0:1]) {
			if !strings.ContainsAny(lines[3], spinnerGlyphs) {
				t.Errorf("row has no spinner: %q", lines[3])
			}
			continue
		}
		if !strings.Contains(lines[3], want) {
			t.Errorf("row is missing %q: %q", want, lines[3])
		}
	}
	if !strings.ContainsAny(lines[3], sparkGlyphs) {
		t.Errorf("row has no sparkline: %q", lines[3])
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

	full := renderRow(&b, *r, now, 200)
	if !has(full, sparkGlyphs) || !strings.Contains(full, barHeadGlyph) || !strings.Contains(full, who) {
		t.Fatalf("a wide terminal should keep every column: %q", full)
	}

	// One column narrower than the full row: the sparkline is the first thing
	// given up, and everything to its left survives.
	noSpark := renderRow(&b, *r, now, 79)
	if has(noSpark, sparkGlyphs) {
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
	if n := strings.Count(got, "OR-237"); n != 2 {
		t.Errorf("expected one plain line per run per tick (2 ticks); got %d:\n%s", n, got)
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

// The scrollback above the region stays untouched: a write erases the region,
// emits the line, and redraws below it. Every line in order, nothing spliced.
func TestScrollbackSurvivesTheRegion(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	LiveStart("OR-237")

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	for _, s := range []string{"first\n", "second\n", "third\n"} {
		if _, err := l.Write([]byte(s)); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}
	l.Close()

	got := b.String()
	fi, si, ti := strings.Index(got, "first"), strings.Index(got, "second"), strings.Index(got, "third")
	if fi < 0 || si < 0 || ti < 0 {
		t.Fatalf("a scrollback line was lost:\n%q", got)
	}
	if !(fi < si && si < ti) {
		t.Errorf("scrollback came out of order:\n%q", got)
	}
	// Every redraw erased exactly the region it had drawn -- four lines: rule,
	// header, blank, one row -- so an erase can never reach up into the
	// scrollback above it. The first write had nothing yet to erase, so three
	// writes and a Close make three erases.
	if n := strings.Count(got, "\x1b[4A\x1b[0J"); n != 3 {
		t.Errorf("expected an erase before every redraw and one at Close, got %d:\n%q", n, got)
	}
	// And Close is the last thing in the stream, so the region is gone and
	// the scrollback is all that is left on screen.
	if !strings.HasSuffix(got, "\x1b[4A\x1b[0J") {
		t.Errorf("Close must end by clearing the region:\n%q", got)
	}
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
	// into the middle of it.
	if n := strings.Count(b.String(), "SENTINEL-line\n"); n != 8*40 {
		t.Errorf("expected 320 whole sentinel lines, got %d", n)
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

	LiveEnd("OR-237")
	if got := liveSnapshot().rows; len(got) != 0 {
		t.Errorf("a reaped run should leave the region: %+v", got)
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
