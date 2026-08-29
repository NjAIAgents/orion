package workspace

import (
	"path/filepath"
	"sync"
)

// Serialising git against the shared clone.
//
// There is ONE clone per project (ws.Dir/repo) and every job worktree hangs
// off it. Worktrees isolate files -- their own working directory, index and
// HEAD -- and share everything else: the object store, the refs, packed-refs,
// and the worktree registry under .git/worktrees. So the isolation that makes
// per-job worktrees safe for the AGENT says nothing about the operations that
// CREATE and DESTROY them.
//
// Every job start runs `git fetch --prune origin` in that clone, then reads
// the ref list to pick a free branch name, then `git worktree add`. Every
// finished ticket runs `git worktree remove` and a branch delete, and a stale
// branch is rebased and force-pushed. Run one ticket at a time and those never
// overlap. Run two and they do: two fetches writing packed-refs, a
// worktree-add racing a worktree-remove, and -- the one that actually bites --
// two jobs reading the same "this branch name is free" answer and both taking
// it.
//
// One mutex per clone is the whole fix. It is cheap in the only unit that
// matters: an agent run is six to eleven minutes and a fetch is a second, so
// serialising the git is unmeasurable against the thing it is protecting.
// Per-task clones would also work and cost a full copy of the repository per
// ticket; that trade is recorded in OR-185 and deliberately not taken here.
//
// In-process only. Across processes the claim label on the ticket is the lock
// (see watch.InFlight) -- two watchers do not work the same ticket, so they do
// not race for the same branch name.

var repoLocks sync.Map // canonical clone path -> *sync.Mutex

// LockRepo blocks until this workspace's shared clone is free, and returns the
// release. Always `defer` it: a git command that fails still has to unlock, or
// the first error freezes every later job on this project.
//
// Not re-entrant. Callers that compose two locked operations -- list the
// worktrees, then remove one -- take the lock twice in sequence rather than
// nesting, which is correct here because each git command is what has to be
// serialised, not the sequence.
func LockRepo(ws *Workspace) func() {
	return lockRepoPath(filepath.Join(ws.Dir, "repo"))
}

func lockRepoPath(clone string) func() {
	v, _ := repoLocks.LoadOrStore(canonPath(clone), &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
