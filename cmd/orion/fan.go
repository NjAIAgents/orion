package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/fanout"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// `orion fan <plan.json>` -- work several independent Go packages at once
// (OR-230).
//
// The caller is the implementer Orion is already running. It proposes an
// assignment of packages to subagents; this command validates that proposal
// deterministically and either dispatches it or refuses. The agent does not
// decide the fan width, because an agent asked to judge whether its own work
// is separable judges it optimistically, and a wrong answer does not fail --
// it writes a call site against a signature another child is still changing
// and is discovered at merge, or later.
//
// A refusal exits non-zero with the reason and the instruction to work
// serially. That is the same fallback contract `orion explore` has: this can
// only ever save wall time, so no failure of it may be the reason a ticket
// cannot proceed.

// Bounds for one fan-out child. Wider than an explore -- this one is changing
// code, not answering a question -- and much tighter than the parent, which
// still has to build, test and fix everything the children produced.
const (
	fanChildMaxMinutes = 20
	fanChildMaxTurns   = 60
)

// fanChildOptions is what one child runs with, split from runFan so the
// capability bound can be asserted without spawning a process. The same
// reason exploreOptions and fixOptions are split out.
//
// The tool lists are the enforcement of "subagents write, only the parent
// verifies". Not a clause in the prompt: a prompt is advice, and this one has
// to hold against an agent that has just been handed a failing tree and every
// incentive to check its own work. See supervisor.WriteOnlyTools.
//
// The child runs as the SAME actor as the run that asked, on that actor's own
// model. It is doing that actor's job -- implementing -- in a smaller scope,
// so a separate identity would split one ticket's implementation spend across
// two rows of its cost report for no reason a reader benefits from.
func fanChildOptions(key, actor string, a fanout.Assignment) supervisor.Options {
	if actor == "" {
		actor = events.ActorImplementer
	}
	return supervisor.Options{
		Stage:        "fan",
		Prompt:       supervisor.FanChildPrompt(a.Package, a.Task),
		MaxMinutes:   fanChildMaxMinutes,
		MaxTurns:     fanChildMaxTurns,
		AllowedTools: supervisor.WriteOnlyTools,
		DeniedTools:  supervisor.ShellTools,
		Actor:        actor,
		Key:          key,
		Model:        actors.Model(actor),
		Effort:       actors.Effort(actor),
	}
}

func runFan(args []string) {
	repo := argFlag(args, "--repo", ".")
	rest := positional(args, "--repo", "--key")
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "orion: usage: orion fan [--repo DIR] <plan.json>")
		os.Exit(64)
	}

	raw, err := os.ReadFile(rest[0])
	if err != nil {
		fanGiveUp(err)
	}
	plan, err := fanout.ParsePlan(raw)
	if err != nil {
		fanGiveUp(err)
	}

	ws, err := exploreWorkspace()
	if err != nil {
		fanGiveUp(err)
	}
	// The worktree the asking run is standing in, not the sandbox clone. The
	// packages being assigned are the ones in front of it, mid-change, and
	// `go list` has to see that tree to answer about it.
	top, err := topLevel(repo)
	if err != nil {
		fanGiveUp(err)
	}
	jobWS := *ws
	jobWS.RepoPath = top

	cfg := config.Load(top)
	verdict := fanout.Validate(plan, cfg.Limits.MaxConcurrentChildren, fanout.GoList(top))
	if verdict.Serial {
		fanRefuse(verdict.Reason)
	}

	key := argFlag(args, "--key", "")
	if key == "" {
		key = exploreKey(ws, top)
	}
	jobs := make([]supervisor.Options, len(plan.Assignments))
	for i, a := range plan.Assignments {
		jobs[i] = fanChildOptions(key, os.Getenv("ORION_ACTOR"), a)
	}

	// supervisor.Fan states the cost shape before dispatch and marks each
	// child as it lands (CONVENTIONS-orchestration §C and §R). What is added
	// here is which PACKAGE each result belongs to, which Fan cannot know.
	results := supervisor.Fan(&jobWS, jobs)
	logFan(&jobWS, key, verdict.Packages, results)
	os.Exit(printFanResults(os.Stdout, verdict.Packages, results))
}

