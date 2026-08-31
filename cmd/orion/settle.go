package main

// `orion settle <KEY>` -- the supported way out of a stuck worktree.
//
// internal/work/residue.go settles a run's residue automatically when the run
// ends, and that is where the problem should normally be solved. This exists
// because "normally" is not always: a killed process, a full disk, or a bug in
// the automatic path all leave the same artefact behind, and the artefact is
// not inert. collect's rebaseOnto refuses a tree with uncommitted tracked
// changes, so one dirty worktree holds its branch out of the landing queue
// indefinitely and starves whatever is behind it.
//
// On OR-217 the only recovery available was to be talked through `cd`-ing into
// a hashed path under ORION_HOME and running `git commit` against an agent's
// branch. That is not a recovery a user of this tool can be expected to
// perform, and a failure mode whose fix is a git incantation is a failure mode
// with no fix (OR-233).
//
// It REFUSES rather than guesses. A worktree mid-merge, mid-rebase or holding
// unmerged paths is a state where committing everything records a
// half-resolution as if it were finished, and a detached HEAD is one where the
// commit lands on no branch at all. Each of those is reported with the command
// that resolves it, because a recovery tool that quietly does the wrong thing
// is worse than the manual path it replaces.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

const settleUsage = `orion settle <KEY> [--dry-run]

Settles the worktree Orion left for a ticket. Finds it by key -- you never
need its path -- reports what is blocking the branch, and commits it as an
unverified snapshot so ` + "`orion collect`" + ` can rebase the branch again.

Nothing is verified by this and nothing is pushed. It refuses, rather than
guessing, when the worktree is mid-merge, mid-rebase, holding unmerged paths
or on a detached HEAD.

Exits non-zero when it refused or could not finish.
`

func runSettle(args []string) {
	var key string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			key = strings.ToUpper(a)
			break
		}
	}
	if key == "" {
		fmt.Fprint(os.Stderr, settleUsage)
		os.Exit(64)
	}

	job, _, err := findJob(workspace.Home(), key)
	exitOn(err)
	// ListWorktrees reads git, which knows the path and the branch but has
	// never heard of a ticket. Carry the key the user typed, so what this
	// prints -- and what it writes into the commit -- names the ticket.
	job.Key = key

	os.Exit(settleJob(os.Stdout, job, hasFlag(args, "--dry-run")))
}

// settleJob reports what is holding a worktree and, unless it refuses,
// commits it. Returns the process exit code.
func settleJob(w io.Writer, job workspace.Job, dryRun bool) int {
	ui.Ok(w, "worktree", "%s", job.Branch)
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, job.Path))

	dirty, err := workspace.DirtyTracked(job.Path)
	if err != nil {
		ui.Warn(w, "could not read the worktree: %v", err)
		return 1
	}
	if dirty == "" {
		ui.Ok(w, "clean", "nothing is blocking %s; its worktree holds no uncommitted tracked changes", job.Branch)
		return 0
	}

	lines := strings.Split(dirty, "\n")
	ui.Warn(w, "%d uncommitted tracked file(s); every rebase of %s refuses while they are there",
		len(lines), job.Branch)
	for _, line := range lines {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
	}

	if why, fix := settleRefusal(job.Path, dirty); why != "" {
		ui.Warn(w, "refusing to settle: %s", why)
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, "Resolve it yourself, then run orion settle "+job.Key+" again:"))
		fmt.Fprintf(w, "            cd \"$(orion sandbox %s --path)\"\n", job.Key)
		fmt.Fprintf(w, "            %s\n", fix)
		return 1
	}

	if dryRun {
		ui.Ok(w, "would", "commit %d file(s) as an unverified snapshot on %s", len(lines), job.Branch)
		return 0
	}

	// The same exclusions the automatic path uses. plans/BLOCKED.md is the
	// breaker's account of a trip, written for whoever opens the worktree, and
	// .orion is Orion's own runtime directory: neither belongs in the branch's
	// history, and neither is what blocks the rebase.
	cfg := config.Load(job.Path)
	n, err := workspace.CommitAll(job.Path, msgOperatorSettle(job.Key),
		cfg.Paths.Plans+"/BLOCKED.md", cfg.Paths.State)
	if err != nil {
		ui.Warn(w, "could not commit it: %v", err)
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			"Nothing was changed and nothing was reverted; the work is still on disk."))
		return 1
	}
	ui.Ok(w, "settled", "committed %d file(s) as an unverified snapshot on %s", n, job.Branch)
	fmt.Fprintf(w, "          %s\n", ui.Dim(w,
		"Nothing here has been verified. It is not pushed; orion collect can rebase this branch now."))
	return 0
}

// settleRefusal names a worktree state a blanket commit would corrupt, with
// the command that resolves it, or empty when there is none.
//
// Read from the worktree itself rather than from any record of what Orion
// last did to it, for the reason this whole ticket exists: a flag describing
// the state is not the state.
func settleRefusal(path, porcelain string) (why, fix string) {
	// An unmerged path is the direct evidence, and it is checked first: a
	// conflict committed wholesale records the markers as if they were the
	// resolution, which is exactly the mistake `orion conflict verify` exists
	// to catch after the fact.
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[:2] {
		case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
			return "the worktree holds unmerged paths, and committing them would record the conflict as its resolution",
				"git status        # resolve each, or: git merge --abort"
		}
	}
	for _, op := range []struct{ file, name, fix string }{
		{"MERGE_HEAD", "a merge", "git merge --abort"},
		{"rebase-merge", "a rebase", "git rebase --abort"},
		{"rebase-apply", "a rebase", "git rebase --abort"},
		{"CHERRY_PICK_HEAD", "a cherry-pick", "git cherry-pick --abort"},
		{"REVERT_HEAD", "a revert", "git revert --abort"},
	} {
		// --git-path resolves against the LINKED worktree's git dir. A job
		// checkout's .git is a file pointing elsewhere, so looking for
		// .git/MERGE_HEAD by hand finds nothing and this would refuse nothing.
		out, err := gitIn(path, "rev-parse", "--git-path", op.file)
		if err != nil {
			continue
		}
		if _, err := os.Stat(strings.TrimSpace(out)); err == nil {
			return "this worktree is in the middle of " + op.name +
				", and settling it would commit a half-finished one", op.fix
		}
	}
	if _, err := gitIn(path, "symbolic-ref", "--quiet", "HEAD"); err != nil {
		return "HEAD is detached, so a commit here would land on no branch and collect would never see it",
			"git checkout <branch>"
	}
	return "", ""
}

// msgOperatorSettle is the commit message for work an operator settled by
// hand.
//
// Deliberately NOT the message internal/work writes for the same shape of
// commit. Both say the work is unverified, but whoever reads `git log` a month
// from now should be able to tell that a person had to intervene here, which
// is a fact about Orion rather than about the branch.
func msgOperatorSettle(key string) string {
	return fmt.Sprintf("wip: settle the work left uncommitted in this worktree\n\n"+
		"Committed by `orion settle %s`, because an uncommitted tree makes every\n"+
		"rebase of this branch refuse and holds it out of the landing queue.\n"+
		"NOTHING here has been verified -- the run that produced it did not commit\n"+
		"it itself. Review before merging.\n", key)
}
