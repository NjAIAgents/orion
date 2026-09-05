package main

// `orion run <workspace> --stage database` -- the database architect's step
// in planning (OR-154).
//
// Routed away from the plain supervised run because it is NOT one: it
// recommends the database, stops for a person to confirm it, and only then
// designs the schema on it. A generic stage run would spend the second run
// before anybody had answered the first question, which is the invariant this
// step exists to hold.
//
// The seams are resolved here and nowhere else, so internal/dbaplan can be
// driven in a test without Slack, without a workspace on disk and without
// spawning an agent.

import (
	"os"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/dbaplan"
	"github.com/orion-sdlc/orion/internal/decide"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func runDatabaseStage(ws *workspace.Workspace, opts supervisor.Options) error {
	// Absent Slack is a normal configuration, not a failure: the record is
	// still written, and it stays unconfirmed, which is where nothing
	// downstream reads it.
	var sl decide.SlackAPI
	if c, err := slack.FromEnv(); err == nil {
		sl = c
	}
	log, err := events.Open(events.Path(ws.Dir), events.Event{})
	if err == nil {
		defer log.Close()
	}
	return dbaplan.Run(ws, config.Load(ws.RepoDir()), dbaplan.Deps{
		Supervise: supervisor.Run,
		Slack:     sl,
		Log:       log,
	}, dbaplan.Options{
		Out:        os.Stdout,
		DryRun:     opts.DryRun,
		MaxMinutes: opts.MaxMinutes,
		MaxTurns:   opts.MaxTurns,
	})
}
