package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
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
// Held under LockRepo end to end, not per git command. The fetch, the base
// lookup, the free-name search and the worktree add are ONE decision: two jobs
// starting at once would otherwise both be told orion/or-184 is free, and the
// second `worktree add -b` fails on a ref lock -- or, worse, succeeds after the
// first has moved on, and two runs share a branch.
func AddWorktree(ws *Workspace, base, desired string) (*Job, error) {
	defer LockRepo(ws)()

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

	// Regenerate the sandbox policy for every job, not only at adoption.
	//
	// The settings are Orion's, not the user's, and a sandbox adopted last
	// month otherwise keeps whatever policy that release generated -- so a
	// fix to the policy reaches nobody until they think to re-run
	// `orion init`, and the run that would benefit is the one that cannot
	// know it should. This is the point every job passes through, and it is
	// a file write.
	if err := writeSettings(ws); err != nil {
		return nil, fmt.Errorf("refreshing the sandbox settings: %w", err)
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
	defer LockRepo(ws)()

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
	// --untracked-files=all, because the default collapses a wholly untracked
	// directory to one entry -- "?? plans/" -- and an entry naming a directory
	// can be neither recognised as Orion's own file nor safely ignored: the
	// same line would cover an agent's draft sitting next to it. Expanding
	// costs a stat walk and hides nothing.
	out, err := git(path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return true, out // unreadable: assume dirty rather than risk deleting
	}
	lines := AgentDirt(path, out)
	return len(lines) > 0, strings.Join(lines, "\n")
}

// AgentDirt filters `git status --porcelain` output down to the lines a person
// or an agent produced, dropping the files Orion writes into a job worktree
// itself.
//
// Being conservative is correct for a deletion guard, but only about things
// somebody else produced. Orion's own artefacts are untracked by design -- so
// they can never be committed away -- and counting them as reasons to refuse
// makes Orion block its own cleanup with its own output:
//
//	WARNING kept the worktree for orion/or-168: ... has uncommitted work:
//	        ?? plans/BLOCKED.md
//
// for a ticket that had merged minutes earlier (OR-220). Every tripped run
// then leaves a full checkout behind forever.
//
// The list is deliberately by NAME. Ignoring untracked files in general is the
// rule that protects an agent's four new test files, and making these tracked
// instead would put them in the diff and the pull request.
func AgentDirt(worktree, porcelain string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(porcelain), "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		// Status lines are "XY path"; the path is what identifies the file,
		// and for a rename ("R  old -> new") the destination is what is on
		// disk to lose.
		f := strings.Fields(l)
		if len(f) >= 2 && orionAuthored(worktree, f[0], f[len(f)-1]) {
			continue
		}
		lines = append(lines, l)
	}
	return lines
}

// orionAuthored reports whether one porcelain entry names something Orion
// wrote rather than something to protect.
func orionAuthored(worktree, status, path string) bool {
	// UNTRACKED only, for every artefact below.
	//
	// Orion writes these files itself and nothing tracks them, so untracked is
	// the state they are always in. Once one is in the index it is a file
	// somebody deliberately committed to this repository -- git does not track
	// a file by accident -- and an uncommitted change to it is a tracked change
	// of exactly the kind the deletion guard exists to protect (OR-122). Being
	// Orion's by name is not the same as being Orion's to discard.
	if status != "??" {
		return false
	}
	// The runtime directory: state, logs, breaker counters.
	if strings.HasPrefix(path, ".orion/") {
		return true
	}
	// The breaker's stop-note (OR-194), at whatever plans path this repository
	// configures.
	plans := config.Load(worktree).Paths.Plans
	return path == filepath.ToSlash(filepath.Join(plans, "BLOCKED.md"))
}

// DirtyTracked reports uncommitted changes to TRACKED files, as porcelain
// lines, and the empty string when there are none.
//
// Deliberately narrower than Dirty. Dirty guards a deletion, so it counts
// anything a person or an agent produced; this answers a different question
// -- will the next rebase of this branch refuse -- and collect's rebaseOnto
// passes --untracked-files=no for the stated reason that git leaves
// untracked files alone across a rebase. Matching that exactly is the point:
// a check that flagged a stray build artefact would revert a worktree to fix
// a problem it does not have.
func DirtyTracked(path string) (string, error) {
	out, err := git(path, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "", fmt.Errorf("reading the state of %s: %w", path, err)
	}
	return strings.TrimSpace(out), nil
}

