package collect

import (
	"os"
	"path/filepath"
)

// manualLockName is the file a person drops in a worktree to say "I am
// touching this branch by hand right now" (OR-130).
//
// A human resolving a real conflict is exactly the moment an unattended
// fix-loop or auto-rebase must not also be rewriting the same branch --
// two independent processes force-pushing the same ref at once is luck,
// not design, and `--force-with-lease` racing on the same expected SHA is
// where that luck runs out. The protocol: `touch .orion-manual-lock` in the
// worktree before rebasing or pushing by hand, `rm` it when done. No new
// command is needed because a person taking this path is already sitting
// in that exact directory.
const manualLockName = ".orion-manual-lock"

// manuallyLocked reports whether a worktree carries the manual lock.
//
// Checked by both the fix loop and the auto-rebase path, so either one
// backs off the instant a person is known to be working the branch by
// hand, rather than only one of the two places that can rewrite it.
func manuallyLocked(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, manualLockName))
	return err == nil
}
