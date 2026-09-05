package main

// The runner behind collect.Deps.Conform -- the one model call in the
// plan-conformance pass (OR-158).
//
// Through internal/supervisor rather than a hand-built argv, for the reason
// doneJudge goes through it: the supervisor is the only layer that sees every
// run, so it is what writes the usage line that puts this spend on the
// ticket's own cost report. A runner that builds its own command line also
// inherits the operator's plugins and MCP servers, which is the fault OR-213
// recorded.

import (
	"fmt"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Bounds for the plan-conformance subagent. The same shape as the
// done-triage bounds and for the same reason: it is handed the plan and the
// diff in its prompt and asked one question with a short answer. A run still
// thinking after ten turns has started exploring the repository, which is a
// different job and one nothing here asked for.
const (
	conformMaxMinutes = 5
	conformMaxTurns   = 10
)

// conformOptions is what the subagent runs with, split from conformReview so
// the actor, model and bounds it is configured with can be asserted without
// spawning a process.
func conformOptions(key, prompt string) supervisor.Options {
	return supervisor.Options{
		Stage:      "plan-conform",
		Prompt:     prompt,
		MaxMinutes: conformMaxMinutes,
		MaxTurns:   conformMaxTurns,
		// Its own actor on its own model, attributed to the ticket it read.
		// NOT done triage's: the two passes answer different questions and
		// sharing an actor would merge two rows of the cost report into one
		// that names neither.
		Actor: events.ActorPlanConform, Key: key,
		Model:  actors.Model(events.ActorPlanConform),
		Effort: actors.Effort(events.ActorPlanConform),
	}
}

// conformReview asks the conformance question and returns the reply verbatim.
//
// Parsing lives in internal/conform, which is where the reply contract is
// defined. This returns the words; it does not decide what they mean.
func conformReview(ws *workspace.Workspace, key, prompt string) (string, error) {
	res, err := supervisor.Run(ws, conformOptions(key, prompt))
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("exited %d (%s)", res.ExitCode, res.Reason)
	}
	return res.Final, nil
}
