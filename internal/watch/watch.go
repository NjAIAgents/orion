// Package watch is the loop that removes the last manual step.
//
// Everything else in Orion is already automatic between the moment a ticket
// is claimed and the moment its branch merges. What remained was TRIGGERING:
// a person typing `orion work FCIA-7`, then later `orion collect`. This turns
// that into a label on a ticket.
//
// The design in one line: a tick does the cheap reconciling first, then tops
// the running set back up to the cap, then sleeps.
//
// Cheap first, because collect costs one API call per waiting ticket and can
// finish work already paid for -- closing a merged ticket, pushing a CI fix,
// asking for an approval. Starting new work before finishing old work would
// mean paying to begin something while something else sat done-but-unclosed.
//
// SEVERAL AT ONCE, up to limits.max_concurrent_tickets. This used to be
// strictly one: collect, start one ticket, block until it finished. The
// reasoning was that two agents in one repository fight over git, and that is
// true of a shared checkout and not of per-job worktrees -- and agent
// execution was never the bottleneck anyway. On 2026-08-29 a ticket's agent
// work took 14m23s while approvals and merge serialisation cost hours, so a
// queue that ran one at a time was idle for most of the day it was watching.
//
// The cap defaults to two, not because two is timid but because everything
// concurrency breaks is invisible at one and obvious at two: git against the
// one shared clone (workspace/gitlock.go), a budget checkpoint sailed past by
// runs already in flight (budget/admit.go), one rate limit reached by N
// sessions at once (limitPause below), and N tickets picked that all edit the
// same files (pick below). Prove it at two, then raise it; five is the
// ceiling, and it is a ceiling rather than advice.
//
// What concurrency does NOT fix is the approval bottleneck: N tickets
// finishing means N approvals waiting on one human, so without auto-merge the
// queue finishes faster and lands no faster. That is worth knowing before it
// is mistaken for a regression.
//
// The state is on the tickets, not in this process. A watcher killed
// mid-flight loses nothing that matters: the labels say what was claimed,
// what is awaiting CI, and what failed, so a restart resumes rather than
// repeats. That is what makes it safe to run this on a laptop that closes.
package watch

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/work"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Options configure one watcher.
type Options struct {
	Out  io.Writer
	Home string
	// Interval between ticks. A short one costs API calls and nothing else,
	// because a tick with nothing to do does nothing.
	Interval time.Duration
	// MaxJobs caps how many tickets this watcher will START. Zero means
	// unlimited, which is what a long-lived daemon wants and what a first
	// unattended run very much does not.
	MaxJobs int
	// Once runs a single tick and exits. The form to put on a timer, where
	// the operating system owns the schedule and a crash is somebody else's
	// problem to restart.
	Once bool
	// DryRun reports what each tick would do and starts nothing.
	DryRun bool
	// Projects limits the watcher to specific project keys. Empty means
	// every registered project.
	Projects []string
	// QueueLabel is what a ticket carries to ask for work. Empty means
	// tracker.QueueLabelDefault. One label across every watched project,
	// because a cross-project query can only ask about one -- a project
	// using a different label needs its own watcher.
	QueueLabel string
	// MaxConcurrent is how many tickets may be in flight at once. Zero means
	// the shipped default (config.Limits.ConcurrentTickets), and any value is
	// clamped to the same ceiling, so a caller cannot widen the control by
	// passing a number the config file would have refused.
	MaxConcurrent int
	// WorkOpts are passed through to each job.
	MaxMinutes int
	MaxTurns   int
}

