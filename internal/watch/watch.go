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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/cost"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/supervisor"
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
	// tracker ranked them, AND the labelled ones the queue is holding back.
	//
	// Issues rather than keys, because the choice of WHICH n to start
	// together needs more than a name: see pick. Held travels with them
	// because the tick that decides to start nothing is exactly the tick
	// that has to say why (OR-221).
	Queued func(home string, projects []string, label string) (Queue, error)
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

// claimsPage is how many claimed tickets one sweep asks for. Jira caps a page
// at 100, so this asks for the most it can get in one call rather than a
// number that has to be revisited whenever concurrency changes.
const claimsPage = 100

// DefaultInterval is the gap between ticks when none is given.
//
// The tick is the latency floor on every transition Orion notices rather
// than causes -- a green CI run, a merged PR, an approval reaction, a newly
// queued ticket -- so the wait is on average half of it for no reason. A
// tick is a tracker query and a PR status check: it starts no agent and
// spends no tokens, because work begins only when a ticket is claimed and
// claiming is gated by MaxConcurrent, not by how often we look. So shortening
// this doubles a cheap poll and changes nothing about spend (OR-218).
//
// Exported because the watch command prints the effective interval in its
// startup banner (OR-128) and has to name the same number the loop uses.
const DefaultInterval = time.Minute

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
		opts.Interval = DefaultInterval
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

	// The live run region (OR-240). It wraps everything else, so EVERY line
	// this watcher prints -- a stage boundary, an escalation, an agent's own
	// prose -- passes through it and the region is erased and redrawn around
	// it. A writer that bypassed this would be overdrawn by the next redraw
	// and lost.
	//
	// Off a terminal it is a pass-through: no cursor control at all, one
	// plain line per run per tick, because a redirected log has to stay a log.
	live := ui.NewLive(w)
	w = live
	// Registered FIRST so it runs LAST: the drain below and the final "stopped"
	// line are printed into the scrollback while the region is still live, and
	// clearing it before them would leave the region's last frame stranded
	// above their output.
	defer live.Close()
	// Published so the signal handler can reach the same writer. Its "press
	// ctrl-c again to force" line is the one message that must not be
	// overdrawn by a redraw, and it is written from a goroutine Run does not
	// own -- the same shape of problem `running` below solves for the pool.
	lw := io.Writer(live)
	liveOut.Store(&lw)
	defer liveOut.Store(nil)
	ui.LiveReset()
	ui.LiveMedians(medianFor(opts.Home, opts.Projects))

	// The count for a run of identical lines is printed when the run ends;
	// the watcher's last one ends when the watcher does (OR-217).
	defer ui.Flush(w)

	p := newPool(opts.MaxConcurrent)
	// Published for the signal handler, which has to name what it abandons
	// if it is forced to quit (OR-195). Cleared on the way out so a later
	// run does not report a pool that finished.
	running.Store(p)
	defer running.Store(nil)
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
		loggedOut := ""
		for _, r := range p.reap() {
			reportFinished(w, r)
			// Remembered, not acted on here: the stop belongs after the whole
			// batch has been reaped, or a second job finishing in the same tick
			// would go unreported.
			//
			// Deliberately NOT excluded from the job count below. An earlier
			// version skipped it, reasoning that a run which spent nothing must
			// not be charged against --max-jobs -- true, and unobservable: the
			// loop breaks unconditionally a few lines down, so `started` is
			// never read again. Untestable code asserting a behaviour is worse
			// than no code, so the claim is gone rather than left to rot.
			if r.Outcome == work.OutcomeNoAuth {
				loggedOut = r.Note
			}
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

		// A missing login stops the watcher, rather than being waited out like a
		// quota wall. There is nothing to wait FOR: every subsequent ticket
		// fails identically until a human signs in, and continuing to claim
		// them converts one fixable problem into a queue of released tickets
		// and a channel full of the same message (OR-212).
		//
		// Between reaping and dispatching, so the deferred drain still waits for
		// the jobs already running -- they hold claims, and this must not be the
		// one exit that abandons them.
		if loggedOut != "" {
			ui.Say(w, "", events.ActorOrion, ui.VerbFail, "%s", loggedOut)
			ui.Say(w, "", events.ActorOrion, ui.VerbWaiting,
				"stopping: every queued ticket would fail the same way. "+
					"Nothing was spent and nothing is labelled failed.")
			break
		}

		// Read the pool ONCE. free and here are two halves of one sentence --
		// "cap 2, 1 running here, 1 free" -- and reading the pool twice lets a
		// job finish between the two, so the terms stop adding up. An
		// unexplained gap in that line is the very thing OR-196 is about.
		s := slots{cap: opts.MaxConcurrent, here: p.len()}
		s.free = s.cap - s.here
		if opts.MaxJobs > 0 {
			if room := opts.MaxJobs - started - s.here; room < s.free {
				s.free = room
			}
		}
		if s.free > 0 && deps.Now().Before(pausedUntil) {
			s.free = 0
		}

		unfinished, err := oneTick(opts, deps, w, s, p)
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

		// The off-terminal heartbeat: one plain line per run. On a terminal
		// this does nothing, because the region is already saying it four
		// times a second; in a redirected log it is the whole display. With
		// nothing running it prints nothing, which is the point -- a tick
		// with nothing to say must say nothing (OR-240).
		live.Tick()

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
func oneTick(opts Options, deps Deps, w io.Writer, s slots, p *pool) (unfinished bool, err error) {
	// 1. Finish what is already in flight. Cheap, and it can free the job
	// slot this tick is about to look for.
	//
	// This now runs ALONGSIDE the agents rather than only between them, which
	// is why the git it performs -- rebase, worktree removal, branch pruning --
	// takes the shared-clone lock (workspace/gitlock.go).
	if deps.Collect != nil {
		inCI := 0
		for _, r := range deps.Collect(collect.Options{
			Out: w, Home: opts.Home, DryRun: opts.DryRun,
			// A tick is not a person at a terminal. Without this the
			// collector told the watcher's operator to run the very command
			// the watcher is already running -- and, since OR-240, printed
			// "nothing is waiting on CI" once a minute all night.
			Unattended: true,
		}) {
			// Pending means CI is still running; passing means CI is green
			// and a human has not approved yet. Both are work this watcher
			// still owes, and exiting on either strands the ticket.
			if r.Verdict == collect.VerdictPending || r.Verdict == collect.VerdictPassing {
				unfinished = true
			}
			// Only PENDING counts as "in CI" for the live header. A passing
			// pull request is waiting on a person, and counting it as CI
			// would tell the operator a machine is working when they are the
			// one holding it up.
			if r.Verdict == collect.VerdictPending {
				inCI++
			}
		}
		ui.LiveCI(inCI)
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
		s.elsewhere = claimedElsewhere(keys, p.keys())
		s.free -= len(s.elsewhere)
	}

	if s.free <= 0 && !opts.DryRun {
		// Worth a line only when a claim held elsewhere is WHY. A rate-limit
		// pause and the job limit both announce themselves already, and a tick
		// with nothing to do has to stay silent or a watcher left running
		// overnight buries the one line that matters.
		if len(s.elsewhere) > 0 {
			ui.Say(w, s.elsewhere[0], events.ActorOrion, ui.VerbWorking,
				"%s; starting nothing else", s)
			ui.Say(w, s.elsewhere[0], events.ActorOrion, ui.VerbWarn, residueHint)
		}
		return unfinished, nil
	}

	// 3. Start the next tickets.
	q, err := deps.Queued(opts.Home, opts.Projects, opts.QueueLabel)
	if err != nil {
		return unfinished, err
	}
	// Before the empty check, because "nothing is queued" and "everything
	// queued is unschedulable" are the two states this most has to tell
	// apart, and the second one prints nothing at all without this.
	reportHeld(w, q.Held)
	queued := q.Ready
	if len(queued) == 0 {
		return unfinished, nil
	}

	if opts.DryRun {
		rehearse(w, opts, queued)
		return unfinished, nil
	}

	next := pick(queued, s.free)
	// Said on EVERY dispatch, not only when a slot was lost. The operator can
	// see the cap in the banner and can see what started, and until OR-196
	// nothing joined the two: "cap 2, 1 free" because a claim is held
	// elsewhere, "2 free, starting 1" because only one ticket is queued, and
	// "2 free, starting 2" all looked identical from outside, so a run at half
	// capacity read as parallelism being broken.
	ui.Say(w, "", events.ActorOrion, ui.VerbWorking,
		"%s; starting %d of %d queued", s, len(next), len(queued))
	if len(s.elsewhere) > 0 {
		ui.Say(w, s.elsewhere[0], events.ActorOrion, ui.VerbWarn, residueHint)
	}
	for _, key := range next {
		ui.Say(w, key, events.ActorOrion, ui.VerbWorking, "claimed")
		p.dispatch(deps, opts, w, key)
	}
	return unfinished, nil
}

// reportHeld names the labelled tickets the queue refused, and why.
//
// ONE LINE PER REASON, listing the keys, rather than one line per ticket.
// The console collapses a run of identical lines into a count (OR-217), and
// that only works while the line does not change: two held tickets printing
// alternate lines would defeat it and put two lines on screen every tick all
// night. Grouped, the whole thing is one line that repeats identically and
// therefore collapses -- and the reason sentence deliberately carries no
// version names so it stays groupable.
//
// Keyed to no ticket, because it is about several.
func reportHeld(w io.Writer, held []HeldTicket) {
	if len(held) == 0 {
		return
	}
	// Insertion order preserved: it is the tracker's own ranking, and
	// re-sorting would make the line jump around between ticks for no reason
	// a reader could see.
	var reasons []string
	keys := map[string][]string{}
	for _, h := range held {
		if _, seen := keys[h.Reason]; !seen {
			reasons = append(reasons, h.Reason)
		}
		keys[h.Reason] = append(keys[h.Reason], h.Key)
	}
	for _, r := range reasons {
		ui.Say(w, "", events.ActorOrion, ui.VerbWarn, "%s: %s", strings.Join(keys[r], ", "), r)
	}
}

// slots is one tick's slot arithmetic: the cap, everything that took a slot,
// and what is left for this tick to start.
type slots struct {
	cap int
	// here is what this watcher itself dispatched and has not yet reaped.
	here int
	// elsewhere is the claims held by another watcher -- or by nobody, when
	// the label outlived the work that set it.
	elsewhere []string
	free      int
}

// String names every term that moved the number, and names the HOLDERS with
// it.
//
// The arithmetic was already right; only the reporting was missing. A claim
// held elsewhere is subtracted because the label is the lock and it lives on
// the ticket, so a second watcher or a restarted one reaches the same answer
// -- that is the design working. What was missing is a line the operator can
// check the banner against: "1 claimed elsewhere" is a fact, "OR-192" is
// something a person can go and look at.
func (s slots) String() string {
	free := s.free
	if free < 0 {
		free = 0
	}
	terms := []string{fmt.Sprintf("cap %d", s.cap)}
	if s.here > 0 {
		terms = append(terms, fmt.Sprintf("%d running here", s.here))
	}
	if len(s.elsewhere) > 0 {
		terms = append(terms, fmt.Sprintf("%d claimed elsewhere (%s)",
			len(s.elsewhere), strings.Join(s.elsewhere, ", ")))
	}
	// Whatever --max-jobs or a rate-limit pause took, so the terms add up. A
	// gap with no name is the same defect in a different place.
	if gap := s.cap - s.here - len(s.elsewhere) - free; gap > 0 {
		terms = append(terms, fmt.Sprintf("%d held back by this run's limits", gap))
	}
	return strings.Join(terms, ", ") + fmt.Sprintf(", %d free", free)
}

// residueHint is what to do about a claim that is not work.
//
// The label is the lock, and the operator cannot see it from the terminal, so
// naming the condition without naming the fix leaves them to work out that the
// answer is removing a label they were never shown. OR-192 was completed by
// hand, so it never took Orion's own close path and never had its claim
// cleared; anything touched outside the normal flow can leave one, which means
// this recurs rather than being cleaned up once.
//
// Offered rather than asserted: a claim held elsewhere is just as likely to be
// a second watcher doing real work, and this cannot tell the two apart. The
// staleness check in InFlight can, and clears it -- but only where the tracker
// categorises the status as Done, which the OR project's own workflow
// currently does not.
const residueHint = "if one of those has actually finished, its " +
	tracker.LabelWorking + " label is residue: remove it to release the slot"

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

// medianFor answers "how long does a run by this actor usually take here",
// for the live region's progress bar.
//
// Read on demand rather than cached at startup. The lookup fires when a run
// changes actor -- a handful of times per ticket -- so the file read is rare,
// and a watcher started against a brand-new project would otherwise show no
// bar for the rest of the day however many runs it completed.
//
// An unreadable history is not an error here. It means no median, the row
// draws no bar, and the region says nothing it cannot support: the display is
// an accessory to the run and must never be able to fail it.
func medianFor(home string, projects []string) func(string) time.Duration {
	return func(actor string) time.Duration {
		rows, err := cost.ReadHistory(home)
		if err != nil {
			return 0
		}
		d, ok := cost.MedianSeconds(rows, projects, actor)
		if !ok {
			return 0
		}
		return d
	}
}

// liveOut publishes the writer that owns the terminal, so the signal handler
// prints THROUGH the live region rather than underneath it.
//
// Package-level for the same reason `running` is: Listen installs the handler
// before Run builds anything, and the handler's first message -- how to force
// a quit -- is the one line that must not be erased by the next redraw.
var liveOut atomic.Pointer[io.Writer]

// out is where a message from outside the loop should go: the live writer if
// a watcher is running, else whatever the caller was given.
func out(fallback io.Writer) io.Writer {
	if p := liveOut.Load(); p != nil && *p != nil {
		return *p
	}
	return fallback
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
	// The row appears at DISPATCH, not at the first stage boundary. A ticket
	// that is claimed and then spends forty seconds provisioning a worktree is
	// exactly the window the live region exists to fill (OR-240).
	ui.LiveStart(key)

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
		ui.LiveEnd(key)
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

// Queue is one tick's view of the labelled work: what may be started, and
// what is being kept back.
//
// Held is carried alongside Ready rather than dropped because a ticket that
// silently never runs is indistinguishable from a broken watcher. The queue
// gate is allowed to refuse work; it is not allowed to refuse it quietly.
type Queue struct {
	Ready []tracker.Issue
	Held  []HeldTicket
}

// HeldTicket is one labelled ticket the queue will not claim, and the reason
// in the words an operator will see.
type HeldTicket struct {
	Key    string
	Reason string
}

// Queued lists tickets carrying the queue label, in the tracker's order.
//
// Scoped to registered projects. An unscoped query would match a label
// somebody applied by hand in an unrelated project, and this is the function
// that decides what an agent is turned loose on.
func Queued(j *tracker.Jira, home string, projects []string, label string) (Queue, error) {
	keys, err := scope(home, projects)
	if err != nil || len(keys) == 0 {
		return Queue{}, err
	}
	// The open milestones per project, read before the query rather than
	// applied to its results: see tracker/schedule.go. An error here stops
	// the tick, which the loop retries -- not knowing whether a ticket is
	// scheduled must not resolve to "claim it".
	sched, err := tracker.LoadSchedules(j, keys)
	if err != nil {
		return Queue{}, err
	}
	issues, err := j.Search(queuedJQL(keys, label, sched), 25)
	if err != nil {
		return Queue{}, err
	}
	q := Queue{Ready: dropClaimedChildren(issues)}

	// The second query runs only where the gate is actually enforced, so a
	// project that does not use versions costs nothing extra.
	held := heldJQL(keys, label, sched)
	if held == "" {
		return q, nil
	}
	hs, err := j.Search(held, 25)
	if err != nil {
		return Queue{}, err
	}
	for _, i := range hs {
		if reason := sched.HoldReason(i, label); reason != "" {
			q.Held = append(q.Held, HeldTicket{Key: i.Key, Reason: reason})
		}
	}
	return q, nil
}

// queuedJQL is the claim criterion: what a watcher will turn an agent loose
// on. Split from Queued so it can be read in a test -- Queued itself needs a
// live Jira, and this is the part that decides what gets worked.
func queuedJQL(keys []string, label string, sched tracker.Schedules) string {
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
	//
	// The project clause carries the milestone requirement (OR-221): the
	// label says a ticket is READY, a fixVersion says it is SCHEDULED, and
	// only both together make it claimable. In the JQL rather than after the
	// fetch, so an unschedulable ticket never enters the candidate set and
	// cannot be claimed in a race.
	return tracker.JQLAnd(
		sched.Scope(keys),
		tracker.JQLEq("labels", label),
		tracker.JQLNotIn("labels", tracker.LabelWorking, tracker.LabelCIWait, tracker.LabelFailed),
		tracker.JQLNotDone(),
	) + " ORDER BY priority DESC, Rank ASC"
}

// heldJQL is queuedJQL with the milestone requirement inverted: the tickets
// that would have been claimed but for their release. Empty when no project
// in scope enforces the gate, which is how a project that does not use
// versions avoids a second query it can have no answers to.
func heldJQL(keys []string, label string, sched tracker.Schedules) string {
	if label == "" {
		label = tracker.QueueLabelDefault
	}
	held := sched.HeldScope(keys)
	if held == "" {
		return ""
	}
	return tracker.JQLAnd(
		held,
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
// orion-working forever, and every later tick counted a ticket that finished
// hours ago as a live claim -- indistinguishable from a genuinely stuck job
// without opening Jira (OR-125).
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
	// A generous page, deliberately unrelated to the concurrency setting.
	//
	// It used to be the concurrency ceiling plus five, which only worked while
	// a ceiling existed; concurrency is now whatever the operator configured,
	// so sizing this from it would make the page shrink or grow with a number
	// that has nothing to do with how many STALE claims are lying around.
	// Asking for too few is the harmful direction: stale claims would fill the
	// answer, hide a live one behind them, and -- since the stale ones are
	// cleared below -- never be cleared either.
	issues, err := j.Search(jql, claimsPage)
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
		// The stage label goes with the lock. A ticket closed outside Orion
		// keeps whatever it was wearing, so clearing only the lock would
		// leave the board naming an actor for finished work (OR-225).
		if err := j.SetLabels(i.Key, nil,
			append([]string{tracker.LabelWorking}, actors.StageLabels()...)); err != nil {
			ui.Say(w, i.Key, events.ActorOrion, ui.VerbWarn,
				"is %s but still holds the %s lock, and it could not be cleared: %v"+
					" -- remove the label by hand or it keeps a slot",
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

// running is the pool the signal handler reaches for, so the force path can
// name the tickets it is abandoning. Package-level because Listen installs
// the handler before Run builds the pool.
var running atomic.Pointer[pool]

// forceGrace is how long the force path waits for a killed process group to
// actually die before it names the pid and leaves. Short on purpose: forcing
// has to be faster than draining, and SIGKILL cannot be ignored, so anything
// still here after this is a problem no further waiting will solve.
const forceGrace = 3 * time.Second

// forceExit is what the process exits with when it was forced. Non-zero, so
// a supervisor or a script can tell "the operator gave up on it" from "the
// queue drained"; 130 is the shell's own convention for death by SIGINT.
const forceExit = 130

// Listen installs the signal handler. Separate from Run so a caller can
// install it before any long-running work begins.
func Listen(w io.Writer) { listen(w, os.Exit) }

// listen is Listen with its exit seam exposed, and returns a function that
// unregisters the handler. Tests use both; nothing else needs them.
func listen(w io.Writer, exit func(int)) (stop func()) {
	// Room for both signals: the handler is busy printing after the first,
	// and an unbuffered send from the runtime is dropped, not queued.
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go handle(w, sig, exit)
	return func() { signal.Stop(sig) }
}

// handle is the two-signal protocol: the first drains, the second forces.
//
// The second signal used to be handed back to the default disposition
// (signal.Stop), reasoning that a genuinely stuck watcher must still be
// killable without hunting for a pid. It is -- but the default disposition
// for SIGINT terminates THE WATCHER and nothing else. Every agent it started
// is in its own process group (setNewProcessGroup), which is exactly why
// ctrl-c never reaches them, so that escape hatch left `claude -p` running
// with its parent gone: reparented to init, holding a worktree, spending for
// as long as it took somebody to notice (OR-195). At a concurrency cap of
// five it left five of them.
//
// So the watcher owns the force path: kill the groups, say what could not be
// killed and what is left claimed, and exit non-zero.
//
// SIGTERM counts as either signal. `kill <watcher-pid>` from another shell
// has the same shape as ctrl-c, and an unattended watcher is more likely to
// be stopped that way than interactively.
func handle(w io.Writer, sig <-chan os.Signal, exit func(int)) {
	<-sig
	stopping.Store(true)
	// Through the live writer when there is one. Written straight to stdout
	// this line lands below the pinned region and the next redraw erases it,
	// so the instruction for how to force a quit would vanish a quarter of a
	// second after being printed (OR-240).
	fmt.Fprintln(out(w), "\nstopping after the current step. Press ctrl-c again to force,\n"+
		"which kills the running agents now and leaves their tickets claimed.")
	<-sig
	exit(forceQuit(out(w), supervisor.KillAll))
}

// forceQuit kills every agent this watcher started and reports what it left
// behind, returning the code the process should exit with.
//
// The claim is NOT released here, and says so rather than going quiet. The
// alternative is a tracker write per ticket from inside a signal handler,
// which is a network call with no timeout on the one path whose whole point
// is to be immediate -- a hung Jira would hang the force, and the next
// ctrl-c would have nothing left to fall back to. Killing is local and
// always works; naming the ticket is what a person needs to finish the job.
func forceQuit(w io.Writer, kill func(time.Duration) []int) int {
	var keys []string
	if p := running.Load(); p != nil {
		keys = p.keys()
		sort.Strings(keys)
	}

	fmt.Fprintln(w, "\nforcing: killing the agent process group(s) this watcher started.")
	for _, pid := range kill(forceGrace) {
		fmt.Fprintf(w, "  pid %d did not die, and is still running now: "+
			"ps -o pid,ppid,etime,command -p %d\n", pid, pid)
	}
	if len(keys) == 0 {
		fmt.Fprintln(w, "  no ticket was in flight; nothing is left claimed.")
		return forceExit
	}
	fmt.Fprintf(w, "  still claimed and NOT released: %s\n"+
		"  Their agents are dead, but the %s label is still on them, so no\n"+
		"  watcher will pick them up until it is removed in the tracker.\n",
		strings.Join(keys, ", "), tracker.LabelWorking)
	return forceExit
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
