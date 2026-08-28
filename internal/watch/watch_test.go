package watch

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/work"
)

type spy struct {
	collects  int
	worked    []string
	queued    []string
	queueErr  error
	busy      bool
	busyKey   string
	busyErr   error
	outcome   work.Outcome
	sleeps    int
	maxSleeps int
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

func (s *spy) deps() Deps {
	return Deps{
		Collect: func(collect.Options) []collect.Result {
			s.collects++
			// Nothing is in flight until a job has been started. A collect
			// that reported otherwise would be describing a ticket that does
			// not exist yet.
			if len(s.worked) == 0 {
				return nil
			}
			if s.pendingTicks > 0 {
				s.pendingTicks--
				return []collect.Result{{Key: "FCIA-7", Verdict: collect.VerdictPending}}
			}
			return nil
		},
		Work: func(o work.Options) []work.Result {
			s.worked = append(s.worked, o.Keys...)
			out := s.outcome
			if out == "" {
				out = work.OutcomeCIWait
			}
			return []work.Result{{Key: o.Keys[0], Outcome: out}}
		},
		Queued: func(string, []string, string) ([]string, error) {
			return s.queued, s.queueErr
		},
		InFlight: func(string, []string) (bool, string, error) {
			return s.busy, s.busyKey, s.busyErr
		},
		Sleep: func(time.Duration) bool {
			s.sleeps++
			return s.sleeps < s.maxSleeps
		},
	}
}

func runWatch(t *testing.T, s *spy, o Options) string {
	t.Helper()
	stopping.Store(false)
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
	s := &spy{queued: []string{"FCIA-7"}}
	runWatch(t, s, Options{Once: true})

	if s.collects != 1 {
		t.Errorf("collect ran %d times, want 1", s.collects)
	}
	if len(s.worked) != 1 || s.worked[0] != "FCIA-7" {
		t.Errorf("worked = %v, want [FCIA-7]", s.worked)
	}
}

// The claim label is the lock, and it lives on the TICKET. A watcher
// restarted mid-job, or a second one somebody started by accident, must see
// the same answer -- two agents in one repository fight over git.
func TestNothingStartsWhileAJobIsAlreadyRunning(t *testing.T) {
	s := &spy{busy: true, busyKey: "FCIA-6", queued: []string{"FCIA-7"}}
	out := runWatch(t, s, Options{Once: true})

	if len(s.worked) != 0 {
		t.Fatalf("started %v while FCIA-6 was in flight", s.worked)
	}
	if !strings.Contains(out, "FCIA-6") {
		t.Errorf("the reason nothing started must name what is running: %s", out)
	}
}

// One at a time, however long the queue. The order a person expressed by
// ranking their backlog is the whole point of a queue; running three at once
// spends more to produce a less predictable order.
func TestOnlyOneJobIsStartedPerTick(t *testing.T) {
	s := &spy{queued: []string{"FCIA-7", "FCIA-8", "FCIA-9"}}
	runWatch(t, s, Options{Once: true})

	if len(s.worked) != 1 {
		t.Fatalf("started %d jobs in one tick: %v", len(s.worked), s.worked)
	}
	if s.worked[0] != "FCIA-7" {
		t.Errorf("started %s; the queue's own order must be respected", s.worked[0])
	}
}

// The limit that makes a first unattended run safe to try.
func TestMaxJobsStopsTheLoop(t *testing.T) {
	s := &spy{queued: []string{"FCIA-7"}, maxSleeps: 99}
	out := runWatch(t, s, Options{MaxJobs: 2})

	if len(s.worked) != 2 {
		t.Fatalf("started %d jobs, want exactly 2", len(s.worked))
	}
	if !strings.Contains(out, "the limit for this run") {
		t.Errorf("stopping at the limit must be stated: %s", out)
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
	s := &spy{queued: []string{"FCIA-7"}, maxSleeps: 20, pendingTicks: 2}
	out := runWatch(t, s, Options{MaxJobs: 1})

	if len(s.worked) != 1 {
		t.Fatalf("started %d jobs, want exactly 1 -- the cap must still cap", len(s.worked))
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
	s := &spy{queued: []string{"FCIA-7"}, outcome: work.OutcomeBlocked, maxSleeps: 20}
	out := runWatch(t, s, Options{MaxJobs: 1})

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
	s := &spy{queued: []string{"FCIA-7"}, outcome: work.OutcomeSkipped, maxSleeps: 3}
	out := runWatch(t, s, Options{MaxJobs: 1})

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
	s := &spy{queued: []string{"FCIA-7"}}
	out := runWatch(t, s, Options{Once: true, DryRun: true})

	if len(s.worked) != 0 {
		t.Fatalf("dry run started %v", s.worked)
	}
	if !strings.Contains(out, "would") {
		t.Errorf("got: %s", out)
	}
}

func TestAnEmptyQueueDoesNothingQuietly(t *testing.T) {
	s := &spy{}
	out := runWatch(t, s, Options{Once: true})

	if len(s.worked) != 0 {
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
func TestStoppingWaitsForTheCurrentStep(t *testing.T) {
	stopping.Store(false)
	s := &spy{queued: []string{"FCIA-7"}, maxSleeps: 99}

	// Signal mid-job, as ctrl-c would.
	d := s.deps()
	inner := d.Work
	d.Work = func(o work.Options) []work.Result {
		stopping.Store(true)
		return inner(o)
	}

	var buf bytes.Buffer
	if err := Run(Options{Out: &buf, Home: t.TempDir(), Interval: time.Millisecond}, d); err != nil {
		t.Fatal(err)
	}

	if len(s.worked) != 1 {
		t.Fatalf("the in-flight job was abandoned: %v", s.worked)
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
	d.Queued = func(string, []string, string) ([]string, error) {
		return nil, errors.New(`not a registered project: FCRA`)
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
	s := &spy{queued: []string{"FCIA-7"}, maxSleeps: 99}
	runWatch(t, s, Options{DryRun: true})

	if s.sleeps != 0 {
		t.Errorf("a dry run looped %d times; once is the whole point", s.sleeps)
	}
}

// Rehearsing is for checking the ORDER, which a count cannot show.
func TestADryRunPrintsTheWholeQueueInOrder(t *testing.T) {
	s := &spy{queued: []string{"FCIA-7", "FCIA-8", "FCIA-10"}}
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
	got := dropClaimedChildren(issues)

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

// An orphan keeps its place. A sub-task whose parent is NOT queued is
// ordinary work somebody asked for directly -- dropping it would silently
// refuse a ticket that was labelled on purpose.
func TestASubTaskWhoseParentIsNotQueuedIsStillWorked(t *testing.T) {
	got := dropClaimedChildren([]tracker.Issue{
		{Key: "OR-51", Parent: "OR-50"}, // OR-50 is not in the list
	})
	if len(got) != 1 || got[0] != "OR-51" {
		t.Errorf("queued %v; a sub-task worked on its own must be allowed", got)
	}
}

// Jira keys are upper-case by convention and not by guarantee. A case
// mismatch that silently failed to match would let both run.
func TestParentMatchingIgnoresCase(t *testing.T) {
	got := dropClaimedChildren([]tracker.Issue{
		{Key: "OR-50"},
		{Key: "OR-51", Parent: "or-50"},
	})
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
	jql := queuedJQL([]string{"OR"}, "ORION")

	if !strings.Contains(jql, `statusCategory != "Done"`) {
		t.Errorf("a Done ticket is still claimable: %s", jql)
	}
	// The rest of the criterion must survive alongside it.
	for _, want := range []string{
		`project IN ("OR")`,
		`labels = "ORION"`,
		`labels NOT IN ("orion-working", "orion-ci-wait", "orion-failed")`,
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
	if jql := queuedJQL([]string{"OR"}, ""); !strings.Contains(jql,
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
}

func (f *fakeLock) Search(string, int) ([]tracker.Issue, error) { return f.issues, nil }

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
	busy, key, err := InFlight(j, home, nil, &b)
	if err != nil {
		t.Fatal(err)
	}
	if busy {
		t.Errorf("a ticket that is already Done was reported in flight as %q", key)
	}
	// Cleared, not merely ignored: ignoring it would re-diagnose the same
	// ticket every tick forever, and `orion queue` reads the label too.
	if got := j.removed["OR-124"]; len(got) != 1 || got[0] != tracker.LabelWorking {
		t.Errorf("the stale lock was not cleared, removed = %v", j.removed)
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

	busy, key, err := InFlight(j, home, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !busy || key != "OR-130" {
		t.Errorf("InFlight = %v, %q; a running job must hold the lock", busy, key)
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

	busy, key, err := InFlight(j, home, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !busy || key != "OR-130" {
		t.Errorf("InFlight = %v, %q; the live claim was lost behind a stale one", busy, key)
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
	busy, _, err := InFlight(j, home, nil, &b)
	if err != nil {
		t.Fatalf("a label write that failed must not fail the tick: %v", err)
	}
	if busy {
		t.Error("a finished ticket held the queue because its label could not be cleared")
	}
	if !strings.Contains(b.String(), "403 forbidden") {
		t.Errorf("the reason must be reported: %q", b.String())
	}
}