// Deps are the seams. Both of these are the real commands, injected so the
// loop itself can be tested without a tracker, a network, or an agent.
type Deps struct {
	// Collect reconciles everything already in flight.
	Collect func(opts collect.Options) []collect.Result
	// Work starts one ticket.
	Work func(opts work.Options) []work.Result
	// Queued returns the issues waiting to be started, in the order the
	// tracker ranked them.
	//
	// Issues rather than keys, because the choice of WHICH n to start
	// together needs more than a name: see pick.
	Queued func(home string, projects []string, label string) ([]tracker.Issue, error)
	// InFlight returns the tickets already claimed somewhere. The claim label
	// is the lock, so this reads the tracker rather than any state this
	// process holds -- a watcher restarted mid-job, or a second watcher
	// somebody started by accident, must not push the total past the cap.
	//
	// A COUNT rather than a yes/no, which is the whole difference between a
	// queue that runs one ticket and one that runs n.
	InFlight func(home string, projects []string) ([]string, error)
	Sleep    func(d time.Duration) bool // returns false if interrupted
	Now      func() time.Time
}

// maxLimitSleep caps how long one rate-limit reading may park the watcher.
//
// A reported reset is a claim, and OR-162 showed a claim can be wrong: a
// graded status near the weekly ceiling was read as exhaustion and the
// watcher slept until Monday with a fifth of the allowance unspent. Even
// with the classification fixed, trusting a single reading for days is a
// bet with a bad payoff -- waking early costs one refused tick, waking late
// costs every ticket the queue would have finished.
const maxLimitSleep = 30 * time.Minute

// Run watches until interrupted, or until MaxJobs tickets have been started.
func Run(opts Options, deps Deps) error {
	w := opts.Out
	if w == nil {
		w = os.Stdout
	}
	if opts.Home == "" {
		opts.Home = workspace.Home()
	}
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Minute
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Sleep == nil {
		deps.Sleep = sleepInterruptible
	}
	opts.MaxConcurrent = config.Limits{MaxConcurrentTickets: opts.MaxConcurrent}.ConcurrentTickets()

	// Every job writes its progress to the same terminal from its own
	// goroutine. Serialised so a line is whole: two agents' output interleaves
	// by line, which is legible because every line names its ticket, and does
	// not interleave mid-word, which would not be.
	w = &syncWriter{w: w}

	p := newPool(opts.MaxConcurrent)
	// Nothing exits while an agent is still running. Killing one leaves a
	// ticket claimed with a half-written branch and no process to finish it,
	// which is the state an unattended tool must never create -- and that is
	// as true of the nth concurrent job as it was of the only one (OR-141).
	defer func() {
		for _, r := range p.wait() {
			reportFinished(w, r)
		}
	}()

	started := 0
	draining := false
	// One rate-limit decision for the whole watcher, not one per run. N
	// concurrent sessions reach a ceiling faster and all N see it at the same
	// moment; without this each would independently decide to sleep, and the
	// pause would be re-derived n times from n copies of the same reading.
	var pausedUntil time.Time

	for tick := 1; ; tick++ {
		if stopping.Load() {
			break
		}

		// Reap what finished while this loop slept. Done before anything else
		// because it frees slots, and because a finished job may carry the
		// rate-limit verdict that decides whether anything starts at all.
		jobsUnfinished := false
		for _, r := range p.reap() {
			reportFinished(w, r)
			if r.Outcome != work.OutcomeSkipped {
				started++
			}
			if r.Outcome == work.OutcomeCIWait {
				jobsUnfinished = true
			}
			if until, paused := limitPause(w, r, deps.Now()); paused && until.After(pausedUntil) {
				pausedUntil = until
			}
		}

		free := p.free()
		if opts.MaxJobs > 0 {
			if room := opts.MaxJobs - started - p.len(); room < free {
				free = room
			}
		}
		if free > 0 && deps.Now().Before(pausedUntil) {
			free = 0
		}

		unfinished, err := oneTick(opts, deps, w, free, p)
		unfinished = unfinished || jobsUnfinished || p.len() > 0
		if err != nil {
			// A misconfiguration will NEVER fix itself, so retrying it every
			// two minutes forever is not resilience -- it is a watcher that
			// looks alive while watching nothing. `orion watch fcra` (one
			// letter wrong) did exactly that.
			//
			// A transient failure is different: a network blip or an expired
			// token is fixable while this keeps running, and stopping would
			// mean the fix also requires noticing the watcher died.
			if permanent(err) {
				ui.Say(w, "", events.ActorOrion, ui.VerbFail, "%v", err)
				return err
			}
			ui.Say(w, "", events.ActorOrion, ui.VerbWarn, "tick %d: %v", tick, err)
		}

		// A dry run has nothing to learn from a second tick: it changes
		// nothing, so every subsequent tick prints the identical thing
		// forever. Rehearsing once is the whole point.
		//
		// --once still means one tick, but a tick now DISPATCHES rather than
		// blocks, so the jobs it started are waited for by the deferred drain
		// rather than by the tick itself.
		if opts.Once || opts.DryRun {
			break
		}
		// The job limit caps how many tickets are STARTED. It must not end the
		// watcher while one of those tickets is still in flight.
		//
		// It used to. `--max-jobs 1` started FCIA-7, pushed it, opened the
		// pull request, moved it to orion-ci-wait -- and exited on the same
		// tick, before any tick could reconcile it. CI went green into an
		// empty room: no approval was ever requested, nothing merged, no
		// worktree was pruned, and the ticket sat in ci-wait indefinitely
		// waiting for a watcher that had already stopped. The limit was
		// abandoning the very job it had just paid for.
		//
		// So the cap now stops STARTING and keeps DRAINING: reconcile only,
		// until the tickets this run began have merged, failed, or been
		// closed. Ctrl-c still exits at the end of the current step.
		if opts.MaxJobs > 0 && started >= opts.MaxJobs {
			if !unfinished {
				ui.Say(w, "", events.ActorOrion, ui.VerbOK,
					"started %d job(s) and finished them; the limit for this run", started)
				break
			}
			if !draining {
				draining = true
				ui.Say(w, "", events.ActorOrion, ui.VerbWaiting,
					"draining: started %d job(s), the limit for this run; "+
						"starting nothing more, but staying up until they finish", started)
			}
		}
		if !deps.Sleep(opts.Interval) {
			break
		}
	}

	if stopping.Load() {
		fmt.Fprintln(w, "\nstopped. Nothing was left half-done: a ticket's labels say\n"+
			"where it got to, so the next watcher picks it up from there.")
	}
	return nil
}

