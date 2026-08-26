package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Per-job git worktrees.
//
// One sandbox clone per repository, one worktree per job. The alternative,
// reusing a single checkout and resetting it between jobs, is what loses
// work: a reset discards anything the previous run left uncommitted, and an
// agent that was killed on the wall clock is exactly the case where those
// changes are worth keeping.
//
// A worktree is a separate directory with its own branch and its own index.
// Nothing a later job does can touch an earlier one, so "do not lose work"
// becomes a property of the layout rather than a rule someone must remember.

// Job is one unit of work in its own worktree.
type Job struct {
	Key    string // the tracker issue, when there is one
	Branch string
	Path   string
}

// AddWorktree creates a worktree for a job, on a branch that is guaranteed
// not to exist yet.
//
// Uniqueness is the point. Reusing orion/fcia-6 across two runs would put
// the second run's commits on top of the first's, so a failed attempt and
// its retry become one indistinguishable branch -- and if the first attempt
// is still open as a pull request, the retry silently rewrites what a
// reviewer is looking at. A suffix costs nothing and keeps attempts separate.
func AddWorktree(ws *Workspace, base, desired string) (*Job, error) {
	repo := filepath.Join(ws.Dir, "repo")
	if _, err := os.Stat(repo); err != nil {
		return nil, fmt.Errorf("no sandbox clone at %s: run orion init in the repository first", repo)
	}

	// Work from the freshest base. A sandbox created days ago is behind, and
	// branching from a stale develop produces a pull request full of other
	// people's changes.
	if out, err := git(repo, "fetch", "--prune", "origin"); err != nil {
		return nil, fmt.Errorf("fetching before branching: %w\n%s", err, out)
	}
	baseRef := "origin/" + base
	if _, err := git(repo, "rev-parse", "--verify", "--quiet", baseRef); err != nil {
		// No remote branch of that name: fall back to the local one rather
		// than failing, so a repo whose work branch is not yet pushed works.
		baseRef = base
		if _, err := git(repo, "rev-parse", "--verify", "--quiet", baseRef); err != nil {
			return nil, fmt.Errorf("base branch %q does not exist locally or on origin", base)
		}
	}

	branch, err := uniqueBranch(repo, desired)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(ws.Dir, "worktrees", strings.ReplaceAll(branch, "/", "-"))
	if out, err := git(repo, "worktree", "add", "-b", branch, path, baseRef); err != nil {
		return nil, fmt.Errorf("creating worktree %s: %w\n%s", path, err, out)
	}
	return &Job{Branch: branch, Path: path}, nil
}

// uniqueBranch returns desired, or desired-2, -3 ... for the first name that
// exists neither locally nor on the remote.
//
// Checking the REMOTE matters as much as the local repo: a previous run's
// branch may have been pushed and then its worktree pruned, so a local-only
// check would happily reuse a name that already has an open pull request.
func uniqueBranch(repo, desired string) (string, error) {
	exists := func(name string) bool {
		if _, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return true
		}
		_, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name)
		return err == nil
	}
	if !exists(desired) {
		return desired, nil
	}
	for n := 2; n < 100; n++ {
		candidate := fmt.Sprintf("%s-%d", desired, n)
		if !exists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free branch name near %q after 99 attempts; "+
		"something is wrong, and guessing further would only add noise", desired)
}

// ListWorktrees reports the job checkouts under a workspace, excluding the
// shared sandbox clone itself.
//
// Paths are compared RESOLVED, not as strings. git reports the real path,
// and on macOS /tmp is a symlink to /private/tmp -- so a plain comparison
// fails to recognise the clone and reports it as a job. A caller that then
// tidied up "finished jobs" would try to delete the sandbox every project
// depends on.
func ListWorktrees(ws *Workspace) ([]Job, error) {
	repo := filepath.Join(ws.Dir, "repo")
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	main := canonPath(repo)

	var jobs []Job
	var cur Job
	keep := func(j Job) {
		if j.Path != "" && canonPath(j.Path) != main {
			jobs = append(jobs, j)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			keep(cur)
			cur = Job{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	keep(cur)
	return jobs, nil
}

// canonPath canonicalises a path for comparison, falling back to the input
// when it cannot be resolved: an unreadable path is still worth comparing
// literally rather than treating as empty, which would match everything.
func canonPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// Dirty reports uncommitted changes in a worktree.
//
// Used before removing one. A killed run leaves work in progress, and that
// is precisely the state worth preserving rather than discarding.
func Dirty(path string) (bool, string) {
	out, err := git(path, "status", "--porcelain")
	if err != nil {
		return true, out // unreadable: assume dirty rather than risk deleting
	}
	return strings.TrimSpace(out) != "", out
}

// RemoveWorktree deletes a job checkout, refusing while it holds work that
// exists nowhere else.
//
// Never force. `git worktree remove --force` on a dirty tree destroys the
// only copy of whatever the agent had produced, and an agent stopped mid-run
// is the common case, not the rare one.
func RemoveWorktree(ws *Workspace, path string, force bool) error {
	if !force {
		if dirty, detail := Dirty(path); dirty {
			return fmt.Errorf("%s has uncommitted work:\n%s\n"+
				"  Commit it on its branch, or re-run with --force to discard it", path, detail)
		}
		if unmerged, n := hasUnpushedCommits(path); unmerged {
			return fmt.Errorf("%s has %d commit(s) that are not on the remote.\n"+
				"  Push the branch, or re-run with --force to discard them", path, n)
		}
	}
	repo := filepath.Join(ws.Dir, "repo")
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	if out, err := git(repo, args...); err != nil {
		return fmt.Errorf("removing worktree: %w\n%s", err, out)
	}
	return nil
}

// hasUnpushedCommits reports commits on this branch that exist on no remote.
//
// Asks the precise question -- "what is reachable from HEAD but from no
// remote ref" -- rather than diffing against a guessed base.
//
// The first version fell back to origin/HEAD..branch when a branch had no
// upstream, and origin/HEAD is the DEFAULT branch. A branch cut from develop
// therefore appeared to carry every commit develop has that main does not,
// so removing an untouched worktree was refused with "1 commit(s) that are
// not on the remote" for work that did not exist. A safety check that fires
// on nothing is worse than none: it teaches people to pass --force by habit,
// and then it is not there on the day it would have mattered.
func hasUnpushedCommits(path string) (bool, int) {
	out, err := git(path, "log", "--oneline", "HEAD", "--not", "--remotes")
	if err != nil {
		// Unreadable: assume there IS work rather than risk discarding it.
		return true, 0
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return false, 0
	}
	return true, len(strings.Split(out, "\n"))
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
