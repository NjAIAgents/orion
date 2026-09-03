package watch

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/ui"
)

// OR-310: an ordinary watch says WHICH check is holding a ticket up, not just
// how many tickets are waiting.
//
// The row existed and only the batch path fed it, so a single ticket waiting
// on CI got "1 in CI" and nothing else -- and on this project that is the
// question worth answering, because the Windows leg runs several times longer
// than Linux (OR-292) and "still running" for nine minutes is usually one
// platform. Asserted off a terminal, which is also the lastPlain contract: the
// log a failure is read out of afterwards must carry what the region drew.
func TestAnOrdinaryWatchNamesTheChecksItIsWaitingOn(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	s := &spy{
		maxSleeps: 4, queued: issues("FCIA-7"), hold: make(chan struct{}),
		pendingTicks: 3,
		pendingChecks: []collect.Check{
			{Name: "go (ubuntu)", State: collect.CheckPassed},
			{Name: "go (windows)", State: collect.CheckRunning},
		},
	}
	releaseAfter(s, 40*time.Millisecond)
	out := runWatch(t, s, Options{MaxConcurrent: 1})

	if !strings.Contains(out, "go (windows) running") {
		t.Errorf("the watch must name the check still going, not only count tickets:\n%s", out)
	}
	if !strings.Contains(out, "go (ubuntu) passed") {
		t.Errorf("the watch must say which checks have landed:\n%s", out)
	}
}

// Two tickets awaiting CI have two independent runs, so their cells are named
// by ticket. Without the prefix both would render "go (ubuntu)" and the line
// would say nothing about which ticket is waiting on what.
func TestChecksFromSeveralTicketsAreNamedByTicket(t *testing.T) {
	rows := checkRows([]collect.Result{
		{Key: "OR-2", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckRunning}}},
		{Key: "OR-1", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckPassed}}},
	})

	if len(rows) != 2 {
		t.Fatalf("both tickets' checks belong on the line, got %+v", rows)
	}
	// Key order, so a cell does not move under the reader between redraws.
	if rows[0].Name != "OR-1 go (ubuntu)" || rows[1].Name != "OR-2 go (ubuntu)" {
		t.Errorf("checks must be named by ticket, in key order: %+v", rows)
	}
}

// One ticket keeps the bare check names: prefixing a single run's checks with
// the only key on screen is noise in the narrowest line of the display.
func TestOneTicketsChecksAreNotPrefixed(t *testing.T) {
	rows := checkRows([]collect.Result{
		{Key: "OR-1", Checks: []collect.Check{{Name: "go (macos)", State: collect.CheckRunning}}},
	})
	if len(rows) != 1 || rows[0].Name != "go (macos)" {
		t.Errorf("a lone ticket's checks must keep their own names: %+v", rows)
	}
}

// A failing check is named, not just counted -- the culprit is the whole
// reason the row exists (OR-310).
func TestAFailingCheckIsNamedByTheWatch(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	s := &spy{
		maxSleeps: 4, queued: issues("FCIA-7"), hold: make(chan struct{}),
		pendingTicks: 3,
		pendingChecks: []collect.Check{
			{Name: "go (ubuntu)", State: collect.CheckPassed},
			{Name: "go (windows)", State: collect.CheckFailed},
		},
	}
	releaseAfter(s, 40*time.Millisecond)
	out := runWatch(t, s, Options{MaxConcurrent: 1})

	if !strings.Contains(out, "go (windows) failed") {
		t.Errorf("the watch must name the check that failed, not only that one did:\n%s", out)
	}
}

// Redrawn from whatever order the collect happened to return results in, the
// checks still land in key order -- so a cell never swaps position under the
// reader just because two `gh pr view` calls raced back in a different order.
func TestChecksStayInKeyOrderWhicheverOrderTheyWereCollected(t *testing.T) {
	a := checkRows([]collect.Result{
		{Key: "OR-2", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckRunning}}},
		{Key: "OR-1", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckRunning}}},
	})
	b := checkRows([]collect.Result{
		{Key: "OR-1", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckRunning}}},
		{Key: "OR-2", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckRunning}}},
	})

	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("both redraws must carry both tickets: a=%+v b=%+v", a, b)
	}
	if a[0].Name != b[0].Name || a[1].Name != b[1].Name {
		t.Errorf("a cell must not move between redraws just because collect returned results in a different order: a=%+v b=%+v", a, b)
	}
	if a[0].Name != "OR-1 go (ubuntu)" || a[1].Name != "OR-2 go (ubuntu)" {
		t.Errorf("checks must be in key order: %+v", a)
	}
}

