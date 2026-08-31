package watch

import (
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/work"
)

// A watcher that keeps claiming while the environment is broken converts one
// fixable problem into a queue of released tickets. Every subsequent ticket
// fails identically until a human fixes it, so there is nothing to gain by
// trying the next one (OR-212).
//
// It HOLDS rather than exits (OR-214). Exiting was right about the danger and
// wrong about the remedy: it meant the thirty-second repair was only picked up
// whenever somebody next noticed the watcher had died. The hold keeps the
// property that matters -- nothing is claimed while the fault stands -- and
// the tick that finds the environment healthy resumes by itself.
func TestAnEnvironmentalFaultHoldsTheQueueRatherThanDrainingIt(t *testing.T) {
	home := t.TempDir()
	s := &spy{
		queued:    issues("OR-1", "OR-2", "OR-3", "OR-4"),
		outcome:   work.OutcomeHeld,
		maxSleeps: 6,
	}
	d := s.deps()
	inner := d.Work
	d.Work = func(o work.Options) []work.Result {
		res := inner(o)
		// What a real held run does on its way out: it files the fault, which
		// is the record the loop gates on and a reaction is read against.
		if _, _, err := work.RecordFault(o.Home,
			work.Fault{Kind: work.FaultClaudeAuth,
				Cause: "claude is not authenticated: Anthropic profile login expired",
				Fix:   "Run: claude, sign in, then react below."},
			res[0].Key, "", nil, time.Now()); err != nil {
			t.Error(err)
		}
		res[0].Note = "claude is not authenticated: Anthropic profile login expired. " +
			"Run: claude, sign in, then restart the watcher."
		return res
	}
	// Still broken on every tick, so nothing is ever released.
	d.Release = work.ReleaseDeps{
		Recheck: func(work.FaultKind) (string, string) { return "FAIL", "the CLI is not signed in" },
	}

	t.Setenv("COLUMNS", "")
	stopping.Store(false)
	var buf strings.Builder
	if err := Run(Options{
		Out: &buf, Home: home, Interval: time.Millisecond, MaxConcurrent: 1,
	}, d); err != nil {
		t.Fatal(err)
	}

	if got := s.workedKeys(); len(got) != 1 {
		t.Fatalf("claimed %v; the queue must not be drained against a broken environment", got)
	}
	out := buf.String()
	if !strings.Contains(out, "claude is not authenticated") ||
		!strings.Contains(out, "sign in") {
		t.Errorf("the watcher held without naming the cause or the fix:\n%s", out)
	}
	// Collect keeps running. Reconciling work already paid for costs nothing
	// and does not depend on whatever broke, and stopping it would leave
	// finished work unclosed for the length of a fault it has nothing to do
	// with.
	if s.collects < 2 {
		t.Errorf("collect stopped along with the dispatch: %d pass(es)", s.collects)
	}
}

// And the resume. This is the whole point of holding rather than exiting: the
// fix lands, the next tick's check agrees, and the queue moves again with
// nobody having restarted anything.
func TestAHealthyTickReleasesTheHoldAndResumes(t *testing.T) {
	home := t.TempDir()
	if _, _, err := work.RecordFault(home,
		work.Fault{Kind: work.FaultClaudeAuth, Cause: "not signed in", Fix: "sign in"},
		"OR-1", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}

	s := &spy{queued: issues("OR-1", "OR-2"), maxSleeps: 4}
	d := s.deps()
	// Broken on the first check, healthy on every one after -- somebody fixed
	// it between ticks, which is exactly the case this exists for.
	checks := 0
	d.Release = work.ReleaseDeps{
		Recheck: func(work.FaultKind) (string, string) {
			checks++
			if checks == 1 {
				return "FAIL", "the CLI is not signed in"
			}
			return "OK", "signed in as a@b"
		},
	}

	t.Setenv("COLUMNS", "")
	stopping.Store(false)
	var buf strings.Builder
	if err := Run(Options{
		Out: &buf, Home: home, Interval: time.Millisecond, MaxConcurrent: 1,
	}, d); err != nil {
		t.Fatal(err)
	}

	if got := s.workedKeys(); len(got) == 0 {
		t.Fatalf("the queue never resumed after the environment came back")
	}
	if h := work.Holds(home); len(h) != 0 {
		t.Errorf("the hold survived a healthy check: %+v", h)
	}
	if !strings.Contains(buf.String(), "healthy again") {
		t.Errorf("the resume was silent:\n%s", buf.String())
	}
}

// The one case where going quiet is better than looking alive: the fault could
// not be RECORDED, so nothing exists to release the queue or to bound the
// retry, and the next tick would claim into the same fault forever.
func TestAFaultThatCannotBeRecordedStopsTheWatcher(t *testing.T) {
	s := &spy{
		queued:    issues("OR-1", "OR-2", "OR-3"),
		outcome:   work.OutcomeHeld,
		maxSleeps: 6,
	}
	d := s.deps()
	inner := d.Work
	d.Work = func(o work.Options) []work.Result {
		res := inner(o)
		res[0].Note = "claude is not authenticated. Run: claude, sign in."
		return res // and deliberately files nothing
	}

	t.Setenv("COLUMNS", "")
	stopping.Store(false)
	var buf strings.Builder
	if err := Run(Options{
		Out: &buf, Home: t.TempDir(), Interval: time.Millisecond, MaxConcurrent: 1,
	}, d); err != nil {
		t.Fatal(err)
	}

	if got := s.workedKeys(); len(got) != 1 {
		t.Fatalf("claimed %v; the queue must not be drained against a fault nothing can clear", got)
	}
	out := buf.String()
	if !strings.Contains(out, "the hold could not be recorded") {
		t.Errorf("the stop does not say why it gave up rather than holding:\n%s", out)
	}
	if !strings.Contains(out, "nothing is labelled failed") {
		t.Errorf("the stop does not say the queue was left clean:\n%s", out)
	}
}