// permanent reports whether an error will still be true in two minutes.
//
// Conservative: only errors this code RAISES about configuration count.
// Treating an unfamiliar error as permanent would turn a transient blip
// into a stopped watcher, which is the failure this whole loop is built to
// survive.
func permanent(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not a registered project")
}

// oneTick does the reconciling, then tops the running set up by at most `free`
// jobs. Returns whether anything collect knows about is still unfinished.
//
// It DISPATCHES rather than runs: the jobs it starts outlive the tick, and the
// loop reaps them on a later one. That is the whole difference between a queue
// that works one ticket and one that works n -- a tick that blocked on the
// agent could never start a second.
//
// The unfinished flag is what lets the loop know it must not exit yet. A
// ticket that has been pushed and is awaiting CI is Orion's responsibility
// until it merges or fails, and nothing else in the system will pick it up.
func oneTick(opts Options, deps Deps, w io.Writer, free int, p *pool) (unfinished bool, err error) {
	// 1. Finish what is already in flight. Cheap, and it can free the job
	// slot this tick is about to look for.
	//
	// This now runs ALONGSIDE the agents rather than only between them, which
	// is why the git it performs -- rebase, worktree removal, branch pruning --
	// takes the shared-clone lock (workspace/gitlock.go).
	if deps.Collect != nil {
		for _, r := range deps.Collect(collect.Options{
			Out: w, Home: opts.Home, DryRun: opts.DryRun,
			// A tick is not a person at a terminal. Without this the
			// collector told the watcher's operator to run the very command
			// the watcher is already running.
			Unattended: true,
		}) {
			// Pending means CI is still running; passing means CI is green
			// and a human has not approved yet. Both are work this watcher
			// still owes, and exiting on either strands the ticket.
			if r.Verdict == collect.VerdictPending || r.Verdict == collect.VerdictPassing {
				unfinished = true
			}
		}
	}

	// 2. How much is already claimed? The label is the lock, and it lives on
	// the ticket rather than in this process, so a restarted watcher -- or a
	// second one somebody started by accident -- sees the same answer.
	//
	// Counted, not merely detected: the cap is on the TOTAL number of agents
	// against a project, and this watcher's own goroutines are only part of
	// it. A claim held by a job this process dispatched is the same claim, so
	// the two are combined rather than added.
	if deps.InFlight != nil {
		keys, err := deps.InFlight(opts.Home, opts.Projects)
		if err != nil {
			return unfinished, err
		}
		if elsewhere := claimedElsewhere(keys, p.keys()); len(elsewhere) > 0 {
			free -= len(elsewhere)
			if free <= 0 && !opts.DryRun {
				ui.Say(w, elsewhere[0], events.ActorOrion, ui.VerbWorking,
					"still running; not starting anything else (%d claimed, cap %d)",
					len(elsewhere)+p.len(), opts.MaxConcurrent)
			}
		}
	}

	if free <= 0 && !opts.DryRun {
		return unfinished, nil
	}

	// 3. Start the next tickets.
	queued, err := deps.Queued(opts.Home, opts.Projects, opts.QueueLabel)
	if err != nil {
		return unfinished, err
	}
	if len(queued) == 0 {
		return unfinished, nil
	}

	if opts.DryRun {
		rehearse(w, opts, queued)
		return unfinished, nil
	}

	next := pick(queued, free)
	for _, key := range next {
		ui.Say(w, key, events.ActorOrion, ui.VerbWorking, "claimed (%d queued)", len(queued))
		p.dispatch(deps, opts, w, key)
	}
	return unfinished, nil
}