// When one of two tickets awaiting CI finishes, the display stops naming it
// and drops the key prefix from the one left -- the prefix exists only to
// tell two waiting tickets apart, and there is only one left to tell apart
// from nothing.
func TestWhenOneTicketFinishesOnlyTheWaitingOnesChecksRemain(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	var buf bytes.Buffer
	live := ui.NewLive(&buf)
	defer live.Close()

	liveChecks(2, []collect.Result{
		{Key: "OR-1", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckRunning}}},
		{Key: "OR-2", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckRunning}}},
	})
	live.Tick()
	first := buf.String()
	if !strings.Contains(first, "OR-1 go (ubuntu)") || !strings.Contains(first, "OR-2 go (ubuntu)") {
		t.Fatalf("both waiting tickets must be named while both are pending: %q", first)
	}

	// OR-1 lands; only OR-2 is still awaiting CI.
	liveChecks(1, []collect.Result{
		{Key: "OR-2", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckPassed}}},
	})
	buf.Reset()
	live.Tick()
	second := buf.String()
	if strings.Contains(second, "OR-1") {
		t.Errorf("a ticket that finished must not still be named: %q", second)
	}
	if strings.Contains(second, "OR-2 go (ubuntu)") {
		t.Errorf("the one ticket left waiting must lose its prefix, not keep it: %q", second)
	}
	if !strings.Contains(second, "go (ubuntu) passed") {
		t.Errorf("the remaining ticket's check must still be shown: %q", second)
	}
}

// Nothing pending and nothing in CI means the run that was holding the
// checks row is gone -- so the row must clear, not keep showing the last
// ticket's checks after that ticket finished (OR-310).
func TestChecksClearWhenNothingIsPendingAndNothingIsInCI(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	var buf bytes.Buffer
	live := ui.NewLive(&buf)
	defer live.Close()

	liveChecks(1, []collect.Result{
		{Key: "OR-1", Checks: []collect.Check{{Name: "go (ubuntu)", State: collect.CheckRunning}}},
	})
	live.Tick()
	if !strings.Contains(buf.String(), "go (ubuntu) running") {
		t.Fatalf("the checks must show while the ticket is pending: %q", buf.String())
	}

	// The ticket landed: no pending results, no tickets left in CI.
	liveChecks(0, nil)
	buf.Reset()
	live.Tick()
	if strings.Contains(buf.String(), "go (ubuntu)") {
		t.Errorf("the checks row must clear once nothing is pending and nothing is in CI: %q", buf.String())
	}
}

// A batch member awaiting CI carries no per-ticket rollup -- batchrun.go
// pushes the ONE shared run's checks itself, and its members come back
// PENDING with an empty Checks field (OR-310's own comment on liveChecks
// says as much). So checkRows must not invent a per-ticket row out of a
// batch member just because several of them are pending at once.
func TestBatchMembersProduceNoPerTicketCheckRows(t *testing.T) {
	rows := checkRows([]collect.Result{
		{Key: "OR-1", Verdict: collect.VerdictPending},
		{Key: "OR-2", Verdict: collect.VerdictPending},
		{Key: "OR-3", Verdict: collect.VerdictPending},
	})
	if len(rows) != 0 {
		t.Errorf("a batch member carries no per-ticket rollup, so it must contribute no row: %+v", rows)
	}
}

// When tickets are awaiting CI but none of them carried a rollup -- the
// batch shape above -- the display must show NOTHING for the checks line
// rather than clearing whatever the batch itself already drew there
// (batchrun.go's own ui.LiveChecks call). liveChecks's guard exists exactly
// for this: silence when nothing was collected AND something is still in
// CI, so a batch's own line stands as it drew it.
func TestNoChecksDataWithNonZeroInCIDisplaysNothing(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	var buf bytes.Buffer
	live := ui.NewLive(&buf)
	defer live.Close()

	// Simulate the batch having already pushed its own checks line.
	ui.LiveChecks([]ui.Check{{Name: "go (ubuntu)", State: ui.CheckRunning}})

	// The ordinary-run path sees tickets pending but no per-ticket rollup
	// for any of them -- the batch-member shape.
	liveChecks(2, []collect.Result{
		{Key: "OR-1", Verdict: collect.VerdictPending},
		{Key: "OR-2", Verdict: collect.VerdictPending},
	})

	live.Tick()
	if got := buf.String(); !strings.Contains(got, "go (ubuntu) running") {
		t.Errorf("the batch's own checks line must be left standing: %q", got)
	}
}

