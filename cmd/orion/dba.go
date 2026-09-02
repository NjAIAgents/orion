package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// `orion dba [<KEY>] ["<question>"]` -- put a database question to the
// database architect directly (OR-135).
//
// The third of this actor's three invocation paths, and the one that exists
// for a moment the other two cannot reach. The advisor answers a question
// raised inside somebody else's run; the pipeline stage reviews a change that
// has already been written. Neither covers "this query got slow, look at it",
// which is how a database problem usually arrives -- as a complaint, before
// anybody has written a ticket, let alone a diff.
//
// SO IT WORKS WITH NO TICKET AT ALL. A key is optional: with one, the question
// is the ticket and the spend is a row in that ticket's cost report; without
// one, the question is whatever was typed and the run says out loud that it
// is attributed to nothing.
//
// It writes NOTHING and runs NO migration -- see supervisor.DBAAskPrompt. The
// output is a proposal on a terminal; a person applies it. That is the same
// division the pipeline stage keeps, and it is what makes it safe for this
// command to be pointed at a database at all.

// Bounds for an explicitly-asked review. Wider than the cheap readers
// (explore, log triage) because this one may connect to a database and wait on
// a query plan, and narrower than a ticket run because it changes nothing:
// there is no suite to run and no branch to get right.
const (
	dbaAskMaxMinutes = 20
	dbaAskMaxTurns   = 60
)

// dbaAskOptions is what the review runs with, split from runDBACommand so the
// actor, model and prompt it is configured with can be asserted without
// spawning a process -- the same reason exploreOptions and aiopsOptions are.
func dbaAskOptions(key, question string, cfg config.Config) supervisor.Options {
	return supervisor.Options{
		Stage:      "dba",
		Prompt:     supervisor.DBAAskPrompt(question, dbaTarget(cfg, os.Stderr)),
		MaxMinutes: dbaAskMaxMinutes,
		MaxTurns:   dbaAskMaxTurns,
		// Its own actor on its own model, attributed to the ticket when there
		// is one, so an afternoon of "look at this query" is a visible row in
		// the cost report rather than spend nothing can account for (OR-135
		// names this as a reason the actor is its own actor at all).
		Actor: events.ActorDBA, Key: key,
		Model:  actors.Model(events.ActorDBA),
		Effort: actors.Effort(events.ActorDBA),
	}
}

// dbaTarget resolves the database this run may reach, and refuses one that
// names itself production.
//
// REFUSED, not warned about and used anyway. The whole hazard this setting
// exists for is a value copied from somewhere else with the environment left
// in it, and a warning printed above a session that then connects is a warning
// read after the EXPLAIN.
func dbaTarget(cfg config.Config, warn *os.File) supervisor.DBATarget {
	if word, isProd := cfg.DBA.ProductionDSN(); isProd {
		fmt.Fprintf(warn, "orion: dba.non_prod_dsn contains %q, so it was refused and nothing "+
			"will be connected to.\n  This review is static. Point it at a throwaway "+
			"database, or leave it empty.\n", word)
		return supervisor.DBATarget{}
	}
	return supervisor.DBATarget{DSN: cfg.DBA.NonProdDSN}
}

