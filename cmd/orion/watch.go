package main

import (
	"fmt"
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

func runWatch(args []string) {
	w := os.Stdout

	var projects []string
	for _, a := range positional(args, "--interval", "--max-jobs", "--max-minutes", "--max-turns") {
		projects = append(projects, strings.ToUpper(a))
	}

	interval := time.Duration(intFlag(args, "--interval", 120)) * time.Second
	maxJobs := intFlag(args, "--max-jobs", 0)
	once := hasFlag(args, "--once")
	dry := hasFlag(args, "--dry-run")

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	// State the terms before the first tick. This is the one command that
	// spends money with nobody watching, so what it will and will not do
	// belongs on screen before it starts rather than in a manual.
	fmt.Fprintf(w, "%s\n", ui.Heading(w, "watching"))
	scope := "every registered project"
	if len(projects) > 0 {
		scope = strings.Join(projects, ", ")
	}
	fmt.Fprintf(w, "  %s\n", ui.Dim(w, "scope     "+scope))
	fmt.Fprintf(w, "  %s\n", ui.Dim(w, "queue     tickets labelled "+tracker.QueueLabelDefault))
	fmt.Fprintf(w, "  %s\n", ui.Dim(w, "interval  "+interval.String()))
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

	watch.Listen(w)

	err = watch.Run(watch.Options{
		Out: w, Home: workspace.Home(),
		Interval: interval, MaxJobs: maxJobs, Once: once, DryRun: dry,
		Projects:   projects,
		MaxMinutes: intFlag(args, "--max-minutes", 90),
		MaxTurns:   intFlag(args, "--max-turns", 120),
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
				Push:     pushBranch, OpenPR: openPR,
			})
		},
		Queued: func(home string, ps []string, label string) ([]string, error) {
			return watch.Queued(j, home, ps, label)
		},
		InFlight: func(home string, ps []string) (bool, string, error) {
			return watch.InFlight(j, home, ps)
		},
	})
	exitOn(err)
}