// CommitAll commits everything the worktree holds -- modified tracked files
// AND new untracked ones -- minus the excluded pathspecs, and reports how
// many files went in. Zero files means there was nothing to commit and no
// commit was made.
//
// It exists because the alternative to committing a stopped run's work is
// losing it. OR-189 and OR-191 both finished their implementation, both had
// it green, and both ended with every line uncommitted: 258 and 439 lines,
// recovered by hand. Untracked files are included deliberately -- both of
// those runs had four NEW files each, and `git commit -a` would have left
// exactly the new tests behind.
//
// The exclusions are pathspecs rather than a post-hoc `git reset`, so a file
// that must not be committed is never staged in the first place. The caller
// passes plans/BLOCKED.md: that note is the breaker's account of the trip,
// written for whoever opens the worktree next, and it does not belong in the
// branch's history.
func CommitAll(path, message string, exclude ...string) (int, error) {
	// Commit HERE or nowhere. git resolves a repository by walking upwards,
	// so `git -C <dir> add -A` in a directory that is not itself a checkout
	// stages files in whatever repository happens to be above it. The caller
	// is a breaker hook holding a path it was handed; that is not a mistake
	// to discover from the commit it made in somebody else's tree.
	top, err := git(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return 0, fmt.Errorf("%s is not a git worktree: %w", path, err)
	}
	if canonPath(strings.TrimSpace(top)) != canonPath(path) {
		return 0, fmt.Errorf("%s is not the root of its worktree (%s is)", path, strings.TrimSpace(top))
	}
	args := []string{"add", "-A", "--", "."}
	for _, e := range exclude {
		if e == "" {
			continue
		}
		// Skip an exclusion git already ignores. Naming a path inside an
		// ignored directory is not a no-op to git: `add` treats an explicit
		// pathspec for an ignored path as an error and refuses the WHOLE
		// invocation --
		//
		//   The following paths are ignored by one of your .gitignore files:
		//   .orion
		//   hint: Use -f if you really want to add them.
		//
		// so :(exclude).orion/state against a .gitignore holding ".orion/"
		// made every call here exit 1. That is exactly this repository, and
		// it meant CommitAll had NEVER succeeded in it: OR-233's residue
		// settle and OR-234's QA-test commit both shipped calling a function
		// that could not work, and OR-211 lost a run to the same error
		// reported as a mystery.
		//
		// The exclusion was always redundant for these paths -- git does not
		// stage an ignored file under `add -A -- .` anyway -- so dropping it
		// changes nothing about what gets committed and everything about
		// whether the commit happens.
		if _, err := git(path, "check-ignore", "-q", e); err == nil {
			continue // already ignored; naming it would fail the add
		}
		args = append(args, ":(exclude)"+e)
	}
	if _, err := git(path, args...); err != nil {
		return 0, fmt.Errorf("staging the work in %s: %w", path, err)
	}
	staged, err := git(path, "diff", "--cached", "--name-only")
	if err != nil {
		return 0, fmt.Errorf("reading the staged set in %s: %w", path, err)
	}
	staged = strings.TrimSpace(staged)
	if staged == "" {
		return 0, nil
	}
	n := len(strings.Split(staged, "\n"))
	if _, err := git(path, "commit", "-q", "-m", message); err != nil {
		return 0, fmt.Errorf("committing %d file(s) in %s: %w", n, path, err)
	}
	return n, nil
}

// RevertTracked discards uncommitted changes to tracked files, staged or not.
//
// `git checkout -- .` is not enough: a change that was staged and never
// committed survives it, and a worktree that still fails DirtyTracked after
// being cleaned is worse than one nobody tried to clean. Commits are
// untouched -- this resets to HEAD, not past it -- so work the agent DID
// commit is never what this destroys.
func RevertTracked(path string) error {
	if _, err := git(path, "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("reverting tracked changes in %s: %w", path, err)
	}
	return nil
}

// RemoveWorktree deletes a job checkout, refusing while it holds work that
// exists nowhere else.
//
// Never force. `git worktree remove --force` on a dirty tree destroys the
// only copy of whatever the agent had produced, and an agent stopped mid-run
// is the common case, not the rare one.
func RemoveWorktree(ws *Workspace, path string, force bool) error {
	return removeWorktree(ws, path, force, false)
}

// RemoveMergedWorktree deletes a job checkout the forge has already merged.
//
// Same as RemoveWorktree except the unpushed-commits guard does not apply.
// That guard asks "are these commits reachable from a remote ref", and a
// squash merge makes the answer no by construction: GitHub replays the branch
// as ONE new commit with a new SHA, then -- with delete_branch_on_merge set,
// which `orion init` sets -- deletes the remote branch, so the ref that would
// have covered them is gone too. Both signals read as "unpushed" for work the
// forge has fully accepted, and every squash-merged ticket left its worktree
// and branch behind with a warning about commits that were never at risk.
//
// This is the same class as the -d/-D distinction in collect.go's pruneBranch:
// ancestry cannot answer a question about a rebase or a squash. The caller
// already has the forge's MERGED verdict; re-deriving doubt from local
// ancestry only overrules a better source of truth.
//
// The dirty check stays. A pull request knows nothing about uncommitted work
// sitting in the checkout, so nothing has answered that question yet.
func RemoveMergedWorktree(ws *Workspace, path string) error {
	return removeWorktree(ws, path, false, true)
}

// Locked for the same reason AddWorktree is: the guards read the shared refs
// (`log --not --remotes`) and the removal rewrites .git/worktrees, so a
// removal running against a concurrent `worktree add` is two writers on one
// registry.
func removeWorktree(ws *Workspace, path string, force, merged bool) error {
	defer LockRepo(ws)()

	if !force {
		if dirty, detail := Dirty(path); dirty {
			return fmt.Errorf("%s has uncommitted work:\n%s\n"+
				"  Commit it on its branch, or re-run with --force to discard it", path, detail)
		}
		if unmerged, n := hasUnpushedCommits(path); unmerged && !merged {
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
	out, err := git(repo, args...)
	if err == nil {
		return nil
	}

	// git has its own untracked-files check, and it does not know that
	// .orion/ belongs to Orion. So a worktree this package has just declared
	// clean is refused by git for the one directory Orion wrote itself --
	// which is how a merged ticket ended up keeping its worktree forever.
	//
	// Retrying with --force is safe HERE and only here: the guards above have
	// already established there is no uncommitted work, and either nothing
	// unpushed or a forge that has the commits anyway, so the sole thing
	// --force can now discard is Orion's own scratch state.
	// The judgement is Orion's to make; git simply lacks the context.
	if !force && strings.Contains(out, "contains modified or untracked files") {
		if out2, err2 := git(repo, "worktree", "remove", "--force", path); err2 == nil {
			return nil
		} else {
			return fmt.Errorf("removing worktree: %w\n%s", err2, out2)
		}
	}
	return fmt.Errorf("removing worktree: %w\n%s", err, out)
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
