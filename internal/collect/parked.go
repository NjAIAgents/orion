package collect

import (
	"fmt"
	"path/filepath"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/state"
)

// What collect could not see (OR-232).
//
// A breaker trip is recorded by the hook, in the agent's own session, and the
// recovery command goes to two places: that session, which the operator never
// reads, and plans/BLOCKED.md inside
// ~/.orion/projects/<slug>/worktrees/<branch>/, which the operator does not
// browse. This package -- the one driving the landing queue, whose output IS
// the watch log -- had no view of the trip at all, so the only thing it could
// report was the downstream symptom:
//
//	note   orion  automatic rebase did not run: .../orion-or-217-2 has uncommitted changes
//	OR-217 waiting  orion/or-217-2 is still behind develop and still yours; nothing has moved
//
// Fifteen times in fifteen minutes, naming neither the cause nor the remedy.
// The worktree it is asking about is right there on disk and so is the state
// the hook wrote into it, which is all this needs.
//
// READ FOR WHAT IT LETS US SAY, never for permission to act. That distinction
// is settled in internal/work/residue.go, and the reason it is settled there
// is that the flag is erased by mechanisms which are each individually correct
// -- an unverified-edits trip self-clears on a passing verify, `orion reset`
// clears it by operator action, and every session writes its own file. So a
// missing flag means "nothing to add", never "the worktree is fine": every
// caller here degrades to exactly the message it printed before.

// parkedBy reports the breaker trip that stopped the run holding this
// worktree, when one is still on record there.
//
// state.AnyTripped rather than a lookup by session id, for the reason that
// function documents: a worktree accumulates one state file per session -- the
// implementer, QA, each fix round -- and collect holds none of those ids.
func parkedBy(dir string, cfg config.Config) (kind, detail, session string, ok bool) {
	if dir == "" || cfg.Paths.State == "" {
		return "", "", "", false
	}
	stateDir := cfg.Paths.State
	if !filepath.IsAbs(stateDir) {
		stateDir = filepath.Join(dir, stateDir)
	}
	sess, tripped := state.New(stateDir).AnyTripped()
	if !tripped {
		return "", "", "", false
	}
	return sess.Tripped, sess.TrippedDetail, sess.ID, true
}

// parkedNote is the clause a message about this worktree gains when a breaker
// parked it: what tripped, where the account of it is, and the command that
// resolves it.
//
// A CLAUSE rather than a line of its own, so it rides on the message the
// operator was already going to read instead of being a second thing to
// correlate with the first. Empty when nothing tripped, which is the ordinary
// case and must cost the caller nothing.
func parkedNote(dir string, cfg config.Config) string {
	kind, detail, session, ok := parkedBy(dir, cfg)
	if !ok {
		return ""
	}
	note := fmt.Sprintf("; %s tripped and parked that worktree (%s), and %s/BLOCKED.md "+
		"in it says what the run was attempting", kind, detail, cfg.Paths.Plans)
	if cmd := state.ResetCommand(session); cmd != "" {
		note += "; resume it with: " + cmd
	}
	return note
}
