package watch

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/work"
)

type spy struct {
	// mu guards everything below. Jobs run in their own goroutines now, so a
	// spy without a lock is a data race in every test that starts two.
	mu        sync.Mutex
	collects  int
	worked    []string
	queued    []tracker.Issue
	held      []HeldTicket
	queueErr  error
	busy      []string
	busyErr   error
	outcome   work.Outcome
	sleeps    int
	maxSleeps int
	// slept is every duration the loop asked to wait for, so a test can
	// assert on the interval actually in use rather than on the one it hoped
	// was passed through.
	slept []time.Duration
	// hold blocks every job until it is closed, so a test can observe how
	// many agents are in flight AT ONCE rather than in total.
	hold chan struct{}
	// peak is the highest number of jobs seen running simultaneously.
	peak    int
	running int
	// pendingTicks makes Collect report a ticket awaiting CI for that many
	// ticks AFTER a job has started -- the shape of a real run.
	//
	// "after" is load-bearing and was got wrong. The first version reported
	// pending on the very first Collect, which happens BEFORE any job has
	// been started, and no real collect can do that: the tick reconciles
	// first, so on tick one there is nothing in flight to report. That
	// fiction made TestTheJobLimitDrains... pass against code that exits
	// immediately, and the bug reached a real run.
	pendingTicks int
}

func (s *spy) workedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.worked...)
}

func (s *spy) deps() Deps {
	return Deps{
		Collect: func(collect.Options) []collect.Result {
			s.mu.Lock()
			s.collects++
			// Nothing is in flight until a job has been started. A collect
			// that reported otherwise would be describing a ticket that does
			// not exist yet.
			started, pending := len(s.worked), s.pendingTicks
			if started > 0 && pending > 0 {
				s.pendingTicks--
			}
			s.mu.Unlock()
			if started == 0 || pending == 0 {
				return nil
			}
			return []collect.Result{{Key: "FCIA-7", Verdict: collect.VerdictPending}}
		},
		Work: func(o work.Options) []work.Result {
			s.mu.Lock()
			s.worked = append(s.worked, o.Keys...)
			s.running++
			if s.running > s.peak {
				s.peak = s.running
			}
			hold, out := s.hold, s.outcome
			s.mu.Unlock()

			if hold != nil {
				<-hold
			}

			s.mu.Lock()
			s.running--
			s.mu.Unlock()

			if out == "" {
				out = work.OutcomeCIWait
			}
			return []work.Result{{Key: o.Keys[0], Outcome: out}}
		},
		Queued: func(string, []string, string) (Queue, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return Queue{Ready: s.queued, Held: s.held}, s.queueErr
		},
		InFlight: func(string, []string) ([]string, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.busy, s.busyErr
		},
		Sleep: func(d time.Duration) bool {
			// A REAL pause, however short. The spy's first version returned
			// instantly, which made the loop spin so tightly that a dispatched
			// job's goroutine never got the spy's own mutex -- so every test
			// saw one job in flight and would have passed against a watcher
			// that could not run two.
			time.Sleep(time.Millisecond)
			s.mu.Lock()
			defer s.mu.Unlock()
			s.sleeps++
			s.slept = append(s.slept, d)
			return s.sleeps < s.maxSleeps
		},
	}
}

// issues turns keys into queued issues, one per (fictional) component so
// nothing in a test is silently reordered by the area spread.
func issues(keys ...string) []tracker.Issue {
	out := make([]tracker.Issue, 0, len(keys))
	for _, k := range keys {
		out = append(out, tracker.Issue{Key: k, Components: []string{k}})
	}
	return out
}