// reportFinished says how a dispatched job ended, for the endings that are not
// already reported by the job itself.
func reportFinished(w io.Writer, r work.Result) {
	// A job that was refused before spending anything -- budget, a dirty
	// sandbox -- must not count against the run's job limit, and must not be
	// retried immediately: the condition that refused it is still true, and a
	// tight loop would hammer the tracker to no purpose. The loop enforces
	// both by not counting it and by only dispatching once per tick.
	if r.Outcome == work.OutcomeSkipped {
		ui.Say(w, r.Key, events.ActorOrion, ui.VerbWarn,
			"not started; waiting rather than retrying immediately")
	}
}

// limitPause turns one run's rate-limit verdict into a time the watcher will
// not dispatch before.
//
// Decided centrally rather than per run, which is the change concurrency
// forces. OR-162's logic was written for a single stream: the run reported a
// limit and the watcher slept. With n sessions all n hit the ceiling within
// seconds of each other, and n independent sleeps would each re-derive the
// same pause from the same reading -- and, worse, sleeping inside a job's own
// path would park the loop that still has to reconcile the OTHER n-1 tickets.
//
// So the job reports, the loop decides, and the pause suppresses dispatch
// while everything already running is left alone to finish.
func limitPause(w io.Writer, r work.Result, now time.Time) (time.Time, bool) {
	if r.Limit.OK() {
		return time.Time{}, false
	}
	ui.Say(w, r.Key, events.ActorOrion, ui.VerbWarn, "%s", r.Limit.Describe(now))

	// Wait answers with the SOONEST blocking window, not whichever event
	// arrived last, so a two-hour five-hour pause no longer waits until the
	// weekly reset (OR-162).
	d := r.Limit.Wait(now)
	if d <= 0 {
		return time.Time{}, false
	}
	// Capped, because pausing for days on one reading is how a misreported
	// limit becomes a lost weekend. Waking early costs one refused tick;
	// waking late costs everything the queue would have done.
	if d > maxLimitSleep {
		ui.Say(w, r.Key, events.ActorOrion, ui.VerbWaiting,
			"the reported reset is %s away; re-checking in %s instead",
			d.Round(time.Minute), maxLimitSleep)
		d = maxLimitSleep
	} else {
		ui.Say(w, r.Key, events.ActorOrion, ui.VerbWaiting,
			"starting nothing new until %s", now.Add(d).Local().Format("15:04 Mon"))
	}
	return now.Add(d), true
}

