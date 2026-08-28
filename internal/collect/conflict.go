package collect

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// A branch that no longer merges cleanly into its base.
//
// This is the ordinary consequence of running two tickets at once. FCIA-9's
// branch is cut from origin/develop BEFORE FCIA-8 lands, so once FCIA-8
// merges the base has moved and the two may overlap. Git catches it and
// refuses -- nothing is corrupted, nothing is overwritten, the branches were
// never able to touch each other's files.
//
// What Orion did with the refusal was the problem. It never asked gh for
// `mergeable`, so a conflict was indistinguishable from any other merge
// failure, and the recovery -- "leave the request in place so a later pass
// retries" -- retried an impossible merge every tick, forever, printing a
// failure each time and never once saying that a person had to rebase.
//
// A human is the only thing that can resolve this. So: say so, exactly once,
// and then wait quietly for them.
func conflicted(res Result, key string, pr PR, cfg config.Config, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	res.Verdict = VerdictConflicted
	base, _ := baseOf(pr, cfg)

	if opts.DryRun {
		ui.Ok(w, "would", "%s: report that %s conflicts with its base", key, branch)
		return res
	}

	// Once per HEAD, not once per tick.
	//
	// Keyed on the commit rather than on the ticket so that a rebase which
	// fails to resolve everything is reported again -- the situation really
	// did change, and a person who pushed a fix deserves to be told it did
	// not work. Re-announcing the SAME commit every two minutes would train
	// them to mute the channel, which loses every later message too.
	already := loadRequests(ws.Dir).Conflicts[key]
	fresh := already == "" || already != pr.Head

	ui.Warn(w, "%s: %s conflicts with its base; git cannot merge it", key, branch)
	fmt.Fprintf(w, "          %s\n", ui.Dim(w,
		"rebase it, push, and Orion picks it up again on the next pass:"))
	// Name WHERE. The branch is checked out in the job's worktree and is
	// almost never a local branch in the user's own clone -- so the obvious
	// reading of this hint, run from the repository they are standing in,
	// fails with "no such branch". A command that cannot be run where the
	// reader is standing is not an instruction, it is a riddle.
	for _, line := range rebaseSteps(ws, branch, base) {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
	}

	if !fresh {
		return res
	}

	log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorOrion,
		Msg: "branch conflicts with its base; a human must rebase"})

	_ = deps.Jira.Comment(key, actors.Comment(events.ActorOrion, fmt.Sprintf(
		"`%s` no longer merges cleanly into its base.\n\n"+
			"This usually means another ticket merged first and the two changes overlap. "+
			"Nothing was lost and nothing was overwritten -- the branch is intact.\n\n"+
			"Rebase it and push; Orion continues from there.\n\n%s", branch, pr.URL)))

	if ch := channelOf(ws); ch != "" {
		tell(w, log, notify.Event{
			Channel: ch, Level: notify.Blocked, Workspace: ws.ID,
			Title: key + " needs a rebase",
			Body: fmt.Sprintf("`%s` conflicts with its base, so git will not merge it.\n"+
				"Another ticket most likely landed first. Nothing was lost.\n\n"+
				"Rebase and push, and Orion carries on: %s", branch, pr.URL),
		})
	}

	if err := markConflict(ws.Dir, key, pr.Head); err != nil {
		// Not fatal, but it means the next pass says this again. Better a
		// repeated warning than a silent one.
		ui.Warn(w, "%s: could not record the conflict (%v); it will be reported again", key, err)
	}
	res.Changed = true
	return res
}

// rebaseSteps writes the commands that actually work, from the directory
// where the branch actually is.
//
// The worktree is checked for rather than assumed: after a merge it is
// pruned, and a conflict on a branch whose worktree is gone needs the fetch
// form instead. Printing `cd` into a directory that does not exist would be
// the same class of error this function was written to fix.
func rebaseSteps(ws *workspace.Workspace, branch, base string) []string {
	// No base, no command. A rebase onto a guess is the one outcome worse
	// than no instruction at all: it succeeds, quietly, onto the wrong
	// branch -- see baseOf below.
	if base == "" {
		return []string{
			"orion cannot tell what this branch is based on: the pull request",
			"did not report a base and vcs.work_branch is not set. Set it in",
			"orion.json, then rebase onto that branch and push.",
		}
	}
	dir := filepath.Join(ws.Dir, "worktrees", strings.ReplaceAll(branch, "/", "-"))
	if _, err := os.Stat(dir); err == nil {
		return []string{
			"cd " + dir,
			"git fetch origin && git rebase origin/" + base,
			"git push --force-with-lease",
		}
	}
	// No worktree: the branch lives only on the remote, so it has to be
	// fetched into a local one first. `git rebase <upstream> <branch>` on a
	// branch the clone does not have fails with a bare "no such branch",
	// which reads as the branch being lost rather than absent locally.
	return []string{
		"git fetch origin " + branch + ":" + branch,
		"git checkout " + branch + " && git rebase origin/" + base,
		"git push --force-with-lease",
	}
}

// baseOf is the ONE answer to "what is this branch based on".
//
// It used to be two answers. The staleness path read cfg.VCS.WorkBranch; this
// path scanned the workspace's branch list for one literally named "develop"
// and returned it. A repository with protected_branches ["main", "develop"]
// and work_branch main therefore got both, seconds apart in the same output:
// "behind main, rebase onto origin/main" followed by "conflicts with its
// base, rebase onto origin/develop" (OR-112).
//
// The develop branch still existed on that repository -- abandoned when the
// work branch moved to main -- so the wrong command did not fail. It
// succeeded, rebasing onto stale code, and buried the real change in a large
// unrelated diff on a pull request whose base was main all along. A wrong
// instruction that errors costs seconds; one that works costs an afternoon,
// and it is handed over at the moment somebody is least likely to question
// it: they have been told something is broken and are following steps.
//
// So: ask the forge. The base is a property of the pull request, and gh
// already reports baseRefName -- correct even for a PR opened by hand
// against something unexpected (same principle as OR-88). Config is the
// fallback for the no-PR case. Nothing is chosen for what it is CALLED, and
// when neither source answers, that is reported rather than guessed.
func baseOf(pr PR, cfg config.Config) (string, bool) {
	if pr.BaseRef != "" {
		return pr.BaseRef, true
	}
	if cfg.VCS.WorkBranch != "" {
		return cfg.VCS.WorkBranch, true
	}
	return "", false
}
