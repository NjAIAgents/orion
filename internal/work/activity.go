package work

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

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
// activityNote is one tool call as a row-width phrase: the tool, and enough
// of its detail to say WHICH file or command. Trimmed here rather than at
// render time so the renderer never has to know what a tool call looks like.
func activityNote(a supervisor.Activity) string {
	n := a.Tool
	if a.Detail != "" {
		n += " " + a.Detail
	}
	return strings.TrimSpace(n)
}

// heartbeatEvery bounds how often a run says it is still working.
//
// Chosen against the two failures either side of it. Print every tool call
// and you get OR-217: 60% of a screen at concurrency 4, with the stage and
// verdict lines that matter lost inside it. Print nothing -- which is what
// removing the live region left, since ui.Trace is gated behind --verbose
// and ui.LiveActivityNote became a no-op -- and a working run is
// indistinguishable from a hung one without opening a worktree by hand.
//
// One line per ticket per interval is four lines a minute at concurrency 4:
// enough to see progress, little enough to read.
const heartbeatEvery = 60 * time.Second

// heartbeatNow is the clock the throttle reads. A variable so a test can
// move time rather than sleep a minute to observe a one-minute rule.
var heartbeatNow = time.Now

// heartbeat rate-limits a single run's progress line.
//
// Per logger rather than process-wide: each ticket gets its own line on its
// own schedule, so a fast run is not silenced by a slow one that happened to
// print first.
type heartbeat struct {
	mu    sync.Mutex
	start time.Time
	last  time.Time
}

// due reports whether enough time has passed to print again, and records
// that it did. The first call is never due: "started" has just been printed
// above it, and a heartbeat one second later says nothing new.
func (h *heartbeat) due(now time.Time) (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.start.IsZero() {
		h.start, h.last = now, now
		return 0, false
	}
	if now.Sub(h.last) < heartbeatEvery {
		return 0, false
	}
	h.last = now
	return now.Sub(h.start), true
}

func ActivityLogger(log *events.Log, w io.Writer, key, actor string) func(supervisor.Activity) {
	hb := &heartbeat{}
	return func(a supervisor.Activity) {
		switch a.Kind {
		case "start":
			// What the run was GIVEN, recorded at the moment it opens.
			// Until OR-213 the only way to discover a run's toolset was to
			// read a raw transcript, which is how 179 tools -- 148 of them
			// MCP tools against the operator's own live accounts -- went
			// unnoticed. A capability nobody can see is one nobody governs.
			log.Emit(events.Event{Kind: events.KindRunStart, Actor: actor,
				Model: a.Model, Msg: sessionOpen(a), Detail: capabilities(a)})
		case "tool":
			// The live region's only input. A tool call is the finest-grained
			// evidence Orion has that a run is doing something, which is
			// exactly what a sparkline needs and what a spinner cannot claim:
			// the spinner says the watcher is alive, this says the AGENT is
			// (OR-240). Counted whatever the verbosity -- the console filter
			// below decides what is printed, not what happened.
			// The call itself, for the ticket's own row (OR-265).
			ui.LiveActivityNote(key, actor, activityNote(a))
			// A delegation is a subagent starting, which the row counts
			// separately: tool calls say the run is busy, this says how many
			// things are busy on its behalf (ADR 0016).
			if a.Tool == "Task" || a.Tool == "Agent" {
				ui.LiveAgents(key)
			}
			msg := a.Tool
			if a.Detail != "" {
				msg += " " + a.Detail
			}
			log.Emit(events.Event{Kind: events.KindTool, Actor: actor,
				Model: a.Model, Msg: msg})
			// The default-verbosity progress signal (OR-338). Throttled, so
			// what reaches the screen is "this run is alive and here is the
			// last thing it did" rather than the transcript -- which stays
			// behind --verbose, unchanged, on the Trace below.
			//
			// Driven BY tool calls rather than by a timer: a run that has
			// genuinely stopped doing anything emits nothing, so silence
			// still means silence. A ticker would report a hung run as
			// healthy once a minute, which is the failure this replaces.
			// Not under --verbose: the Trace below already prints this same
			// tool call, and a heartbeat beside it is the same fact twice.
			if elapsed, ok := hb.due(heartbeatNow()); ok && !ui.Verbose() {
				ui.Say(w, key, actor, ui.VerbWorking, "%s · %s %s",
					elapsed.Round(time.Second), verbFor(a.Tool), a.Detail)
			}
			// The agent's own line, carrying its ticket, its name and the
			// model that produced it -- and its prose unedited. The
			// metadata columns are Orion's to style; the text is not.
			//
			// Through ui.Trace, so it reaches the CONSOLE only under
			// --verbose. The Emit above is unconditional and runs first:
			// this is the transcript, it is what OR-217 measured at 60% of
			// a screen at concurrency 4, and it is already complete in the
			// event log for anyone reading the run back.
			ui.Trace(w, key, actor, a.Model, ui.VerbWorking, "%s %s", verbFor(a.Tool), a.Detail)
		case "text":
			log.Emit(events.Event{Kind: events.KindSay, Actor: actor,
				Model: a.Model, Msg: a.Detail})
		}
	}
}

// sessionOpen states the run's capabilities in the line a person reads.
//
// An unreported toolset says so rather than reading as "no tools": a CLI
// that omits the field from its init frame would otherwise have its silence
// rendered as a claim, and this line exists precisely so capabilities are
// measured rather than asserted.
func sessionOpen(a supervisor.Activity) string {
	if a.Tools == 0 {
		return "session open (toolset not reported)"
	}
	msg := fmt.Sprintf("session open: %d tools", a.Tools)
	if len(a.MCPServers) == 0 {
		return msg + ", no MCP servers"
	}
	return msg + ", MCP: " + strings.Join(a.MCPServers, ", ")
}

// capabilities is the same fact in the form a query can filter on. Both
// forms, because the log serves a person scanning `orion tail` now and a
// reader asking "which runs had a write path to the tracker" months later,
// and prose cannot be filtered.
func capabilities(a supervisor.Activity) map[string]any {
	if a.Tools == 0 && len(a.MCPServers) == 0 {
		return nil
	}
	servers := a.MCPServers
	if servers == nil {
		servers = []string{}
	}
	return map[string]any{"tools": a.Tools, "mcp_servers": servers}
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
