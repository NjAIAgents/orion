package collect

// The repository side of batch integration (OR-236): the Git implementation
// the pure logic in batch.go is written against.
//
// Kept apart from batch.go on purpose. Assembly order, ejection and bisection
// are where the interesting mistakes live, and they are tested against a fake
// precisely so those tests do not need a repository. This file is the thin,
// boring half: it runs git and reports what happened.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/workspace"
)

// repoGit runs batch operations against a workspace's sandbox clone.
//
// The SANDBOX, never a job worktree. A job worktree is one branch's checkout
// and an agent may still be working in it; assembling a batch there would put
// other tickets' commits under somebody's feet. The sandbox is the shared
// clone every worktree already hangs off, which is also why every operation
// here takes the repo lock: a batch fetching and creating refs while another
// job adds or removes a worktree is exactly the race workspace.LockRepo
// exists for.
type repoGit struct {
	ws *workspace.Workspace
}

// CloneDir, emphatically not RepoDir. RepoDir resolves to a per-job worktree
// whenever one is set, so using it here would assemble a batch inside a
// running agent's checkout -- putting other tickets' commits under somebody's
// feet, which is the one thing this must never do. The distinction is
// invisible at the call site and the compiler cannot catch it, so it is
// spelled out here.
func (r repoGit) dir() string { return r.ws.CloneDir() }

// CutRef creates ref at base, discarding any previous ref of that name.
//
// Discarding rather than refusing: the ref is ephemeral by definition, and a
// leftover from an interrupted batch is not a reason to refuse the next one.
// It is created DETACHED from any worktree, so nothing has it checked out and
// deleting it later cannot disturb a job.
func (r repoGit) CutRef(ref, base string) error {
	defer workspace.LockRepo(r.ws)()

	if out, err := git(r.dir(), "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("fetching before assembling the batch: %w\n%s", err, out)
	}
	baseRef := "origin/" + base
	if _, err := git(r.dir(), "rev-parse", "--verify", "--quiet", baseRef); err != nil {
		baseRef = base
		if _, err := git(r.dir(), "rev-parse", "--verify", "--quiet", baseRef); err != nil {
			return fmt.Errorf("base %q exists neither locally nor on origin", base)
		}
	}
	if out, err := git(r.dir(), "branch", "-f", ref, baseRef); err != nil {
		return fmt.Errorf("cutting %s from %s: %w\n%s", ref, baseRef, err, out)
	}
	return nil
}

// MergeInto merges branch into ref, and reports a conflict as an error so the
// caller ejects.
//
// A --no-ff merge into a temporary WORKTREE for the ref, not a checkout of
// the sandbox: the sandbox's own HEAD is what every other operation assumes,
// and moving it for the duration of a batch would break anything that reads
// it concurrently.
//
// On conflict the merge is ABORTED before returning. Leaving a half-merged
// state behind would make the next member's merge fail for a reason that has
// nothing to do with that member -- one conflict would cascade into ejecting
// the whole batch.
func (r repoGit) MergeInto(ref, branch string) error {
	defer workspace.LockRepo(r.ws)()

	wt, err := r.refWorktree(ref)
	if err != nil {
		return err
	}
	src := branch
	if _, err := git(wt, "rev-parse", "--verify", "--quiet", "origin/"+branch); err == nil {
		src = "origin/" + branch // the pushed branch is what would really merge
	}
	out, mergeErr := git(wt, "merge", "--no-ff", "--no-edit", src)
	if mergeErr != nil {
		_, _ = git(wt, "merge", "--abort")
		return fmt.Errorf("%s does not merge into the batch: %s", branch, firstLine(out))
	}
	return nil
}

// DropRef removes an ephemeral ref and its worktree. Best effort: a ref left
// behind costs disk, and failing a landed batch over cleanup would be worse
// than the litter.
func (r repoGit) DropRef(ref string) error {
	defer workspace.LockRepo(r.ws)()

	wt := r.worktreePath(ref)
	_, _ = git(r.dir(), "worktree", "remove", "--force", wt)
	_, _ = git(r.dir(), "branch", "-D", ref)
	return nil
}

// refWorktree returns a checkout of ref, creating it if needed.
func (r repoGit) refWorktree(ref string) (string, error) {
	wt := r.worktreePath(ref)
	if out, err := git(r.dir(), "worktree", "add", "--force", wt, ref); err != nil {
		// Already there from an earlier member of this same batch, which is
		// the common case: every member after the first reuses it.
		if _, e := git(wt, "rev-parse", "--git-dir"); e != nil {
			return "", fmt.Errorf("checking out %s: %w\n%s", ref, err, out)
		}
	}
	return wt, nil
}

func (r repoGit) worktreePath(ref string) string {
	return filepath.Join(r.ws.Dir, "worktrees", strings.ReplaceAll(ref, "/", "-"))
}

// PushRef publishes the assembled ref so CI can run against it.
//
// Batch CI needs somewhere the forge will actually build, and that is a real
// branch. It is force-pushed because the ref is rebuilt from scratch on every
// pass and carries no history anyone else is tracking -- and deleted again
// when the batch is done, so no long-lived branch accumulates.
func (r repoGit) PushRef(ref string) error {
	defer workspace.LockRepo(r.ws)()
	if out, err := git(r.dir(), "push", "--force", "origin", ref+":"+ref); err != nil {
		return fmt.Errorf("publishing %s for CI: %w\n%s", ref, err, out)
	}
	return nil
}

// DeleteRemoteRef removes the published ref. Best effort, same reasoning as
// DropRef.
func (r repoGit) DeleteRemoteRef(ref string) error {
	defer workspace.LockRepo(r.ws)()
	_, _ = git(r.dir(), "push", "origin", "--delete", ref)
	return nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(b)), err
}