// rehearse prints what a dry run would do.
func rehearse(w io.Writer, opts Options, queued []tracker.Issue) {
	// Show the WHOLE queue in the order it would be worked, not just the
	// head. "would start FCIA-7 (3 queued)" says a number; the point of
	// rehearsing is to see whether the ORDER is the one you meant, and that
	// cannot be checked against a count.
	first := pick(queued, opts.MaxConcurrent)
	ui.Say(w, strings.Join(first, ", "), events.ActorOrion, ui.VerbWaiting,
		"would start them together, then work down this queue:")
	starting := map[string]bool{}
	for _, k := range first {
		starting[k] = true
	}
	for i, is := range queued {
		marker := "  "
		if starting[is.Key] {
			marker = "->"
		}
		fmt.Fprintf(w, "          %s %d. %s\n", marker, i+1, is.Key)
	}
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
		"limits.max_concurrent_tickets %d: that many run at once.", opts.MaxConcurrent)))
	if opts.MaxJobs > 0 {
		n := opts.MaxJobs
		if n > len(queued) {
			n = len(queued)
		}
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			fmt.Sprintf("--max-jobs %d: it would start the first %d and stop.",
				opts.MaxJobs, n)))
	} else {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			fmt.Sprintf("no job limit: it would work all %d and keep watching.", len(queued))))
	}
}

// pick chooses which n of the queued tickets to start together.
//
// Concurrency does not cause merge conflicts; picking n tickets that all edit
// the same files does. On 2026-08-29 five queued tickets all touched the fix
// loop, the activity logger and the notify path, and taking the top five by
// priority produced a hand-resolved three-way conflict.
//
// Orion cannot know a ticket's file set before it is worked, so it spreads
// across the coarsest thing it CAN see: the ticket's area -- its first Jira
// component, or failing that its project. Tickets from different areas are
// less likely to touch the same code than the next two on one component's
// backlog, which is a weaker claim than "disjoint" and the honest one.
//
// A reordering, not a filter. Once every area is represented the rest are
// taken in the tracker's own priority order, so nothing is refused and the
// backlog ranking still decides what gets worked -- it only stops deciding
// what gets worked SIMULTANEOUSLY. With n == 1 this is exactly the old
// behaviour: the head of the queue, no reordering at all.
func pick(queued []tracker.Issue, n int) []string {
	if n <= 0 || len(queued) == 0 {
		return nil
	}
	var out []string
	taken := make([]bool, len(queued))
	seen := map[string]bool{}
	for i, is := range queued {
		if len(out) == n {
			return out
		}
		a := area(is)
		if seen[a] {
			continue
		}
		seen[a] = true
		taken[i] = true
		out = append(out, is.Key)
	}
	// Fewer areas than slots: the spread cannot be known any better than this,
	// so fall back to priority order rather than leaving a slot idle.
	for i, is := range queued {
		if len(out) == n {
			break
		}
		if !taken[i] {
			out = append(out, is.Key)
		}
	}
	return out
}

// area is the coarsest grouping Orion has for "these probably touch the same
// code": Jira's own component, else the project the key belongs to.
func area(is tracker.Issue) string {
	for _, c := range is.Components {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			return "component:" + c
		}
	}
	return "project:" + registry.ProjectOf(is.Key)
}

// claimedElsewhere returns the claims held by somebody other than this
// process.
//
// A ticket this watcher dispatched holds the orion-working label itself, so
// counting the tracker's answer and this process's goroutines separately would
// double-count every running job and halve the effective cap.
func claimedElsewhere(claimed, mine []string) []string {
	own := make(map[string]bool, len(mine))
	for _, k := range mine {
		own[strings.ToUpper(strings.TrimSpace(k))] = true
	}
	var out []string
	for _, k := range claimed {
		if !own[strings.ToUpper(strings.TrimSpace(k))] {
			out = append(out, k)
		}
	}
	return out
}

