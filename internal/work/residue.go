package work

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/state"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// What a tripped run leaves behind, and why Orion clears it rather than the
// agent.
//
// The agent now has a bounded cleanup allowance after a trip (see
// internal/hook/breaker.go), but an allowance only helps while the agent is
// still alive to spend it. A run killed on the turn ceiling, or one whose
// last act was the call that tripped, has no such moment. This does not
// depend on one: it runs when the RUN ends, whatever ended it.
//
// It matters because a dirty worktree is not inert. collect's rebaseOnto
// refuses a tree with uncommitted tracked changes, and correctly says so --
// so residue from a trip silently disables the automation that would have
// kept the branch current, and the operator finds out on some later collect
// tick, as three commands to run by hand. OR-192 recovered only because a
// later stage happened to exist and happened to read plans/BLOCKED.md; had
// the run ended on the trip, nothing was coming back for it.
//
// Reverting rather than preserving is a choice, and the reason is that the
// residue of a trip is by definition unverified: the session was stopped for
// not making progress. What it committed survives untouched.
func revertTripResidue(jobPath, branch, key, summary, issueURL string,
	cfg config.Config, ws *workspace.Workspace, log *events.Log, w io.Writer) {

	if jobPath == "" || ws == nil {
		return
	}
	// A dry run removes its own worktree before returning. Nothing ran, so
	// there is nothing to tidy and nothing to say.
	if _, err := os.Stat(jobPath); err != nil {
		return
	}

	sess, tripped := state.New(stateDirOf(jobPath, cfg)).AnyTripped()
	if !tripped {
		return
	}

	dirty, err := workspace.DirtyTracked(jobPath)
	if err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"the breaker tripped (%s) and the worktree could not be read: %v", sess.Tripped, err)
		return
	}
	if dirty == "" {
		return // tripped, but it left the tree clean. Nothing to report here.
	}

	files := len(strings.Split(dirty, "\n"))
	revertErr := workspace.RevertTracked(jobPath)

	outcome := fmt.Sprintf("reverted %d uncommitted file(s)", files)
	verb := ui.VerbWarn
	if revertErr != nil {
		outcome = fmt.Sprintf("could NOT revert %d uncommitted file(s): %v", files, revertErr)
		verb = ui.VerbFail
	}
	ui.Say(w, key, events.ActorOrion, verb,
		"the breaker tripped (%s: %s) and left the worktree dirty; %s",
		sess.Tripped, sess.TrippedDetail, outcome)
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, firstLine(dirty)))
	log.Emitf(events.KindNote, events.ActorOrion,
		"tripped (%s) with a dirty worktree; %s", sess.Tripped, outcome)

	// Said where somebody who was not watching will see it. A blocked rebase
	// discovered on the next collect tick is a slow way to learn that a run
	// ended badly enough to need a person.
	title, body := msgTripResidue(key, summary, sess.Tripped, sess.TrippedDetail,
		branch, dirty, issueURL, revertErr)
	tell(w, log, ws, notify.Event{
		Level: notify.Warning, Workspace: ws.ID, Actor: events.ActorOrion,
		Title: title, Body: body,
	})
}

// stateDirOf resolves where a job's hooks wrote their session state. The
// hooks run with the worktree as their root, so it is that worktree's state
// directory rather than the shared clone's.
func stateDirOf(jobPath string, cfg config.Config) string {
	if filepath.IsAbs(cfg.Paths.State) {
		return cfg.Paths.State
	}
	return filepath.Join(jobPath, cfg.Paths.State)
}