func runDBACommand(args []string) {
	repo := argFlag(args, "--repo", ".")
	rest := positional(args, "--repo", "--key")

	// A leading argument that looks like an issue key IS one; anything else is
	// the question. Deliberately positional rather than a --key flag only:
	// `orion dba OR-135` is what somebody reaches for, and a command that
	// answered "usage" to the obvious invocation is a command nobody uses
	// twice. --key still works, for the case where the question itself starts
	// with something key-shaped.
	key := argFlag(args, "--key", "")
	if key == "" && len(rest) > 0 && looksLikeIssueKey(rest[0]) {
		key, rest = rest[0], rest[1:]
	}
	question := strings.TrimSpace(strings.Join(rest, "\n"))

	if key == "" && question == "" {
		fmt.Fprintln(os.Stderr,
			`orion: usage: orion dba [<KEY>] ["this query got slow, look at it"] [--repo DIR]`)
		fmt.Fprintln(os.Stderr,
			"  A key, a question, or both. With neither there is nothing to look at.")
		os.Exit(64)
	}

	ws, top := dbaWorkspace(key, repo)

	// The globally configured roster (OR-132), so the review's own lines name
	// the actor the way the operator named it.
	agents, err := config.LoadAgents(workspace.Home())
	exitOn(err)
	exitOn(actors.Configure(agents))

	cfg := config.Load(top)
	if question == "" {
		question = dbaQuestionFromTicket(key)
	}
	if question == "" {
		exitOn(fmt.Errorf("%s could not be read from the tracker and no question was given, "+
			"so there is nothing to look at.\n  Try: orion dba %s \"what is slow, and where\"",
			key, key))
	}
	if key == "" {
		// Not fatal. An unattributed answer is still an answer; an answer
		// withheld over bookkeeping is not -- the same call `orion explore`
		// makes.
		fmt.Fprintln(os.Stderr, "orion: no ticket given, so this review will not appear in "+
			"any ticket's cost report")
	}

	res, err := supervisor.Run(ws, dbaAskOptions(key, question, cfg))
	exitOn(err)
	if res.ExitCode != 0 {
		exitOn(fmt.Errorf("the database review exited %d: %s", res.ExitCode, res.Reason))
	}
	final := strings.TrimSpace(res.Final)
	if final == "" {
		exitOn(fmt.Errorf("the database review finished without writing anything"))
	}
	fmt.Println(final)
	// Said last, where it is read. Everything above is a PROPOSAL: nothing was
	// applied, and the run was told it may not apply anything.
	fmt.Fprintln(os.Stderr, "\norion: nothing was changed and no migration was run. "+
		"Read the proposal and apply it yourself.")
}

// dbaWorkspace resolves where to run: the workspace bound to the ticket's
// project when there is a key, and otherwise the one bound to the repository
// the caller is standing in.
//
// The second path is what makes "no ticket at all" work. It is also why the
// repository is resolved to its top level and used as the run's RepoPath: the
// question is about the code as it stands right now, in the checkout the
// person asking is looking at, not the sandbox clone -- exactly the
// substitution `orion explore` makes for the same reason.
func dbaWorkspace(key, repo string) (*workspace.Workspace, string) {
	top, err := topLevel(repo)
	exitOn(err)

	if key != "" {
		ws, _, wErr := resolveWorkspace(key)
		if wErr == nil {
			return ws, top
		}
		// Not fatal: a key that is not registered here is a perfectly ordinary
		// thing to ask about from inside the repository it is about.
		fmt.Fprintf(os.Stderr, "orion: %s is not a registered project here, "+
			"so this runs against %s\n", key, top)
	}
	ws := workspace.FindBySource(top)
	if ws == nil {
		exitOn(fmt.Errorf("%s is not adopted by Orion, so there is no workspace to run in.\n"+
			"  Adopt it with: orion init", top))
	}
	ws.RepoPath = top
	return ws, top
}

// dbaQuestionFromTicket falls back to the ticket's own text when a key was
// given with no question. An unreachable tracker is not fatal here: the caller
// is told to type the question instead, which costs them a line and works.
func dbaQuestionFromTicket(key string) string {
	if key == "" {
		return ""
	}
	j, err := tracker.NewJiraFromEnv()
	if err != nil {
		return ""
	}
	issue, err := j.GetIssue(key)
	if err != nil || issue == nil {
		return ""
	}
	return strings.TrimSpace(key + ": " + issue.Summary + "\n\n" + issue.Description)
}

// looksLikeIssueKey reports whether an argument is an issue key rather than a
// question. PROJ-123, case-insensitively; a question is prose and does not
// take that shape.
func looksLikeIssueKey(s string) bool {
	project, num, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok || project == "" || num == "" || strings.ContainsAny(s, " \t") {
		return false
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
