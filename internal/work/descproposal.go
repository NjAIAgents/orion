package work

// A run whose deliverable is a Jira description rewrite has no repository
// change to make. Before this, that ran cleanly, produced no commits, and
// ended as an ordinary blocked question -- correct as far as it went, but it
// left the actual answer (the drafted replacement text) buried in a comment
// for a human to notice, extract and apply by hand. OR-288 spent an opus run
// reaching exactly that outcome three times.
//
// This recognises the same shape supervisor.NoopMarker already recognises
// for a different one: a structured signal in the agent's own closing
// message, rather than a reading of its prose, because "I propose this text"
// and "I could not decide what to change" are opposite conclusions that read
// similarly in English.

import (
	"fmt"
	"io"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// descProposalDeclared reports whether the agent's closing message drafted a
// description replacement, and returns the text between the two markers.
//
// Both markers must appear, each starting a line, in that order -- an agent
// that opens the block but never closes it (truncated by a token limit, or
// abandoned mid-thought) is not a proposal, and treating a half-written
// block as one would send unreviewed partial prose toward the approval gate.
func descProposalDeclared(final string) (string, bool) {
	lines := strings.Split(final, "\n")
	start := -1
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), supervisor.DescProposalStart) {
			start = i
			break
		}
	}
	if start == -1 {
		return "", false
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.EqualFold(strings.TrimSpace(lines[i]), supervisor.DescProposalEnd) {
			body := strings.TrimSpace(strings.Join(lines[start+1:i], "\n"))
			if body == "" {
				// An empty block is the same failure SetDescription itself
				// refuses: more likely a template that did not render than
				// an intention, and refusing it here is cheaper than making
				// the tracker layer say so after a round trip to Jira.
				return "", false
			}
			return body, true
		}
	}
	return "", false
}

// descProposed ends a run whose agent drafted a Jira description rewrite
// instead of a repository change.
//
// Not a failure and not orion-failed: the agent did exactly what OR-288's
// class of ticket asks for, which is a draft for a human to approve. The
// claim is released the same way noChange releases one -- the ticket is not
// "in progress" on anything a watcher should wait on, and it is not stuck
// either.
func descProposed(res Result, key, actorID, proposed string,
	deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	res.Outcome = OutcomeNoop
	res.Note = "proposed a description change and is waiting for review"

	if err := collect.RequestDescriptionChange(deps.Jira, key, actorID, proposed, w); err != nil {
		return failAndTell(res, fmt.Errorf("posting the description proposal: %w", err),
			key, ws, log, w, deps)
	}

	log.Emitf(events.KindNote, actorID,
		"drafted a description change; waiting on human review in Jira")

	if err := deps.Jira.SetLabels(key, nil,
		append([]string{tracker.LabelWorking}, actors.StageLabels()...)); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"proposal posted but the claim could not be released: %v", err)
	}
	return res
}
