package collect

import (
	"errors"
	"fmt"
	"io"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The rebase that happens BEFORE a branch is ever pushed (OR-227).
//
// An agent run takes ten to forty minutes. At concurrency 4 something else
// usually merges inside that window, so by the time the branch is pushed its
// base has already moved -- and the checks that push triggers are describing a
// base that no longer exists. The landing pass then notices, rebases, and
// force-pushes, and CI runs a SECOND time on a second commit, both runs doing
// full work. On 2026-08-30 that read, once per ticket:
//
//	rebase  OR-219: orion/or-219 was behind develop; rebased and pushed, checks re-running
//
// One fetch and one replay immediately before the push makes the first run the
// one that counts. This is a stage earlier than the landing queue (OR-206),
// which manages contention among branches ALREADY queued to land; it stops a
// branch entering that queue stale in the first place, and the two compound.
//
// THE WINDOW NARROWS, IT DOES NOT CLOSE. The base can still move between this
// rebase and the push, and certainly between the push and the merge. This
// removes the common case and nothing more; OR-206 still handles the rest.
//
// It lives in this package rather than in work because everything it needs is
// already here -- the replay itself, the base resolution (OR-112), the manual
// lock (OR-130), and the commands a person is given on a conflict. A copy in
// the work path would be a second set of refusals to keep in step, which is
// the thing OR-112 was filed about.

// RebaseBeforePush replays a branch onto the current tip of its base, in the
// worktree, immediately before that branch's first push.
//
// It never refuses the push. Every reason not to rebase -- a person holding
// the worktree, an unreachable remote, a conflict -- ends with the branch
// exactly as the agent left it and the caller pushing that. Finished work
// hidden behind an unresolved conflict leaves the operator with nothing to
// look at, and the conflict is just as visible on an open pull request.
//
// Nothing is said on the clean path. A branch that is already current costs
// one fetch and prints nothing at all.
func RebaseBeforePush(key, dir, branch string, cfg config.Config,
	ws *workspace.Workspace, log *events.Log, w io.Writer) {

	// The same resolution the landing path uses (OR-112). There is no pull
	// request yet, so it is the config fallback that answers here -- but
	// asking through baseOf rather than reading cfg.VCS.WorkBranch directly is
	// what stops this path and that one naming different branches later.
	base, named := baseOf(PR{}, cfg)
	if !named {
		return
	}

	// A person working this worktree by hand always wins, exactly as on the
	// landing path (OR-130). Push what they have rather than rewriting it
	// underneath them.
	if manuallyLocked(dir) {
		ui.Say(w, key, events.ActorOrion, ui.VerbWaiting,
			"%s is locked for manual work (%s); pushing it as it stands", branch, manualLockName)
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: "skipped the pre-push rebase: " + manualLockName + " present"})
		return
	}

	// Read before and after rather than asking whether the branch is behind:
	// the answer is only trustworthy after a fetch, and the rebase fetches
	// anyway. Two rev-parses are cheaper than a second round trip.
	before, _ := gitLine(dir, "rev-parse", "HEAD")

	// A rebase in a job worktree is still git against the SHARED clone, and
	// other jobs are creating and removing worktrees off the same object
	// store, so it takes the same lock they do (workspace/gitlock.go).
	unlock := workspace.LockRepo(ws)
	err := rebaseLocal(dir, base, branch)
	unlock()

	switch {
	case err == nil:
		after, _ := gitLine(dir, "rev-parse", "HEAD")
		if before == "" || after == before {
			// Already current. Requirement of the quiet path: the common case
			// costs one fetch and produces no output, because a line printed
			// on every run is a line nobody reads on the run that matters.
			return
		}
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: fmt.Sprintf("rebased %s onto %s before its first push", branch, base)})
		ui.Ok(w, "rebase", "%s: %s was behind %s already; rebased before pushing, "+
			"so the first checks are the ones that count", key, branch, base)

	case errors.Is(err, errRebaseConflict):
		// A conflict needs a person, and this is the same message and the same
		// commands the landing path gives them (conflict.go) -- said here, at
		// the moment it is first knowable, instead of one CI run later. The
		// branch is untouched, so those commands are still the right ones.
		ui.Warn(w, "%s: %s does not replay cleanly onto %s; pushing it as it stands "+
			"and opening the pull request anyway", key, branch, base)
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			"resolve it, push, and Orion picks it up again on the next pass:"))
		for _, line := range rebaseSteps(ws, branch, base) {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
		}
		log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorOrion,
			Msg: "branch conflicts with its base before its first push; a human must rebase"})

	default:
		// A circumstance rather than a decision: an unreachable remote, a
		// worktree somebody left dirty. Degrade to the behaviour that existed
		// before this ran at all, which is a stale first CI run -- not a lost
		// push.
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"could not refresh %s onto %s before pushing (%v); pushing it as it stands",
			branch, base, err)
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: "pre-push rebase did not run: " + err.Error()})
	}
}
