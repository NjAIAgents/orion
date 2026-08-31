package main

// The runner behind collect.Deps.Judge -- the one model call in the
// done-triage pass (OR-244).
//
// Through internal/supervisor rather than a hand-built argv, for the reason
// `orion aiops` goes through it: the supervisor is the only layer that sees
// every run, so it is what writes the usage line that puts this spend on the
// ticket's own cost report instead of leaving it unaccounted for. A runner
// that builds its own command line also inherits the operator's plugins and
// MCP servers, which is the fault OR-213 recorded.

import (
	"fmt"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Bounds for the done-triage subagent. Tight, and for the same reason the
// log-triage and AIOps bounds are: it is handed the ticket and the diff in
// its prompt and asked one question with a one-line answer. A run still
// thinking after ten turns has stopped being the cheap second reading it was
// started as and has begun exploring the repository, which is a different job.
const (
	doneMaxMinutes = 5
	doneMaxTurns   = 10
)

// doneOptions is what the subagent runs with, split from doneJudge so the
// actor, model and bounds it is configured with can be asserted without
// spawning a process -- the same reason aiopsOptions and triageOptions are
// split from their callers.
func doneOptions(key, prompt string) supervisor.Options {
	return supervisor.Options{
		Stage:      "done-triage",
		Prompt:     prompt,
		MaxMinutes: doneMaxMinutes,
		MaxTurns:   doneMaxTurns,
		// Its own actor on its own model, attributed to the ticket it read,
		// so the pass is a visible row in that ticket's cost report.
		Actor: events.ActorDoneTriage, Key: key,
		Model:  actors.Model(events.ActorDoneTriage),
		Effort: actors.Effort(events.ActorDoneTriage),
	}
}

// doneJudge asks the intent question and returns the reply verbatim.
//
// Parsing lives in internal/done, which is where the reply contract is
// defined. This returns the words; it does not decide what they mean.
func doneJudge(ws *workspace.Workspace, key, prompt string) (string, error) {
	res, err := supervisor.Run(ws, doneOptions(key, prompt))
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("exited %d (%s)", res.ExitCode, res.Reason)
	}
	return res.Final, nil
}