// Showing the checks costs nothing beyond the read the verdict already came
// from: every reconcile tick calls Collect exactly once, and the checks line
// is built from that same call's []collect.Result (liveChecks takes no
// fetcher of its own). If displaying them ever cost a second `gh pr view`,
// collects would climb past the number of reconcile ticks; it does not
// (OR-310).
func TestChecksCostNoAdditionalAPICallBeyondTheTickThatFetchedThem(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	s := &spy{
		maxSleeps: 3, queued: issues("FCIA-7"), hold: make(chan struct{}),
		pendingTicks: 3,
		pendingChecks: []collect.Check{
			{Name: "go (ubuntu)", State: collect.CheckRunning},
		},
	}
	releaseAfter(s, 40*time.Millisecond)
	out := runWatch(t, s, Options{MaxConcurrent: 1})

	if !strings.Contains(out, "go (ubuntu) running") {
		t.Fatalf("expected the checks line from the tick's own read: %q", out)
	}
	if s.collects != s.sleeps {
		t.Errorf("collect ran %d times against %d reconcile ticks: showing the checks must not add an extra call",
			s.collects, s.sleeps)
	}
}

// A batch's own checks line -- pushed straight into ui.LiveChecks from
// batchTester.Test, via the batch path -- must not be overwritten by an
// ordinary watch tick's per-member pending Results. Batch members come back
// PENDING with no per-member rollup (collect.Result{Key: m.Key, Verdict:
// VerdictPending} -- Checks left nil), which is exactly what liveChecks must
// tolerate silently rather than clearing the line the batch just drew
// (OR-310).
func TestBatchModeIsUnchangedByWatchsPerMemberPendingResults(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	var buf bytes.Buffer
	live := ui.NewLive(&buf)
	defer live.Close()

	// The batch path drawing its own checks line, as batchTester.Test does.
	ui.LiveChecks([]ui.Check{{Name: "go (ubuntu)", State: ui.CheckRunning}})

	// An ordinary watch tick sees two batch members awaiting CI, each with no
	// per-member rollup -- the shape runBatch's b.Pending branch returns.
	liveChecks(2, []collect.Result{
		{Key: "OR-1", Verdict: collect.VerdictPending},
		{Key: "OR-2", Verdict: collect.VerdictPending},
	})

	live.Tick()
	if got := buf.String(); !strings.Contains(got, "go (ubuntu) running") {
		t.Errorf("the batch's own checks line must survive an ordinary tick's empty per-member results: %q", got)
	}
}

// releaseAfter frees the spy's held jobs once the watcher has had time to
// dispatch and tick. The hold blocks the JOB goroutine, never the loop, so
// the watcher keeps ticking with a run genuinely in flight -- which is the
// only situation the live display exists for.
func releaseAfter(s *spy, d time.Duration) {
	go func() {
		time.Sleep(d)
		close(s.hold)
	}()
}

// OR-240: off a terminal the watcher emits one plain line per run per tick,
// and no cursor control at all.
//
// A redirected log has to stay a log. This is the whole degradation contract,
// and it is checked against the SAME object that pins a region on a terminal:
// there is no second code path to keep honest.
func TestOffTerminalTheWatcherPrintsAPlainHeartbeatPerRun(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	s := &spy{maxSleeps: 3, queued: issues("FCIA-7"), hold: make(chan struct{})}
	releaseAfter(s, 40*time.Millisecond)
	out := runWatch(t, s, Options{MaxConcurrent: 1})

	if strings.Contains(out, "\x1b[") {
		t.Errorf("a non-terminal destination must get no cursor control:\n%q", out)
	}
	var beats []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "calls") {
			beats = append(beats, line)
		}
	}
	if len(beats) == 0 {
		t.Fatalf("expected a plain heartbeat line while a run was in flight:\n%s", out)
	}
	for _, b := range beats {
		if !strings.Contains(b, "FCIA-7") {
			t.Errorf("a heartbeat must name its run: %q", b)
		}
		// One LINE per run per tick: the whole point is that a log stays
		// greppable, so the row must not carry a bar or a sparkline.
		if strings.ContainsAny(b, "━╸─▁▂▃▄▅▆▇█") {
			t.Errorf("the plain form must carry no region glyphs: %q", b)
		}
	}
}

