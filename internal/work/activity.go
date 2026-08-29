package work

import (
	"io"
	"strings"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

// ActivityLogger turns the agent's live activity into event-log lines.
//
// Why this exists: with the run's output buffered to exit, the event log
// showed "implementing FCIA-6" and then nothing at all until the run ended.
// Three lines over a run of any length is not a log, and the practical
// consequence is that a working run and a hung one look identical -- so the
// only way to tell them apart was to go and read the git status of a
// worktree by hand.
//
// What it deliberately does NOT do is mirror the whole stream. The verbatim
// NDJSON is already in the run log for postmortems. What belongs in the event
// stream is the trace a person scans to answer "is it doing something sane":
// which files it touched, which commands it ran, what it said it was doing.
//
// Exported so cmd/orion's fix loop -- a different package, run from outside
// internal/work -- wires the SAME logger rather than a second one. A second
// copy is how the two drift apart: OR-176 was exactly that, a hand-rolled
// OnActivity that printed unattributed lines and emitted nothing to the
// event log at all.
func ActivityLogger(log *events.Log, w io.Writer, key, actor string) func(supervisor.Activity) {
	return func(a supervisor.Activity) {
		switch a.Kind {
		case "start":
			log.Emit(events.Event{Kind: events.KindRunStart, Actor: actor,
				Model: a.Model, Msg: "session open"})
		case "tool":
			msg := a.Tool
			if a.Detail != "" {
				msg += " " + a.Detail
			}
			log.Emit(events.Event{Kind: events.KindTool, Actor: actor,
				Model: a.Model, Msg: msg})
			// The agent's own line, carrying its ticket, its name and the
			// model that produced it -- and its prose unedited. The
			// metadata columns are Orion's to style; the text is not.
			ui.SayModel(w, key, actor, a.Model, ui.VerbWorking, "%s %s", verbFor(a.Tool), a.Detail)
		case "text":
			log.Emit(events.Event{Kind: events.KindSay, Actor: actor,
				Model: a.Model, Msg: a.Detail})
		}
	}
}

// verbFor gives the terminal a past-tense verb per tool, so a run reads as a
// narrative rather than as a list of API names. The event log keeps the tool
// name itself, which is what a later query wants to filter on.
func verbFor(tool string) string {
	switch tool {
	case "Read", "NotebookRead":
		return "read"
	case "Edit", "Write", "NotebookEdit":
		return "edited"
	case "Bash":
		return "ran"
	case "Grep", "Glob":
		return "searched"
	case "Task", "Agent":
		return "delegated"
	case "WebFetch", "WebSearch":
		return "fetched"
	case "TodoWrite":
		return "planned"
	}
	if tool == "" {
		return "acted"
	}
	return strings.ToLower(tool)
}