// printFanResults reports each child against the package it owned and returns
// the exit code.
//
// Every child's report is printed whatever the others did, including the
// failures: a package nobody wrote is work the parent still has to do, and
// finding that out from a summary is much cheaper than finding it out from
// the build. Exit is non-zero when any child failed, so the parent cannot
// read a partial fan as a complete one.
func printFanResults(w io.Writer, packages []string, results []supervisor.FanResult) int {
	failed := 0
	for i, r := range results {
		pkg := packages[i]
		switch {
		case r.Err != nil:
			failed++
			fmt.Fprintf(w, "\n=== %s -- FAILED: %v\n", pkg, r.Err)
		case r.Result == nil || r.Result.ExitCode != 0:
			failed++
			reason := "the child returned nothing"
			if r.Result != nil {
				reason = fmt.Sprintf("exit %d: %s", r.Result.ExitCode, r.Result.Reason)
			}
			fmt.Fprintf(w, "\n=== %s -- FAILED: %s\n", pkg, reason)
		default:
			fmt.Fprintf(w, "\n=== %s\n%s\n", pkg, strings.TrimSpace(r.Result.Final))
		}
	}

	fmt.Fprintf(w, "\norion: %d of %d packages written.\n", len(results)-failed, len(results))
	if failed > 0 {
		fmt.Fprintln(w, "  The packages marked FAILED were not changed. Do that work yourself.")
	}
	fmt.Fprintln(w, "  Nothing has been built or tested: the children have no shell. "+
		"Run the suite ONCE now, and fix what it reports.")
	if failed > 0 {
		return 1
	}
	return 0
}

// logFan records what was dispatched and what came back.
//
// The children's own contexts are gone the moment they exit, so their reports
// are all that ever existed of them. An event that names the packages is also
// the only way to ask later whether a change was written by one agent or
// four, which is the first question about a diff that turns out to be wrong.
func logFan(ws *workspace.Workspace, key string, packages []string, results []supervisor.FanResult) {
	l, err := events.Open(events.Path(ws.Dir), events.Event{})
	if err != nil {
		return
	}
	defer func() { _ = l.Close() }()

	reports := make(map[string]any, len(packages))
	for i, pkg := range packages {
		switch {
		case results[i].Err != nil:
			reports[pkg] = "failed: " + results[i].Err.Error()
		case results[i].Result != nil:
			reports[pkg] = results[i].Result.Final
		default:
			reports[pkg] = "no result"
		}
	}
	l.Emit(events.Event{
		Kind: events.KindNote, Actor: events.ActorOrion, Key: key,
		Msg: fmt.Sprintf("fanned implementation across %d packages: %s",
			len(packages), strings.Join(packages, ", ")),
		Detail: map[string]any{"packages": packages, "reports": reports},
	})
}

// fanRefuse is the validator's answer when the plan cannot run concurrently.
//
// It says which check failed, because an agent told only "no" will propose a
// slightly different plan and pay for another round of `go list`. It does not
// invite a revision: a plan that was rejected for an import edge is rejected
// for the same edge however it is re-spelled, and the work is the same work
// either way.
func fanRefuse(reason string) {
	fmt.Fprintf(os.Stderr, "orion: this plan runs serially, not concurrently.\n"+
		"  %s\n"+
		"  Work the packages yourself, in dependency order. Do not re-propose:\n"+
		"  the check is deterministic and will give the same answer.\n", reason)
	os.Exit(1)
}

// fanGiveUp is the one exit for every failure that is not a refusal: say what
// broke, then send the caller back to doing the work itself.
func fanGiveUp(err error) {
	fmt.Fprintf(os.Stderr, "orion: fan could not run: %v\n"+
		"  Work the packages yourself; nothing is waiting on this.\n", err)
	os.Exit(1)
}