// pool is the set of tickets this watcher has dispatched and not yet reaped.
type pool struct {
	cap  int
	mu   sync.Mutex
	live map[string]bool
	done chan work.Result
	wg   sync.WaitGroup
}

func newPool(n int) *pool {
	return &pool{cap: n, live: map[string]bool{}, done: make(chan work.Result, n*4)}
}

func (p *pool) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.live)
}

func (p *pool) free() int { return p.cap - p.len() }

func (p *pool) keys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.live))
	for k := range p.live {
		out = append(out, k)
	}
	return out
}

// dispatch starts one ticket in its own goroutine.
//
// One key per call rather than one call with n keys: work.Run stops the whole
// batch when a ticket fails, which is right for a batch somebody typed and
// wrong for a queue -- an unrelated ticket must not be cancelled because
// another one broke.
func (p *pool) dispatch(deps Deps, opts Options, w io.Writer, key string) {
	p.mu.Lock()
	p.live[key] = true
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		res := deps.Work(work.Options{
			Keys: []string{key}, Out: w, Home: opts.Home,
			MaxMinutes: opts.MaxMinutes, MaxTurns: opts.MaxTurns,
		})
		if len(res) == 0 {
			res = []work.Result{{Key: key}}
		}
		p.mu.Lock()
		delete(p.live, key)
		p.mu.Unlock()
		for _, r := range res {
			p.done <- r
		}
	}()
}

// reap collects everything that has finished, without blocking on anything
// that has not.
func (p *pool) reap() []work.Result {
	var out []work.Result
	for {
		select {
		case r := <-p.done:
			out = append(out, r)
		default:
			return out
		}
	}
}

// wait blocks until every dispatched job has finished, and returns their
// results. This is what makes ctrl-c safe for n jobs: the loop stops
// dispatching immediately and the process still outlives every agent it
// started (OR-141).
func (p *pool) wait() []work.Result {
	p.wg.Wait()
	return p.reap()
}

// syncWriter serialises writes from concurrent jobs.
//
// Without it two agents' progress lines interleave mid-word on the terminal,
// and -- the reason this is not merely cosmetic -- concurrent writes to the
// same io.Writer are a data race for any writer that is not itself safe, which
// includes the bytes.Buffer every test uses.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(b)
}

// Queued lists tickets carrying the queue label, in the tracker's order.
//
// Scoped to registered projects. An unscoped query would match a label
// somebody applied by hand in an unrelated project, and this is the function
// that decides what an agent is turned loose on.
func Queued(j *tracker.Jira, home string, projects []string, label string) ([]tracker.Issue, error) {
	keys, err := scope(home, projects)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	issues, err := j.Search(queuedJQL(keys, label), 25)
	if err != nil {
		return nil, err
	}
	return dropClaimedChildren(issues), nil
}

// queuedJQL is the claim criterion: what a watcher will turn an agent loose
// on. Split from Queued so it can be read in a test -- Queued itself needs a
// live Jira, and this is the part that decides what gets worked.
func queuedJQL(keys []string, label string) string {
	if label == "" {
		label = tracker.QueueLabelDefault
	}
	// Excluding the other managed labels is what stops a ticket being picked
	// up twice. A ticket keeps its queue label while it is worked, so
	// matching on that alone would re-claim something already in flight the
	// moment the in-flight check raced or a second watcher existed.
	//
	// And excluding resolved tickets, because a label outlives the work it
	// asked for. OR-119 was fixed by hand, merged and moved to Done with its
	// ORION label still on it; the next tick claimed it as the head of the
	// queue and spent an agent re-investigating a bug that was already fixed
	// on the trunk. Nothing here filtered on status, and the merged-branch
	// guard could not help: a hand fix lands on a branch Orion never named.
	return tracker.JQLAnd(
		tracker.JQLIn("project", keys...),
		tracker.JQLEq("labels", label),
		tracker.JQLNotIn("labels", tracker.LabelWorking, tracker.LabelCIWait, tracker.LabelFailed),
		tracker.JQLNotDone(),
	) + " ORDER BY priority DESC, Rank ASC"
}