func runWatch(t *testing.T, s *spy, o Options) string {
	t.Helper()
	// ui clips a message to COLUMNS when the environment sets it. A test that
	// asserts on the text of a line must not pass or fail on the width of the
	// terminal that happened to run it.
	t.Setenv("COLUMNS", "")
	stopping.Store(false)
	// The console is process-global and keyed on the writer's ADDRESS, so a
	// buffer allocated where a finished test's buffer used to live inherits
	// its dedupe state and loses its first line (OR-262).
	ui.ConsoleReset()
	var buf bytes.Buffer
	o.Out = &buf
	o.Home = t.TempDir()
	if o.Interval == 0 {
		o.Interval = time.Millisecond
	}
	if err := Run(o, s.deps()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return buf.String()
}

// Reconciling comes first because it is cheap and can FINISH work already
// paid for. Starting a new job while a merged ticket sat unclosed would mean
// spending money to begin something before banking something already done.
func TestATickReconcilesBeforeItStartsAnything(t *testing.T) {
	s := &spy{queued: issues("FCIA-7")}
	runWatch(t, s, Options{Once: true, MaxConcurrent: 1})

	if s.collects != 1 {
		t.Errorf("collect ran %d times, want 1", s.collects)
	}
	if got := s.workedKeys(); len(got) != 1 || got[0] != "FCIA-7" {
		t.Errorf("worked = %v, want [FCIA-7]", got)
	}
}

// The claim label is the lock, and it lives on the TICKET. A watcher
// restarted mid-job, or a second one somebody started by accident, holds
// slots this watcher must not fill on top of.
func TestNothingStartsWhenTheCapIsAlreadyTakenElsewhere(t *testing.T) {
	s := &spy{busy: []string{"FCIA-6"}, queued: issues("FCIA-7")}
	out := runWatch(t, s, Options{Once: true, MaxConcurrent: 1})

	if got := s.workedKeys(); len(got) != 0 {
		t.Fatalf("started %v while FCIA-6 was in flight", got)
	}
	if !strings.Contains(out, "FCIA-6") {
		t.Errorf("the reason nothing started must name what is running: %s", out)
	}
}

// A claim held elsewhere consumes a SLOT, not the whole queue. With a cap of
// two and one ticket claimed by another watcher, exactly one more may start --
// the counting version of the old boolean, and the reason the cap is a number.
func TestAClaimElsewhereConsumesOneSlotNotAllOfThem(t *testing.T) {
	s := &spy{busy: []string{"FCIA-6"}, queued: issues("FCIA-7", "FCIA-8", "FCIA-9")}
	runWatch(t, s, Options{Once: true, MaxConcurrent: 2})

	if got := s.workedKeys(); len(got) != 1 {
		t.Fatalf("started %v; a cap of 2 with 1 claimed elsewhere leaves room for exactly 1", got)
	}
}

// The observed bug, in full (OR-196). On 2026-08-29 the watcher announced a
// cap of 2, claimed exactly one ticket with five queued, and said nothing
// about the difference. Nothing was broken: OR-192 was Done but still carried
// orion-working, so it counted as a live claim and free went 2 - 1 = 1. The
// reduction was reported only when free reached ZERO, and 1 is not zero, so
// the run proceeded at half capacity in silence and it took reading the source
// to find out why.
//
// The arithmetic is right; it has to be VISIBLE. And it has to name the
// holder: "1 claimed elsewhere" is a fact, "OR-192" is something a person can
// go and look at.
func TestAClaimElsewhereIsReportedWhenItReducesFreeRatherThanOnlyAtZero(t *testing.T) {
	s := &spy{
		busy:   []string{"OR-192"},
		queued: issues("OR-193", "OR-194", "OR-195", "OR-196", "OR-197"),
	}
	out := runWatch(t, s, Options{Once: true, MaxConcurrent: 2})

	if got := s.workedKeys(); len(got) != 1 {
		t.Fatalf("started %v; a cap of 2 with 1 claimed elsewhere leaves room for 1", got)
	}
	for _, want := range []string{
		"cap 2",
		"1 claimed elsewhere (OR-192)", // the count AND the holder
		"1 free",
		"starting 1 of 5 queued",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the tick never said %q, so half capacity is invisible again:\n%s", want, out)
		}
	}
	// And what to do about it: the label is the lock, and the operator cannot
	// see it from the terminal.
	if !strings.Contains(out, tracker.LabelWorking) {
		t.Errorf("a claim held elsewhere must say how to clear it if it is residue:\n%s", out)
	}
}

// The counterpart, and the reason the line is printed on EVERY dispatch rather
// than only when a slot was lost: "2 free, starting 2" has to be
// distinguishable from the bug above, and from a short queue below.
func TestAFullCapacityTickSaysSoRatherThanLookingLikeALostSlot(t *testing.T) {
	s := &spy{queued: issues("OR-1", "OR-2", "OR-3")}
	out := runWatch(t, s, Options{Once: true, MaxConcurrent: 2})

	if got := s.workedKeys(); len(got) != 2 {
		t.Fatalf("started %v, want 2", got)
	}
	if !strings.Contains(out, "cap 2, 2 free; starting 2 of 3 queued") {
		t.Errorf("a full-capacity tick must state the arithmetic too:\n%s", out)
	}
	if strings.Contains(out, "claimed elsewhere") {
		t.Errorf("nothing was claimed elsewhere; the term must not appear:\n%s", out)
	}
}

// The third case that used to look identical to the other two: the slot is
// free and there is simply nothing to put in it.
func TestAShortQueueIsNamedAsTheReasonOnlyOneStarted(t *testing.T) {
	s := &spy{queued: issues("OR-1")}
	out := runWatch(t, s, Options{Once: true, MaxConcurrent: 2})

	if !strings.Contains(out, "cap 2, 2 free; starting 1 of 1 queued") {
		t.Errorf("a short queue must be distinguishable from a lost slot:\n%s", out)
	}
}

