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

	// merge lands the batch's pull request. The SAME seam the per-branch path
	// uses (collect.Deps.Merge), not a second one: merging is the only
	// irreversible action in this package and it keeps its single
	// implementation, whatever assembled the thing being merged.
	//
	// Nil disables landing, and LandRef says so rather than silently
	// reporting a batch as landed. A batch that cannot merge is a batch that
	// tested something for nothing, and the operator needs to know which.
	merge func(dir, branch, reason, strategy string) error

	// openPR publishes the batch for review and for CI.
	//
	// The ref needs a pull request for three separate reasons, and it is
	// worth naming all three because any one of them alone would look like it
	// could be worked around. CI: `ci.yml` builds `pull_request`, and a bare
	// push to an ephemeral ref matches no trigger. READING: prStatus asks
	// `gh pr view`, so with no pull request Orion cannot see its own green
	// run and waits out the deadline (observed twice on 2026-08-31).
	// REVIEW: with no per-ticket pull request, this is the only place a
	// person can read what is about to land.
	openPR func(dir, branch, title, body, base string) (string, error)
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
// THE REF'S OWN WORKTREE IS RELEASED FIRST, and that is not housekeeping
// (OR-261). MergeInto checks the ref out in a worktree to merge into, and git
// refuses to force-update a branch that any worktree holds:
//
//	fatal: cannot force update the branch 'orion/batch' used by worktree at ...
//
// A batch that parks to wait for CI returns before DropRef, so its worktree
// outlives the tick by design. Without this, the next batch hits that fatal
// forever -- observed on 2026-09-01, looping every minute overnight and
// landing nothing. This doc line used to claim the ref was "created DETACHED
// from any worktree, so nothing has it checked out"; MergeInto had made that
// false, and the sentence is now true because this makes it true rather than
// because it was never violated.
//
// Removing only the worktree for THIS ref, and only ever a batch ref: a job
// worktree belongs to a ticket that may still be running.
func (r repoGit) CutRef(ref, base string) error {
	defer workspace.LockRepo(r.ws)()

	_, _ = git(r.dir(), "worktree", "remove", "--force", r.worktreePath(ref))
	// Prune the administrative record too. A worktree directory deleted by
	// hand -- which is what an operator does when this wedges -- leaves git
	// still believing the branch is checked out, so the fatal survives the
	// cleanup that was supposed to fix it.
	_, _ = git(r.dir(), "worktree", "prune")

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

// SHAOf resolves a ref, branch or remote branch to its commit.
//
// origin FIRST, then local, which is the opposite of what reads naturally and
// is the point: the question this answers is "what would I be merging into",
// and that is the remote's state. A local branch can trail the remote by any
// amount and answering from it would compare the batch against a base nobody
// else has.
func (r repoGit) SHAOf(ref string) (string, error) {
	defer workspace.LockRepo(r.ws)()

	for _, cand := range []string{"origin/" + ref, ref} {
		if out, err := git(r.dir(), "rev-parse", "--verify", "--quiet", cand); err == nil {
			if sha := strings.TrimSpace(out); sha != "" {
				return sha, nil
			}
		}
	}
	return "", fmt.Errorf("%s resolves to no commit locally or on origin", ref)
}

// ContainsRef reports whether base already contains branch.
//
// Asked of ORIGIN, for the same reason SHAOf prefers it: the question is
// whether this work has already landed where everyone can see it, and a local
// base that trails the remote would answer no for something that merged an
// hour ago.
//
// Ancestry, not a squash-aware comparison. A squash-merged branch reads as
// NOT contained, which is deliberately the safe direction here: it would be
// offered to a batch, merge as an empty change, and land harmlessly. The
// opposite error -- treating unlanded work as already landed -- silently
// drops a ticket's commits. OR-88 is the same distinction, seen from the
// cleanup side.
func (r repoGit) ContainsRef(base, branch string) (bool, error) {
	defer workspace.LockRepo(r.ws)()

	baseRef, err := r.resolve(base)
	if err != nil {
		return false, err
	}
	branchRef, err := r.resolve(branch)
	if err != nil {
		return false, err
	}
	_, err = git(r.dir(), "merge-base", "--is-ancestor", branchRef, baseRef)
	return err == nil, nil
}

// resolve names the ref that actually exists, origin first.
func (r repoGit) resolve(ref string) (string, error) {
	for _, cand := range []string{"origin/" + ref, ref} {
		if _, err := git(r.dir(), "rev-parse", "--verify", "--quiet", cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%s exists neither locally nor on origin", ref)
}

// LandRef merges the tested ref into base through a pull request.
//
// THROUGH A PULL REQUEST, not a push to base. Three reasons, and the first is
// the one that bites: `develop` requires status checks by name, and a merge
// commit created locally carries none of its own, so a direct push is
// accepted only by bypassing the very protection that is supposed to be
// gating this. Second, the pull request is where the approval reaction lands.
// Third, it leaves a record of what merged and why, which a push does not.
//
// The ref is pushed first because a pull request needs a remote branch. The
// pull request is opened only if there is not one already: this is called on
// a ref that Test has usually just published and opened, and asking twice
// would either fail or leave a duplicate.
func (r repoGit) LandRef(ref, base string) (string, error) {
	if r.merge == nil {
		return "", fmt.Errorf(
			"the batch is green but nothing was wired to merge it; "+
				"merge %s into %s yourself, or report this as a bug", ref, base)
	}
	if err := r.PushRef(ref); err != nil {
		return "", err
	}
	if r.openPR != nil {
		title := fmt.Sprintf("batch: land %s into %s", ref, base)
		body := "Assembled and tested as one set by Orion (OR-253).\n\n" +
			"Every member was merged into this ref and the ref was tested once. " +
			"What CI proved here is what merges."
		// An existing pull request is the expected case, not a failure: Test
		// opened one to get the checks read. Reported and ignored.
		if _, err := r.openPR(r.dir(), ref, title, body, base); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return "", fmt.Errorf("opening the batch pull request: %w", err)
		}
	}
	if err := r.merge(r.dir(), ref, "batch validated as one set", ""); err != nil {
		return "", err
	}
	// Read AFTER the merge, so what is recorded is where base actually ended
	// up rather than where it was predicted to.
	return r.SHAOf(base)
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
