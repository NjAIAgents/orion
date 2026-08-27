// Package watch is the loop that removes the last manual step.
//
// Everything else in Orion is already automatic between the moment a ticket
// is claimed and the moment its branch merges. What remained was TRIGGERING:
// a person typing `orion work FCIA-7`, then later `orion collect`. This turns
// that into a label on a ticket.
//
// The design in one line: a tick does the cheap reconciling first, then
// starts at most one new job, then sleeps.
//
// Cheap first, because collect costs one API call per waiting ticket and can
// finish work already paid for -- closing a merged ticket, pushing a CI fix,
// asking for an approval. Starting new work before finishing old work would
// mean paying to begin something while something else sat done-but-unclosed.
//
// One job at a time, because two agents in one repository fight over git, and
// because the ordering a person expressed by ranking their backlog is the
// entire point of a queue. Concurrency here would spend more money to produce
// a less predictable order.
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
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
	// Queued returns the keys waiting to be started, in the order they
	// should be started.
	Queued func(home string, projects []string, label string) ([]string, error)
	// InFlight reports whether a job is already running somewhere. The
	// claim label is the lock, so this reads the tracker rather than any
	// state this process holds -- a watcher restarted mid-job must not
	// start a second agent on the same repository.
	InFlight func(home string, projects []string) (bool, string, error)
	Sleep    func(d time.Duration) bool // returns false if interrupted
	Now      func() time.Time
}

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

	started := 0
	draining := false
	for tick := 1; ; tick++ {
		if stopping.Load() {
			break
		}

		n, unfinished, err := oneTick(opts, deps, w, opts.MaxJobs-started)
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
		started += n

		// A dry run has nothing to learn from a second tick: it changes
		// nothing, so every subsequent tick prints the identical thing
		// forever. Rehearsing once is the whole point.
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

// oneTick does the reconciling, then starts at most one job. Returns how many
// jobs it started (always 0 or 1) and whether anything is still unfinished.
//
// The unfinished flag is what lets the loop know it must not exit yet. A
// ticket that has been pushed and is awaiting CI is Orion's responsibility
// until it merges or fails, and nothing else in the system will pick it up.
func oneTick(opts Options, deps Deps, w io.Writer, remaining int) (started int, unfinished bool, err error) {
	// 1. Finish what is already in flight. Cheap, and it can free the job
	// slot this tick is about to look for.
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

	// 2. Is something already running? The label is the lock, and it lives
	// on the ticket rather than in this process, so a restarted watcher --
	// or a second one somebody started by accident -- sees the same answer.
	if deps.InFlight != nil {
		busy, key, err := deps.InFlight(opts.Home, opts.Projects)
		if err != nil {
			return 0, unfinished, err
		}
		if busy {
			ui.Say(w, key, events.ActorOrion, ui.VerbWorking,
				"still running; not starting anything else")
			return 0, unfinished, nil
		}
	}

	if remaining == 0 && opts.MaxJobs > 0 {
		return 0, unfinished, nil
	}

	// 3. Start the next ticket, and only one.
	keys, err := deps.Queued(opts.Home, opts.Projects, opts.QueueLabel)
	if err != nil {
		return 0, unfinished, err
	}
	if len(keys) == 0 {
		return 0, unfinished, nil
	}
	next := keys[0]

	if opts.DryRun {
		// Show the WHOLE queue in the order it would be worked, not just the
		// head. "would start FCIA-7 (3 queued)" says a number; the point of
		// rehearsing is to see whether the ORDER is the one you meant, and
		// that cannot be checked against a count.
		ui.Say(w, next, events.ActorOrion, ui.VerbWaiting, "would start it, then work down this queue:")
		for i, k := range keys {
			marker := "  "
			if i == 0 {
				marker = "->"
			}
			fmt.Fprintf(w, "          %s %d. %s\n", marker, i+1, k)
		}
		if opts.MaxJobs > 0 {
			n := opts.MaxJobs
			if n > len(keys) {
				n = len(keys)
			}
			fmt.Fprintf(w, "          %s\n", ui.Dim(w,
				fmt.Sprintf("--max-jobs %d: it would start the first %d and stop.",
					opts.MaxJobs, n)))
		} else {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w,
				fmt.Sprintf("no job limit: it would work all %d, one at a time, and keep watching.",
					len(keys))))
		}
		return 0, unfinished, nil
	}

	ui.Say(w, next, events.ActorOrion, ui.VerbWorking, "claimed (%d queued)", len(keys))
	res := deps.Work(work.Options{
		Keys: []string{next}, Out: w, Home: opts.Home,
		MaxMinutes: opts.MaxMinutes, MaxTurns: opts.MaxTurns,
	})

	// The plan's own limit, reported by the run that just finished. This is
	// the gate that replaced an invented weekly token number: when the plan
	// says no, wait for the exact second it says yes again.
	for _, r := range res {
		if !r.Limit.OK() {
			until := r.Limit.ResetsAt
			ui.Say(w, r.Key, events.ActorOrion, ui.VerbWarn, "%s", r.Limit.Describe(deps.Now()))
			if d := r.Limit.Wait(deps.Now()); d > 0 {
				// Sleep until the reset rather than polling through it. Every
				// tick in between would start an agent only to be refused,
				// and a refusal still costs an API round trip and a log line
				// -- hundreds of them, for hours, saying the same thing.
				ui.Say(w, r.Key, events.ActorOrion, ui.VerbWaiting,
					"sleeping until %s", until.Local().Format("15:04 Mon"))
				if !deps.Sleep(d + time.Minute) {
					return 0, unfinished, nil
				}
			}
			return 0, unfinished, nil
		}
	}

	// A job that was refused before spending anything -- budget, a dirty
	// sandbox -- must not count against the run's job limit, and must not be
	// retried immediately: the condition that refused it is still true, and
	// a tight loop would hammer the tracker to no purpose.
	for _, r := range res {
		if r.Outcome == work.OutcomeSkipped {
			ui.Say(w, r.Key, events.ActorOrion, ui.VerbWarn,
				"not started; waiting rather than retrying immediately")
			return 0, unfinished, nil
		}
	}

	// The job JUST started is in flight, and only this line knows it.
	//
	// `unfinished` is otherwise learned from the collect at the top of the
	// tick -- which ran BEFORE this job existed and therefore reported
	// nothing about it. So a run that pushed, opened a pull request and
	// moved the ticket to ci-wait was followed by:
	//
	//	bound     OR-39 awaiting CI; the job slot is free
	//	ok        started 1 job(s) and finished them; the limit for this run
	//
	// which is the exact abandonment the drain was written to prevent,
	// surviving the fix because the fix asked the wrong source.
	//
	// The work result says it directly: ci-wait means pushed and waiting.
	// Anything else is terminal for this watcher -- blocked and failed both
	// need a person, and there is nothing to drain toward.
	for _, r := range res {
		if r.Outcome == work.OutcomeCIWait {
			unfinished = true
		}
	}
	return 1, unfinished, nil
}