// dropClaimedChildren removes any issue whose PARENT is also in the list.
//
// A parent is worked together with its sub-tasks -- one branch, one pull
// request, one approval -- so a sub-task that is ALSO labelled would be
// claimed a second time as a job of its own. Two agents on the same work, on
// separate branches, guaranteed to conflict: they were decomposed from one
// story precisely BECAUSE they touch the same code.
//
// Dropped rather than refused. Labelling both a story and its tasks is a
// reasonable thing for a person to do, not a mistake to scold them for --
// they are saying "do this work", and the parent already says it.
//
// Split out from Queued so the judgement is reachable from a test: Queued
// itself needs a live Jira, and this is the part that can be wrong.
func dropClaimedChildren(issues []tracker.Issue) []tracker.Issue {
	queued := make(map[string]bool, len(issues))
	for _, i := range issues {
		queued[strings.ToUpper(strings.TrimSpace(i.Key))] = true
	}
	var out []tracker.Issue
	for _, i := range issues {
		if p := strings.ToUpper(strings.TrimSpace(i.Parent)); p != "" && queued[p] {
			continue
		}
		out = append(out, i)
	}
	return out
}

// LockAPI is the slice of the tracker the claim lock needs. An interface
// rather than *tracker.Jira so the stale-lock path can be tested without a
// server -- it is the path that decides whether the queue moves at all.
type LockAPI interface {
	Search(jql string, maxResults int) ([]tracker.Issue, error)
	SetLabels(key string, add, remove []string) error
}

// InFlight returns the tickets currently claimed, in the tracker's order.
//
// A LIST rather than a yes/no. With one job at a time "is anything running"
// was the whole question; with a cap of n the answer has to be a number, or
// the second slot can never be filled.
//
// The claim label deliberately outlives the process that set it: that is
// what makes a restarted watcher resume rather than double-start. It also
// outlives the WORK, though, and nothing but the watch-driven close path
// ever cleared it. A ticket fixed and transitioned to Done by hand kept
// orion-working forever, and every later tick reported a ticket that
// finished hours ago as "still running; not starting anything else" --
// indistinguishable from a genuinely stuck job without opening Jira
// (OR-125).
//
// So a resolved ticket is not in flight, whatever its labels say. The lock
// is stripped here rather than merely ignored, because ignoring it would
// re-diagnose the same ticket on every tick forever, and because the label
// is read by `orion queue` too.
func InFlight(j LockAPI, home string, projects []string, w io.Writer) ([]string, error) {
	keys, err := scope(home, projects)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	jql := tracker.JQLAnd(
		tracker.JQLIn("project", keys...),
		tracker.JQLEq("labels", tracker.LabelWorking),
	)
	// Comfortably above the concurrency ceiling. Asking for exactly the cap
	// would let a handful of stale claims fill the answer and hide a live one
	// behind them -- and the stale ones are cleared below, so a short page
	// would also mean they were never cleared.
	issues, err := j.Search(jql, config.MaxConcurrentTicketsCeiling+5)
	if err != nil {
		return nil, err
	}
	var running []string
	for _, i := range issues {
		if !i.Resolved() {
			running = append(running, i.Key)
			continue
		}
		// Best effort. A tracker that refuses the write leaves the queue
		// exactly as wedged as before, which is worth a line but not worth
		// failing the tick over.
		if err := j.SetLabels(i.Key, nil, []string{tracker.LabelWorking}); err != nil {
			ui.Say(w, i.Key, events.ActorOrion, ui.VerbWarn,
				"is %s but still holds the %s lock, and it could not be cleared: %v",
				i.Status, tracker.LabelWorking, err)
			continue
		}
		ui.Say(w, i.Key, events.ActorOrion, ui.VerbWarn,
			"was closed outside Orion while still holding the %s lock; cleared it",
			tracker.LabelWorking)
	}
	return running, nil
}

