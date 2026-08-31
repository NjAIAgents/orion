package work

// A run that never began because Orion's own credential had expired.
//
// Three tickets were claimed and dead within five seconds on 2026-08-30, each
// reported as "stage ticket failed: claude exited 1" and each left wearing
// orion-failed. Nothing had touched them: no turn, no token, no branch work.
// Clearing three labels by hand was the entire cost of a problem whose real fix
// was one command and thirty seconds (OR-212).
//
// So this ending: no orion-failed, the claim handed back to the QUEUE, and the
// cause and the fix said in the terminal and in Slack. The ticket is exactly as
// workable as it was before the run, and it will be worked as soon as somebody
// signs in -- which is the same shape as the quota back-off, for the same
// reason: the condition belongs to the machine, not to the ticket.

import (
	"io"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// notAuthenticated releases the ticket and reports what has to be fixed.
//
// reason is the supervisor's sentence, which already names the CLI's own cause
// and the command that repairs it; it is passed through rather than rewritten
// so one wording reaches the log, the terminal and the notification.
func notAuthenticated(res Result, key, reason string, cfg config.Config,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	res.Outcome = OutcomeNoAuth
	res.Note = reason
	ui.Say(w, key, events.ActorOrion, ui.VerbFail, "%s", reason)
	ui.Say(w, key, events.ActorOrion, ui.VerbOK,
		"nothing was attempted, so %s is queued again rather than marked failed", key)
	log.Emitf(events.KindBlocked, events.ActorOrion, "%s", reason)
	if opts.DryRun {
		return res
	}

	// Back to the queue label the claim removed, and NOT to orion-failed: the
	// deferred rollback in one() adds that for a failed or blocked outcome, and
	// this outcome is neither.
	queue := cfg.Tracker.QueueLabel
	if queue == "" {
		queue = tracker.QueueLabelDefault
	}
	// And the stage label with it: a queued ticket that still named an actor
	// would say somebody is working what nothing has started (OR-225).
	if err := deps.Jira.SetLabels(key, []string{queue},
		append([]string{tracker.LabelWorking}, actors.StageLabels()...)); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"could not requeue it; remove its %s label by hand or nothing will pick it up: %v",
			tracker.LabelWorking, err)
	}
	// The claim moved it to In Progress. Left there it would say an agent is
	// working it while the queue says it is waiting, which is the same
	// contradiction the label rollback exists to prevent.
	if err := deps.Jira.TransitionTo(key, "To Do"); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"left it In Progress: %v", err)
	}

	title, body := msgNoAuth(key, res.Summary, reason, res.IssueURL)
	tell(w, log, ws, notify.Event{
		Key: key, Level: notify.Blocked, Workspace: ws.ID, Actor: events.ActorOrion,
		Title: title, Body: body,
	})
	return res
}
