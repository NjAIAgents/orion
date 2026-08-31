package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/watch"
	"github.com/orion-sdlc/orion/internal/work"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// watchBanner states the terms before the first tick. This is the one
// command that spends money with nobody watching, so what it will and will
// not do belongs on screen before it starts rather than in a manual.
//
// Split out of runWatch so the "something is on screen before any network
// call" guarantee is reachable from a test; runWatch itself needs a
// configured tracker and exits the process on failure.
func watchBanner(w io.Writer, projects []string, interval time.Duration, maxJobs, concurrent int, concurrentFrom string, dry bool) {
	fmt.Fprintf(w, "%s\n", ui.Heading(w, "watching"))
	scope := "every registered project"
	if len(projects) > 0 {
		scope = strings.Join(projects, ", ")
	}
	fmt.Fprintf(w, "  %s\n", ui.Dim(w, "scope     "+scope))
	// Both halves of the claim criterion, because half of it is new (OR-221)
	// and a ticket held back for the second half is otherwise a ticket that
	// looks labelled and never runs.
	fmt.Fprintf(w, "  %s\n", ui.Dim(w, "queue     tickets labelled "+tracker.QueueLabelDefault+
		", and -- where the project uses releases -- attached to an open one"))
	fmt.Fprintf(w, "  %s\n", ui.Dim(w, "interval  "+interval.String()))
	// Said up front, with its source, because it is the setting that decides
	// how much money is in flight at once and it is read from a file the
	// operator may not have opened.
	fmt.Fprintf(w, "  %s\n", ui.Dim(w, fmt.Sprintf("at once   %d ticket(s) (%s)", concurrent, concurrentFrom)))
	// What is on screen, and what is being kept back. An operator who is not
	// told the console is filtered reads a quiet run as a stalled one -- and
	// the answer to "where did the rest go" has to be on screen before the
	// question is asked (OR-217).
	if ui.Verbose() {
		fmt.Fprintf(w, "  %s\n", ui.Dim(w, "printing  --verbose: every tool call the agents make"))
	} else {
		fmt.Fprintf(w, "  %s\n", ui.Dim(w,
			"printing  stages, outcomes and anything awaiting you; --verbose adds "+
				"the agents' tool calls (always in the event log: orion logs KEY)"))
	}
	switch {
	case dry:
		fmt.Fprintf(w, "  %s\n", ui.Dim(w, "limit     --dry-run: nothing will be started"))
	case maxJobs > 0:
		fmt.Fprintf(w, "  %s\n", ui.Dim(w, fmt.Sprintf("limit     %d job(s), then stop", maxJobs)))
	default:
		ui.Warn(w, "no job limit: this will keep starting tickets, and spending, until stopped.")
		fmt.Fprintf(w, "  %s\n", ui.Dim(w,
			"          Use --max-jobs N for an unattended run you have not watched before."))
	}
	fmt.Fprintf(w, "  %s\n\n", ui.Dim(w, "ctrl-c to stop after the current step"))
}

// watchInterval reads --interval S, in seconds, and applies the SAME
// fallback the loop does: anything at or below zero means the default.
//
// Resolved here rather than left to watch.Run because the banner prints the
// effective interval before Run is called, and a banner that says 0s while
// the loop ticks at the default is worse than no banner at all.
func watchInterval(args []string) time.Duration {
	if n := intFlag(args, "--interval", 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return watch.DefaultInterval
}

func runWatch(args []string) {
	w := os.Stdout

	projects, err := projectKeys(
		positional(args, "--interval", "--max-jobs", "--max-minutes", "--max-turns"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}

	interval := watchInterval(args)
	maxJobs := intFlag(args, "--max-jobs", 0)
	once := hasFlag(args, "--once")
	dry := hasFlag(args, "--dry-run")

	// The banner goes out BEFORE anything that could block -- before the
	// credential read, before the tracker client, and long before the first
	// network call (OR-128). It used to print after the client was built,
	// which was harmless right up until something ahead of it stalled: then
	// `orion watch` sat there having printed literally nothing, and "still
	// starting up" looked exactly like "hung, kill it".
	concurrent, concurrentFrom := watch.Concurrency(workspace.Home(), projects)
	watchBanner(w, projects, interval, maxJobs, concurrent, concurrentFrom, dry)

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	watch.Listen(w)

	err = watch.Run(watch.Options{
		Out: w, Home: workspace.Home(),
		Interval: interval, MaxJobs: maxJobs, Once: once, DryRun: dry,
		Projects: projects, MaxConcurrent: concurrent,
		// Zero is the sentinel for "not set", NOT a default. Filling the
		// human-readable defaults in here made them EXPLICIT values, and
		// turnsFor/minutesFor let explicit win -- so the sub-task scaling
		// (120+25N turns) could never apply on the watch path, and a
		// four-sub-task story died at turn 121 having cost $17 (OR-117).
		// The real defaults live in turnsFor/minutesFor, next to the
		// scaling they belong with.
		MaxMinutes: intFlag(args, "--max-minutes", 0),
		MaxTurns:   intFlag(args, "--max-turns", 0),
	}, watch.Deps{
		Collect: func(o collect.Options) []collect.Result {
			return collect.Run(o, collect.Deps{
				Jira: mustJiraSearch(), Status: prStatus,
				Refresh: workspace.Refresh, Prune: pruneBranch,
				Merge: mergePR, Fix: fixRun, Slack: slackForApproval(),
			})
		},
		Work: func(o work.Options) []work.Result {
			return work.Run(o, work.Deps{
				Jira: mustJira(), Supervise: supervisor.Run, Advise: adviseRunner,
				Describe: describeRunner,
				Push:     pushBranch, OpenPR: openPR, Merged: mergedBranch,
				Slack: slackForHold(), Preflight: preflightEnv,
			})
		},
		// How a hold gets released without anyone running a command: the
		// reaction is read here, and the doctor check re-run, on every tick.
		Release: work.ReleaseDeps{Slack: slackForHold(), Recheck: recheckEnv},
		// watch.Queue, not []tracker.Issue: develop's return type, kept over
		// the branch's older one. OR-214 only ever needed to ADD Release here
		// -- it touched this line because it sits adjacent, not because the
		// signature is its business.
		Queued: func(home string, ps []string, label string) (watch.Queue, error) {
			return watch.Queued(j, home, ps, label)
		},
		InFlight: func(home string, ps []string) ([]string, error) {
			return watch.InFlight(j, home, ps, os.Stdout)
		},
	})
	exitOn(err)
}