// Queued lists tickets carrying the queue label, in the tracker's order.
//
// Scoped to registered projects. An unscoped query would match a label
// somebody applied by hand in an unrelated project, and this is the function
// that decides what an agent is turned loose on.
func Queued(j *tracker.Jira, home string, projects []string, label string) ([]string, error) {
	keys, err := scope(home, projects)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	if label == "" {
		label = tracker.QueueLabelDefault
	}
	// Excluding the other managed labels is what stops a ticket being picked
	// up twice. A ticket keeps its queue label while it is worked, so
	// matching on that alone would re-claim something already in flight the
	// moment the in-flight check raced or a second watcher existed.
	jql := tracker.JQLAnd(
		tracker.JQLIn("project", keys...),
		tracker.JQLEq("labels", label),
		tracker.JQLNotIn("labels", tracker.LabelWorking, tracker.LabelCIWait, tracker.LabelFailed),
	) + " ORDER BY priority DESC, Rank ASC"

	issues, err := j.Search(jql, 25)
	if err != nil {
		return nil, err
	}
	return dropClaimedChildren(issues), nil
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
func dropClaimedChildren(issues []tracker.Issue) []string {
	queued := make(map[string]bool, len(issues))
	for _, i := range issues {
		queued[strings.ToUpper(strings.TrimSpace(i.Key))] = true
	}
	var out []string
	for _, i := range issues {
		if p := strings.ToUpper(strings.TrimSpace(i.Parent)); p != "" && queued[p] {
			continue
		}
		out = append(out, i.Key)
	}
	return out
}

// InFlight reports whether any ticket is currently claimed.
func InFlight(j *tracker.Jira, home string, projects []string) (bool, string, error) {
	keys, err := scope(home, projects)
	if err != nil || len(keys) == 0 {
		return false, "", err
	}
	jql := tracker.JQLAnd(
		tracker.JQLIn("project", keys...),
		tracker.JQLEq("labels", tracker.LabelWorking),
	)
	issues, err := j.Search(jql, 5)
	if err != nil {
		return false, "", err
	}
	if len(issues) == 0 {
		return false, "", nil
	}
	return true, issues[0].Key, nil
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