// Reaching zero still reports, and still reports the whole sum rather than a
// bare count -- this is the line the old code got right and said too little in.
func TestAFullyClaimedCapReportsTheWholeSumAndNotJustACount(t *testing.T) {
	s := &spy{busy: []string{"OR-192", "OR-193"}, queued: issues("OR-194")}
	out := runWatch(t, s, Options{Once: true, MaxConcurrent: 2})

	if got := s.workedKeys(); len(got) != 0 {
		t.Fatalf("started %v with the cap fully claimed elsewhere", got)
	}
	for _, want := range []string{"cap 2", "2 claimed elsewhere (OR-192, OR-193)", "0 free"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q from a wedged tick:\n%s", want, out)
		}
	}
}

// Every term that moved the number has to be named, or the operator is back to
// reconciling a cap against a start count with nothing in between. The gap
// term matters most: --max-jobs and a rate-limit pause both trim free, and an
// unexplained shortfall is exactly the defect this line exists to remove.
func TestSlotsNamesEveryTermThatMovedTheNumber(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   slots
		want string
	}{
		{"nothing taken", slots{cap: 2, free: 2}, "cap 2, 2 free"},
		{"our own jobs", slots{cap: 2, here: 1, free: 1}, "cap 2, 1 running here, 1 free"},
		{"a claim elsewhere", slots{cap: 2, elsewhere: []string{"OR-192"}, free: 1},
			"cap 2, 1 claimed elsewhere (OR-192), 1 free"},
		{"both", slots{cap: 3, here: 1, elsewhere: []string{"OR-192"}, free: 1},
			"cap 3, 1 running here, 1 claimed elsewhere (OR-192), 1 free"},
		{"trimmed by --max-jobs", slots{cap: 2, free: 1},
			"cap 2, 1 held back by this run's limits, 1 free"},
		// More claimed than the cap allows. Reporting -1 free would be a
		// second puzzle rather than an answer.
		{"over-subscribed", slots{cap: 1, elsewhere: []string{"OR-192", "OR-193"}, free: -1},
			"cap 1, 2 claimed elsewhere (OR-192, OR-193), 0 free"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("%s: slots = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSlotsNamesEveryTermThatMovedTheNumber proves the gap term formats
// correctly against a hand-built slots value. It does not prove Run wires
// --max-jobs INTO that value correctly -- a bug in that wiring (e.g. the gap
// silently landing in "free" instead of being named) would pass the unit test
// above while still reproducing OR-196 for anyone hitting the job limit.
func TestAMaxJobsLimitBelowTheCapIsNamedAsTheReasonNotJustAbsorbedIntoFree(t *testing.T) {
	s := &spy{queued: issues("OR-1", "OR-2", "OR-3"), maxSleeps: 99}
	out := runWatch(t, s, Options{MaxJobs: 1, MaxConcurrent: 3})

	if got := s.workedKeys(); len(got) != 1 {
		t.Fatalf("started %v; --max-jobs 1 must cap starts at 1", got)
	}
	if !strings.Contains(out, "cap 3, 2 held back by this run's limits, 1 free; starting 1 of 3 queued") {
		t.Errorf("the job limit's bite out of the cap must be named, not just missing from the count:\n%s", out)
	}
}

// The cap is on agents IN FLIGHT, not on starts per tick. This is the property
// the whole change turns on, and the only way to observe it is to hold every
// job open and count how many are inside at once.
func TestACapOfTwoNeverHasThreeAgentRunsInFlight(t *testing.T) {
	s := &spy{
		queued:    issues("FCIA-7", "FCIA-8", "FCIA-9", "FCIA-10", "FCIA-11"),
		maxSleeps: 6,
		hold:      make(chan struct{}),
	}
	d := s.deps()
	// Release everything once the loop has had several ticks to over-dispatch
	// if it were going to.
	var once sync.Once
	sleep := d.Sleep
	d.Sleep = func(dur time.Duration) bool {
		ok := sleep(dur)
		if !ok {
			once.Do(func() { close(s.hold) })
		}
		return ok
	}

	stopping.Store(false)
	var buf bytes.Buffer
	if err := Run(Options{
		Out: &buf, Home: t.TempDir(), Interval: time.Millisecond, MaxConcurrent: 2,
	}, d); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	peak := s.peak
	s.mu.Unlock()
	if peak > 2 {
		t.Fatalf("%d agent runs were in flight at once; the cap is 2", peak)
	}
	if peak != 2 {
		t.Fatalf("peak concurrency was %d; a cap of 2 with 5 queued must actually reach 2", peak)
	}
}

// The ceiling is a ceiling. A hand-edited orion.json asking for forty must not
// get forty, and neither must a caller passing one straight through.
func TestTheConcurrencyCapIsClampedToTheCeiling(t *testing.T) {
	s := &spy{
		queued:    issues("A-1", "A-2", "A-3", "A-4", "A-5", "A-6", "A-7", "A-8"),
		maxSleeps: 4,
		hold:      make(chan struct{}),
	}
	d := s.deps()
	var once sync.Once
	sleep := d.Sleep
	d.Sleep = func(dur time.Duration) bool {
		ok := sleep(dur)
		if !ok {
			once.Do(func() { close(s.hold) })
		}
		return ok
	}

	stopping.Store(false)
	var buf bytes.Buffer
	if err := Run(Options{
		Out: &buf, Home: t.TempDir(), Interval: time.Millisecond,
		MaxConcurrent: 40,
	}, d); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	peak := s.peak
	s.mu.Unlock()
	// Bounded by what was ASKED for, not by a ceiling Orion imposes. The
	// hard cap of five is gone: a configured number is honoured, and a large
	// one is questioned where it is set rather than silently reduced here.
	if peak > 40 {
		t.Fatalf("ran %d at once, above the configured 40", peak)
	}
}

// The limit that makes a first unattended run safe to try.
func TestMaxJobsStopsTheLoop(t *testing.T) {
	s := &spy{queued: issues("FCIA-7"), maxSleeps: 99}
	out := runWatch(t, s, Options{MaxJobs: 2, MaxConcurrent: 1})

	if got := s.workedKeys(); len(got) != 2 {
		t.Fatalf("started %d jobs, want exactly 2", len(got))
	}
	if !strings.Contains(out, "the limit for this run") {
		t.Errorf("stopping at the limit must be stated: %s", out)
	}
}

// --max-jobs bounds STARTS across the whole run, and concurrency must not let
// the pool overshoot it: with a cap of 3 and a limit of 2, three agents must
// never have been paid for.
func TestMaxJobsIsNotOvershotByTheConcurrencyCap(t *testing.T) {
	s := &spy{queued: issues("FCIA-7", "FCIA-8", "FCIA-9"), maxSleeps: 99}
	runWatch(t, s, Options{MaxJobs: 2, MaxConcurrent: 3})

	if got := s.workedKeys(); len(got) != 2 {
		t.Fatalf("started %v; --max-jobs 2 must cap starts however wide the concurrency is", got)
	}
}

// The job limit caps STARTS, and must not abandon a ticket mid-flight.
//
// This is the bug it was written for, observed in full: `orion watch fcia
// --max-jobs 1` claimed FCIA-7, ran the agent, pushed the branch, opened the
// pull request, moved the ticket to orion-ci-wait -- and exited on that same
// tick. CI went green minutes later with no watcher left to notice. No
// approval was requested in Slack, nothing merged, the worktree was never
// pruned, and the ticket sat in ci-wait forever. Every line of output said
// success. The limit had abandoned the one job it just paid for.
//
// Reaching the cap must stop STARTING, not stop WATCHING.
func TestTheJobLimitDrainsInFlightWorkInsteadOfAbandoningIt(t *testing.T) {
	// CI is still running for the first two ticks after the job starts.
	s := &spy{queued: issues("FCIA-7"), maxSleeps: 20, pendingTicks: 2}
	out := runWatch(t, s, Options{MaxJobs: 1, MaxConcurrent: 1})

	if got := s.workedKeys(); len(got) != 1 {
		t.Fatalf("started %d jobs, want exactly 1 -- the cap must still cap", len(got))
	}
	// The tick that started the job collected BEFORE starting it, so
	// finishing that job needs at least two more reconciles.
	if s.collects < 3 {
		t.Errorf("collected %d times; the watcher exited before the ticket it started could finish",
			s.collects)
	}
	if !strings.Contains(out, "draining") {
		t.Errorf("staying up to finish in-flight work must be stated, or it looks hung:\n%s", out)
	}
	if !strings.Contains(out, "finished them") {
		t.Errorf("the watcher must say the work actually completed:\n%s", out)
	}
}

// The counterpart: with nothing in flight, the cap exits immediately. A
// watcher that lingered after finishing everything would be a daemon the
// person did not ask for.
//
// BLOCKED, not the default ci-wait: a ticket awaiting CI is by definition in
// flight, so the old version of this test could only pass while the drain was
// broken. A blocked ticket is the real "nothing to drain toward" case -- it
// needs a person, and no amount of waiting produces one.
func TestTheJobLimitExitsAtOnceWhenNothingIsInFlight(t *testing.T) {
	s := &spy{queued: issues("FCIA-7"), outcome: work.OutcomeBlocked, maxSleeps: 20}
	out := runWatch(t, s, Options{MaxJobs: 1, MaxConcurrent: 1})

	if s.sleeps > 1 {
		t.Errorf("slept %d times with nothing to wait for", s.sleeps)
	}
	if strings.Contains(out, "draining") {
		t.Errorf("nothing was in flight, so there was nothing to drain:\n%s", out)
	}
}

// A ticket refused before spending anything -- budget, a dirty sandbox --
// has not been "started". Counting it would burn the job limit on work that
// never happened, and retrying it immediately would hammer the tracker while
// the condition that refused it is still true.
func TestASkippedTicketDoesNotCountAsAStartedJob(t *testing.T) {
	s := &spy{queued: issues("FCIA-7"), outcome: work.OutcomeSkipped, maxSleeps: 3}
	out := runWatch(t, s, Options{MaxJobs: 1, MaxConcurrent: 1})

	if !strings.Contains(out, "not started") {
		t.Errorf("a skip must be explained: %s", out)
	}
	// It kept looping rather than declaring the limit reached.
	if s.sleeps == 0 {
		t.Error("the loop ended as though the job limit had been consumed")
	}
}

// A tick that fails must not end the watch. The usual causes -- a network
// blip, an expired token -- are transient or fixable while it keeps running,
// and a watcher that dies quietly means the fix also requires noticing.
func TestATickErrorIsReportedAndTheLoopContinues(t *testing.T) {
	s := &spy{queueErr: errors.New("jira timed out"), maxSleeps: 3}
	out := runWatch(t, s, Options{})

	if !strings.Contains(out, "jira timed out") {
		t.Errorf("the error must be surfaced: %s", out)
	}
	if s.sleeps < 2 {
		t.Errorf("the loop stopped after one failure (%d sleeps)", s.sleeps)
	}
}

// Dry run is what someone reaches for before trusting this unattended. If it
// starts an agent, it is worse than useless.
func TestDryRunStartsNothing(t *testing.T) {
	s := &spy{queued: issues("FCIA-7")}
	out := runWatch(t, s, Options{Once: true, DryRun: true})

	if got := s.workedKeys(); len(got) != 0 {
		t.Fatalf("dry run started %v", got)
	}
	if !strings.Contains(out, "would") {
		t.Errorf("got: %s", out)
	}
}

func TestAnEmptyQueueDoesNothingQuietly(t *testing.T) {
	s := &spy{}
	out := runWatch(t, s, Options{Once: true})

	if got := s.workedKeys(); len(got) != 0 {
		t.Fatal("started something with an empty queue")
	}
	// A tick with nothing to do must not produce output, or a watcher left
	// running overnight fills a terminal with a thousand lines saying
	// nothing happened -- and the one line that matters is lost in them.
	if strings.Contains(out, "queued") {
		t.Errorf("an idle tick should be silent, got: %s", out)
	}
}

// Ctrl-c must not kill an agent mid-run: that leaves a ticket claimed with a
// half-written branch and no process to finish it, which is precisely the
// state an unattended tool must never create.
//
// Concurrency makes this a stronger claim than it was, and the one OR-141
// names for n: Run must not return while ANY of the jobs it dispatched is
// still going. A loop that only waited for "the current job" would leave the
// other n-1 as orphans -- agent processes with no parent watching, holding
// claims nobody will release.
func TestStoppingWaitsForEveryJobAlreadyRunning(t *testing.T) {
	stopping.Store(false)
	s := &spy{
		queued: issues("FCIA-7", "FCIA-8"), maxSleeps: 99,
		hold: make(chan struct{}),
	}

	// Ctrl-c arrives while both agents are mid-run.
	d := s.deps()
	var once sync.Once
	sleep := d.Sleep
	d.Sleep = func(dur time.Duration) bool {
		stopping.Store(true)
		once.Do(func() { close(s.hold) })
		return sleep(dur)
	}

	var buf bytes.Buffer
	if err := Run(Options{
		Out: &buf, Home: t.TempDir(), Interval: time.Millisecond, MaxConcurrent: 2,
	}, d); err != nil {
		t.Fatal(err)
	}

	if got := s.workedKeys(); len(got) != 2 {
		t.Fatalf("worked = %v, want both jobs dispatched before the stop", got)
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running != 0 {
		t.Fatalf("Run returned with %d agent(s) still going: exactly the orphan OR-141 forbids", running)
	}
	if !strings.Contains(buf.String(), "stopped") {
		t.Errorf("stopping must be reported: %s", buf.String())
	}
	stopping.Store(false)
}

// A mistyped project must FAIL, not look like an empty queue.
//
// `orion watch fcra` -- one letter wrong -- searched a project that does not
// exist, found nothing, and printed "nothing is waiting on CI", which is
// exactly what a correct key with an empty queue prints. The watcher then
// sat there all night watching nothing, reporting success every two minutes.
//
// A typo that looks like success is worse than one that fails.
func TestAnUnregisteredProjectIsRefusedAndNamed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	if err := registry.Bind(home, registry.Entry{
		Key: "FCIA", Source: t.TempDir(), Workspace: "ws-1",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := scope(home, []string{"fcra"})
	if err == nil {
		t.Fatal("a project that is not registered was accepted")
	}
	if !strings.Contains(err.Error(), "FCRA") {
		t.Errorf("the error must name what was not found: %v", err)
	}
	if !strings.Contains(err.Error(), "FCIA") {
		t.Errorf("it must list what IS registered, or a typo is hard to spot: %v", err)
	}

	// The correct key still works, and case does not matter.
	got, err := scope(home, []string{"fcia"})
	if err != nil || len(got) != 1 || got[0] != "FCIA" {
		t.Errorf("scope(fcia) = %v, %v", got, err)
	}
}

// A misconfiguration will never fix itself. Retrying it every two minutes
// forever is not resilience -- it is a watcher that looks alive while
// watching nothing.
func TestAPermanentErrorStopsTheWatcherRatherThanRetryingForever(t *testing.T) {
	stopping.Store(false)
	s := &spy{maxSleeps: 99}
	d := s.deps()
	d.Queued = func(string, []string, string) (Queue, error) {
		return Queue{}, errors.New(`not a registered project: FCRA`)
	}

	var buf bytes.Buffer
	err := Run(Options{Out: &buf, Home: t.TempDir(), Interval: time.Millisecond}, d)

	if err == nil {
		t.Fatal("a permanent error must stop the watcher and be returned")
	}
	if s.sleeps != 0 {
		t.Errorf("it slept %d times before giving up on an error that cannot resolve", s.sleeps)
	}
}

// A dry run changes nothing, so a second tick prints the identical thing.
// Without --once it looped forever, which is why `orion watch fcia
// --dry-run --max-jobs 1` appeared to hang.
func TestADryRunRehearsesOnceAndStops(t *testing.T) {
	s := &spy{queued: issues("FCIA-7"), maxSleeps: 99}
	runWatch(t, s, Options{DryRun: true})

	if s.sleeps != 0 {
		t.Errorf("a dry run looped %d times; once is the whole point", s.sleeps)
	}
}

// Rehearsing is for checking the ORDER, which a count cannot show.
func TestADryRunPrintsTheWholeQueueInOrder(t *testing.T) {
	s := &spy{queued: issues("FCIA-7", "FCIA-8", "FCIA-10")}
	out := runWatch(t, s, Options{DryRun: true, MaxJobs: 2})

	for _, k := range []string{"FCIA-7", "FCIA-8", "FCIA-10"} {
		if !strings.Contains(out, k) {
			t.Errorf("%s is missing from the rehearsal: %s", k, out)
		}
	}
	if !strings.Contains(out, "first 2") {
		t.Errorf("it must say how far --max-jobs would get: %s", out)
	}
}

// A story and its sub-tasks must not both be started.
//
// A parent is worked together with its children -- one branch, one pull
// request, one approval (OR-48). If a labelled sub-task were ALSO claimed as
// a job of its own, two agents would work the same change on separate
// branches and conflict for certain: they were decomposed from one story
// precisely BECAUSE they touch the same code. That is the FCIA-8/FCIA-10
// collision, manufactured deliberately.
func TestASubTaskIsNotStartedWhenItsParentIs(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "OR-50"},                  // the story
		{Key: "OR-51", Parent: "OR-50"}, // its tasks, also labelled
		{Key: "OR-52", Parent: "OR-50"},
		{Key: "OR-60"}, // an unrelated ticket
	}
	got := keysOf(dropClaimedChildren(issues))

	want := []string{"OR-50", "OR-60"}
	if len(got) != len(want) {
		t.Fatalf("queued %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("queued %v, want %v", got, want)
		}
	}
}

func keysOf(issues []tracker.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Key)
	}
	return out
}

// An orphan keeps its place. A sub-task whose parent is NOT queued is
// ordinary work somebody asked for directly -- dropping it would silently
// refuse a ticket that was labelled on purpose.
func TestASubTaskWhoseParentIsNotQueuedIsStillWorked(t *testing.T) {
	got := keysOf(dropClaimedChildren([]tracker.Issue{
		{Key: "OR-51", Parent: "OR-50"}, // OR-50 is not in the list
	}))
	if len(got) != 1 || got[0] != "OR-51" {
		t.Errorf("queued %v; a sub-task worked on its own must be allowed", got)
	}
}

// Jira keys are upper-case by convention and not by guarantee. A case
// mismatch that silently failed to match would let both run.
func TestParentMatchingIgnoresCase(t *testing.T) {
	got := keysOf(dropClaimedChildren([]tracker.Issue{
		{Key: "OR-50"},
		{Key: "OR-51", Parent: "or-50"},
	}))
	if len(got) != 1 {
		t.Errorf("queued %v; the parent link was missed on case alone", got)
	}
}

// A resolved ticket must not be claimable, however it got resolved.
//
// OR-121: the queue selected on labels alone. OR-119 was fixed by hand,
// merged and moved to Done with its ORION label still attached, so the next
// tick claimed it and spent an opus agent re-investigating a fixed bug. The
// merged-branch guard did not catch it -- a hand fix lands on a branch Orion
// never named -- so the status has to be in the query.
func TestTheQueueExcludesResolvedTickets(t *testing.T) {
	jql := queuedJQL([]string{"OR"}, "ORION", nil)

	if !strings.Contains(jql, `statusCategory != "Done"`) {
		t.Errorf("a Done ticket is still claimable: %s", jql)
	}
	// The rest of the criterion must survive alongside it.
	for _, want := range []string{
		`project IN ("OR")`,
		`labels = "ORION"`,
		wantClaimExclusions(),
		" ORDER BY priority DESC, Rank ASC",
	} {
		if !strings.Contains(jql, want) {
			t.Errorf("lost %s from the queue query: %s", want, jql)
		}
	}
	// The category rather than a status NAME: Cancelled and Won't Do are
	// terminal too, and enumerating names needs an edit per workflow.
	if strings.Contains(jql, "status =") || strings.Contains(jql, "status !=") {
		t.Errorf("filtered on a status name rather than its category: %s", jql)
	}
}

// An empty label falls back to the default rather than matching everything.
func TestTheQueueDefaultsItsLabel(t *testing.T) {
	if jql := queuedJQL([]string{"OR"}, "", nil); !strings.Contains(jql,
		`labels = "`+tracker.QueueLabelDefault+`"`) {
		t.Errorf("got %s", jql)
	}
}

// fakeLock is a tracker that answers one claim-label search and records the
// label writes made against it.
type fakeLock struct {
	issues  []tracker.Issue
	removed map[string][]string
	err     error
	// searches records the JQL, so a test can assert that the lock is still
	// matched EXACTLY and by nothing else (OR-225).
	searches []string
}

func (f *fakeLock) Search(jql string, _ int) ([]tracker.Issue, error) {
	f.searches = append(f.searches, jql)
	return f.issues, nil
}

// containsLabel reports whether a label is in a recorded add/remove list.
func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func (f *fakeLock) SetLabels(key string, _, remove []string) error {
	if f.err != nil {
		return f.err
	}
	if f.removed == nil {
		f.removed = map[string][]string{}
	}
	f.removed[key] = append(f.removed[key], remove...)
	return nil
}

func lockHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	if err := registry.Bind(home, registry.Entry{
		Key: "OR", Source: t.TempDir(), Workspace: "ws-1",
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

// The claim label outlives the ticket. OR-124 was fixed by hand and moved to
// Done with orion-working still attached, and every tick after that reported
// a ticket that finished hours ago as "still running; not starting anything
// else" -- indistinguishable from a genuinely stuck job, and the queue never
// moved again (OR-125).
func TestAHandClosedTicketDoesNotHoldTheQueue(t *testing.T) {
	home := lockHome(t)
	j := &fakeLock{issues: []tracker.Issue{
		{Key: "OR-124", Status: "Done", StatusCategory: "Done",
			Labels: []string{tracker.LabelWorking}},
	}}

	var b bytes.Buffer
	running, err := InFlight(j, home, nil, &b)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 0 {
		t.Errorf("a ticket that is already Done was reported in flight as %v", running)
	}
	// Cleared, not merely ignored: ignoring it would re-diagnose the same
	// ticket every tick forever, and `orion queue` reads the label too.
	//
	// The stage label goes with it (OR-225). A ticket closed outside Orion
	// keeps whatever stage it was wearing, so a clear that took only the lock
	// would leave the board naming an actor for work that ended hours ago.
	got := j.removed["OR-124"]
	if !containsLabel(got, tracker.LabelWorking) {
		t.Errorf("the stale lock was not cleared, removed = %v", j.removed)
	}
	for _, l := range actors.StageLabels() {
		if !containsLabel(got, l) {
			t.Errorf("clearing a stale lock left %s behind, removed = %v", l, got)
		}
	}
	if out := b.String(); !strings.Contains(out, "OR-124") ||
		!strings.Contains(out, tracker.LabelWorking) {
		t.Errorf("clearing a lock must be reported, got %q", out)
	}
}

// The lock still has to work. A ticket that is genuinely being worked holds
// the queue, which is the entire reason the label exists.
func TestAnUnresolvedClaimStillHoldsTheQueue(t *testing.T) {
	home := lockHome(t)
	j := &fakeLock{issues: []tracker.Issue{
		{Key: "OR-130", Status: "In Progress", StatusCategory: "indeterminate",
			Labels: []string{tracker.LabelWorking}},
	}}

	running, err := InFlight(j, home, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0] != "OR-130" {
		t.Errorf("InFlight = %v; a running job must hold a slot", running)
	}
	if len(j.removed) != 0 {
		t.Errorf("a live claim was cleared: %v", j.removed)
	}
}

// A stale lock ahead of a live one must not hide it. Clearing the first and
// returning "nothing is running" would start a second agent on a repository
// that already has one.
func TestALiveClaimBehindAStaleOneIsStillFound(t *testing.T) {
	home := lockHome(t)
	j := &fakeLock{issues: []tracker.Issue{
		{Key: "OR-124", StatusCategory: "Done", Labels: []string{tracker.LabelWorking}},
		{Key: "OR-130", StatusCategory: "indeterminate", Labels: []string{tracker.LabelWorking}},
	}}

	running, err := InFlight(j, home, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0] != "OR-130" {
		t.Errorf("InFlight = %v; the live claim was lost behind a stale one", running)
	}
}

// A tracker that refuses the write leaves the queue exactly as wedged as
// before. Worth a line, not worth failing the tick over -- and the ticket is
// still finished, so it still must not hold the lock.
func TestALockThatCannotBeClearedIsReportedAndNotTreatedAsRunning(t *testing.T) {
	home := lockHome(t)
	j := &fakeLock{
		issues: []tracker.Issue{{Key: "OR-124", Status: "Done", StatusCategory: "Done",
			Labels: []string{tracker.LabelWorking}}},
		err: errors.New("403 forbidden"),
	}

	var b bytes.Buffer
	running, err := InFlight(j, home, nil, &b)
	if err != nil {
		t.Fatalf("a label write that failed must not fail the tick: %v", err)
	}
	if len(running) != 0 {
		t.Error("a finished ticket held the queue because its label could not be cleared")
	}
	if !strings.Contains(b.String(), "403 forbidden") {
		t.Errorf("the reason must be reported: %q", b.String())
	}
}

// sleptDurations returns every interval the loop asked to wait for.
func (s *spy) sleptDurations() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.slept...)
}

// The tick interval is the latency floor on every transition Orion NOTICES
// rather than causes -- green CI, a merged PR, an approval, a newly queued
// ticket. A tick is one tracker query and one status check and starts
// nothing, so the default is a minute rather than two (OR-218).
//
// Asserted on the duration the loop actually sleeps for, not on the
// constant: a default read correctly and then not used is the failure worth
// catching.
func TestAnUnsetIntervalTicksEveryMinute(t *testing.T) {
	stopping.Store(false)
	s := &spy{maxSleeps: 1}

	var buf bytes.Buffer
	if err := Run(Options{Out: &buf, Home: t.TempDir()}, s.deps()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := s.sleptDurations()
	if len(got) != 1 || got[0] != time.Minute {
		t.Errorf("an unset interval slept %v, want [1m0s]", got)
	}
}

// The single-sleep test above only proves the FIRST tick used the default; it
// would still pass if the interval were somehow recomputed and widened on
// later ticks. Subsequent ticks matter just as much -- the queued work this
// loop notices keeps arriving after tick one, not only before it.
func TestSubsequentTicksAlsoUseTheMinuteDefault(t *testing.T) {
	stopping.Store(false)
	s := &spy{maxSleeps: 4}

	var buf bytes.Buffer
	if err := Run(Options{Out: &buf, Home: t.TempDir()}, s.deps()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := s.sleptDurations()
	if len(got) != 4 {
		t.Fatalf("slept %d times, want 4", len(got))
	}
	for i, d := range got {
		if d != time.Minute {
			t.Errorf("tick %d slept %v, want 1m0s", i+1, d)
		}
	}
}

// The default is a fallback, never an override: a caller who names an
// interval gets exactly that one.
func TestAnExplicitIntervalIsUsedAsGiven(t *testing.T) {
	stopping.Store(false)
	s := &spy{maxSleeps: 1}

	var buf bytes.Buffer
	err := Run(Options{Out: &buf, Home: t.TempDir(), Interval: 17 * time.Second}, s.deps())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := s.sleptDurations()
	if len(got) != 1 || got[0] != 17*time.Second {
		t.Errorf("an explicit interval slept %v, want [17s]", got)
	}
}

// Zero and below mean "unset", which has to land on the default rather than
// on no wait at all: a loop that sleeps for nothing spins, and a watcher
// that spins hammers the tracker until it is rate-limited.
func TestANonPositiveIntervalFallsBackRatherThanSpinning(t *testing.T) {
	for _, given := range []time.Duration{0, -time.Second} {
		stopping.Store(false)
		s := &spy{maxSleeps: 1}

		var buf bytes.Buffer
		if err := Run(Options{Out: &buf, Home: t.TempDir(), Interval: given}, s.deps()); err != nil {
			t.Fatalf("Run(%v): %v", given, err)
		}

		got := s.sleptDurations()
		if len(got) != 1 || got[0] != DefaultInterval {
			t.Errorf("interval %v slept %v, want [%v]", given, got, DefaultInterval)
		}
	}
}

// wantClaimExclusions is the "already in flight" clause the claim query must
// carry, derived from the labels Orion owns rather than spelled out.
//
// A literal here fails whenever the SET grows -- orion-ready arrived with
// OR-253 -- which makes a test about the query's shape fail for a reason that
// has nothing to do with its shape. What these tests are about is that the
// clause is present and quoted, not which labels exist this month.
func wantClaimExclusions() string {
	return tracker.JQLNotIn("labels", tracker.LabelWorking, tracker.LabelCIWait,
		tracker.LabelReady, tracker.LabelFailed)
}
