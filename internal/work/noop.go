package work

// A run that changed nothing because there was nothing to change.
//
// Two ways in, one ending. Either the ticket's pull request had already
// merged before this run started, or the agent ran, looked, found the change
// already present and declined to manufacture a diff to justify its own
// existence. Both are correct outcomes and neither is a failure.
//
// They were both reported as failures. A run that produced no commits was
// blocked, and blocked is labelled orion-failed -- so a ticket whose code was
// already on the trunk sat In Progress wearing a failure label, and every
// signal a person looks at said it had failed. Nothing had. Conflating the
// two teaches the reader that orion-failed sometimes means "fine, actually",
// which is how a failure label stops carrying information.
//
// So this ending: no orion-failed, the claim released, the reason on the
// ticket, and the ticket moved on rather than left In Progress for a watcher
// that will never look at it again.

import (
	"fmt"
	"io"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// noopMarker is what the ticket prompt told the agent to write. Taken from
// the prompt package rather than restated here: two copies of a sentinel is
// one edit away from a prompt that asks for a phrase nothing reads.
const noopMarker = supervisor.NoopMarker

// noopDeclared reports whether the agent's closing message declares the work
// already done, and why it said so.
//
// The marker must START a line. An agent that quotes its own instructions
// ("if there is nothing to do, write NOTHING TO DO: ...") would otherwise
// declare a no-op by describing one.
func noopDeclared(final string) (string, bool) {
	for _, line := range strings.Split(final, "\n") {
		// Leading decoration only: models reach for a bullet or a bold run
		// when a line is the conclusion, and the sentinel is worth nothing if
		// a "**" in front of it silently turns the outcome back into failure.
		line = strings.TrimLeft(strings.TrimSpace(line), "*#->_ ")
		if len(line) < len(noopMarker) || !strings.EqualFold(line[:len(noopMarker)], noopMarker) {
			continue
		}
		// The same decoration can close the run as opened it -- "**NOTHING TO
		// DO**" -- so the separator is trimmed from both sides of what is left.
		return strings.TrimSpace(strings.Trim(line[len(noopMarker):], " :-.*_")), true
	}
	return "", false
}

// alreadyMerged ends a run that never started: the pull request had merged
// before this ticket was claimed.
func alreadyMerged(res Result, key, prURL, branch string, cfg config.Config,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	res.Outcome, res.PR, res.Branch = OutcomeNoop, prURL, branch
	res.Note = "its pull request has already merged"
	if prURL != "" {
		res.Note += " (" + prURL + ")"
	}
	ui.Say(w, key, events.ActorOrion, ui.VerbOK,
		"already merged; nothing to do, and nothing was spent")
	if opts.DryRun {
		return res
	}
	release(res.Note+".\n\nThe work is on "+cfg.VCS.WorkBranch+" already. No agent was "+
		"started, so this run cost nothing. If that is wrong, reopen the ticket "+
		"and requeue it.", key, cfg, deps, ws, log, w, res)
	return res
}

// noChange ends a run that DID start, and correctly produced no diff.
func noChange(res Result, key, why string, cfg config.Config,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	res.Outcome = OutcomeNoop
	res.Note = strings.TrimSpace(why)
	if res.Note == "" {
		res.Note = "the agent reported there was nothing to do"
	}
	ui.Say(w, key, events.ActorImplementer, ui.VerbOK,
		"needed no change. That is a result, not a failure.")
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, firstLine(res.Note)))
	if opts.DryRun {
		return res
	}
	release("Orion made no change: "+res.Note+
		"\n\nThe run was clean; it inspected the repository and found the work "+
		"already present rather than inventing a diff to justify itself. This is "+
		"NOT a failure and the ticket is not labelled as one. Reopen and requeue "+
		"it if the work is in fact missing.", key, cfg, deps, ws, log, w, res)
	return res
}

// release is the shared tracker ending: clear every label Orion owns, move
// the ticket on, say why, and tell Slack.
//
// Every label, not just the one that let this run through. Whichever label
// survived is the one that made a finished ticket workable again, and
// clearing only the expected one would leave the same window open.
//
// Transitioned rather than left alone. The ticket was moved to In Progress by
// the run that is now ending; leaving it there is the state the incident
// complained about, and a ticket in progress with no labels is one nothing
// will ever pick up again. The comment says how to undo it.
func release(comment, key string, cfg config.Config, deps Deps,
	ws *workspace.Workspace, log *events.Log, w io.Writer, res Result) {

	if err := deps.Jira.SetLabels(key, nil, tracker.Managed(cfg.Tracker.QueueLabel)); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"nothing to do, but its labels could not be cleared: %v", err)
	}
	if err := deps.Jira.TransitionTo(key, "Done"); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"nothing to do, but it could not be transitioned to Done: %v", err)
	}
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorImplementer, comment))
	log.Emitf(events.KindNote, events.ActorOrion, "no change: %s", firstLine(res.Note))
	title, body := msgNoop(key, res.Summary, res.Note, res.IssueURL)
	tell(w, log, ws, notify.Event{
		Level: notify.Info, Workspace: ws.ID, Actor: events.ActorImplementer,
		Title: title, Body: body,
	})
}