// With nothing in flight and nothing awaiting CI, a tick prints nothing at
// all. A watcher left running overnight must not bury the one line that
// matters under eight hundred saying nothing happened.
func TestATickWithNothingRunningPrintsNothing(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	out := runWatch(t, &spy{maxSleeps: 3}, Options{MaxConcurrent: 2})

	if strings.TrimSpace(out) != "" {
		t.Errorf("a tick with nothing in flight and nothing queued must print nothing:\n%q", out)
	}
}

// A reaped job leaves the live registry, or its row spins forever for work
// that finished. Checked through the display itself rather than through the
// registry's internals: a leak is only a bug because it is visible.
func TestAReapedJobLeavesTheDisplay(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	s := &spy{maxSleeps: 2, queued: issues("FCIA-7"), hold: make(chan struct{})}
	releaseAfter(s, 20*time.Millisecond)
	runOutput := runWatch(t, s, Options{MaxConcurrent: 1, MaxJobs: 1})

	// A SECOND Live would print nothing: the loop's own Tick already reported
	// this row's ending, and a finished row is reported once (OR-265). So the
	// assertion is on what the RUN printed, which is where an operator would
	// actually read it.
	out := runOutput

	// The row STAYS, carrying what became of the ticket.
	//
	// It used to vanish the instant the job was reaped, which answered "is
	// anything running" and threw away "what happened to the thing that just
	// finished" -- so a ticket that pushed and a ticket that failed both left
	// the same way: silently (OR-265).
	if !strings.Contains(out, "FCIA-7") {
		t.Errorf("a finished job left no trace of itself: %q", out)
	}
	if !strings.Contains(out, "ci-wait") {
		t.Errorf("the row must say what became of the ticket: %q", out)
	}
	// And it is no longer counted as RUNNING, which is the half that must
	// still be true: a finished ticket holds no slot.
	if strings.Contains(out, "1 running") {
		t.Errorf("a finished job is still counted as running: %q", out)
	}
}

// A FINISHED row is reported once off a terminal, then goes quiet.
//
// The region keeps a finished ticket on screen to say what became of it,
// which is right for a display redrawn in place. A log APPENDS, so a row that
// outlives its work printed the same line every tick forever -- and on CI
// that buried a held run's "claude is not authenticated" out of the captured
// output entirely. OR-240's rule, broken by OR-265's fix: a tick with nothing
// to say must say nothing.
func TestAFinishedRowIsReportedOnceOffATerminal(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	var buf bytes.Buffer
	live := ui.NewLive(&buf)
	ui.LiveStart("OR-1")
	ui.LiveDone("OR-1", "held")

	live.Tick()
	first := buf.String()
	if !strings.Contains(first, "OR-1") || !strings.Contains(first, "held") {
		t.Fatalf("a finished ticket must report its ending once: %q", first)
	}

	// Every tick after it says nothing about that ticket.
	buf.Reset()
	for i := 0; i < 3; i++ {
		live.Tick()
	}
	if got := buf.String(); strings.Contains(got, "OR-1") {
		t.Errorf("a finished row repeated after it was reported, which buries the lines that matter:\n%q", got)
	}
	live.Close()
}

// A HELD row that has nothing new to say prints nothing.
//
// The first fix for repeated rows (OR-265, #378) silenced only FINISHED rows.
// A held run never finishes: it stays "starting", was exempt from the guard,
// and re-printed the identical line every tick -- six copies on the macOS
// runner -- burying "claude is not authenticated" exactly as the finished row
// had before. OR-240's rule is a tick with nothing to SAY, not a tick with
// nothing finished, so an unchanged line is suppressed whatever the row's
// state.
func TestAHeldRowWithNothingNewIsNotRepeatedOffATerminal(t *testing.T) {
	ui.LiveReset()
	t.Cleanup(ui.LiveReset)

	var buf bytes.Buffer
	live := ui.NewLive(&buf)
	ui.LiveStart("OR-1") // held: never finishes, stage stays as it started

	live.Tick()
	first := buf.String()
	if !strings.Contains(first, "OR-1") {
		t.Fatalf("a new row must print once: %q", first)
	}

	// Nothing about the row changes, so the next ticks must be silent.
	buf.Reset()
	for i := 0; i < 5; i++ {
		live.Tick()
	}
	if got := buf.String(); strings.Contains(got, "OR-1") {
		t.Errorf("an unchanged held row repeated, which buries the fault line beside it:\n%q", got)
	}

	// And a real change prints again: the guard is on the LINE, not the row.
	ui.LiveActivity("OR-1", "implementer")
	buf.Reset()
	live.Tick()
	if got := buf.String(); !strings.Contains(got, "OR-1") {
		t.Errorf("a row whose line changed must print: %q", got)
	}
	live.Close()
}