// Concurrency resolves the cap this watcher will run at, and says where the
// number came from so the banner can state it before anything is spent.
//
// The SMALLEST value among the watched projects wins. A watcher spans several
// projects and the cap is one number, so it has to be reconciled somehow, and
// the only safe direction is down: config.go's rule is that an absent or
// malformed config must never silently widen a control, and taking the largest
// -- or the first one read -- would let one repository's setting raise the
// concurrency of another's.
//
// A project whose config cannot be read contributes the shipped default rather
// than nothing, for the same reason.
func Concurrency(home string, projects []string) (int, string) {
	keys, err := scope(home, projects)
	if err != nil || len(keys) == 0 {
		return config.Limits{}.ConcurrentTickets(), "default; no registered project to read it from"
	}
	f, err := registry.Load(home)
	if err != nil {
		return config.Limits{}.ConcurrentTickets(), "default; the registry could not be read"
	}
	n, from := 0, ""
	for _, k := range keys {
		e, ok := f.Repos[strings.ToUpper(k)]
		if !ok {
			continue
		}
		c := config.Load(e.Source).Limits.ConcurrentTickets()
		if n == 0 || c < n {
			n, from = c, k
		}
	}
	if n == 0 {
		return config.Limits{}.ConcurrentTickets(), "default; no project config could be read"
	}
	if len(keys) > 1 {
		return n, fmt.Sprintf("%s's limits.max_concurrent_tickets, the smallest of %d projects", from, len(keys))
	}
	return n, from + "'s limits.max_concurrent_tickets"
}

func scope(home string, projects []string) ([]string, error) {
	f, err := registry.Load(home)
	if err != nil {
		return nil, err
	}
	if len(projects) > 0 {
		// Check every name against the registry rather than trusting it.
		//
		// `orion watch fcra` -- one letter wrong -- searched a project that
		// does not exist, found nothing, and reported "nothing is waiting"
		// exactly as it would for a correct key with an empty queue. A typo
		// that looks like success is worse than one that fails, because the
		// watcher then sits there all night watching nothing.
		known := map[string]bool{}
		for _, k := range f.Keys() {
			known[strings.ToUpper(k)] = true
		}
		var out, unknown []string
		for _, p := range projects {
			u := strings.ToUpper(strings.TrimSpace(p))
			if u == "" {
				continue
			}
			if !known[u] {
				unknown = append(unknown, u)
				continue
			}
			out = append(out, u)
		}
		if len(unknown) > 0 {
			return nil, fmt.Errorf("not a registered project: %s\n"+
				"  Registered: %s\n"+
				"  Bind one with: orion init (inside its repository)",
				strings.Join(unknown, ", "), strings.Join(f.Keys(), ", "))
		}
		return out, nil
	}
	return f.Keys(), nil
}

// stopping is set by the signal handler and read between steps.
//
// A flag rather than a hard exit: ctrl-c must not kill an agent mid-run.
// Killing one leaves a ticket claimed with a half-written branch and no
// process to finish it, which is the state that needs a person to untangle
// it -- exactly what an unattended tool must not create. So the signal
// arrives, the current job finishes, and the loop stops before the next one.
var stopping atomic.Bool

// Listen installs the signal handler. Separate from Run so a caller can
// install it before any long-running work begins.
func Listen(w io.Writer) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		stopping.Store(true)
		fmt.Fprintln(w, "\nstopping after the current step. Press ctrl-c again to force,\n"+
			"which risks leaving a ticket claimed with nothing running.")
		// A second signal restores the default behaviour, so a genuinely
		// stuck process can still be killed without finding its pid.
		signal.Stop(sig)
	}()
}

func sleepInterruptible(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			return true
		case <-time.After(200 * time.Millisecond):
			if stopping.Load() {
				return false
			}
		}
	}
}
