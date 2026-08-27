package collect

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
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
func conflicted(res Result, key string, pr PR, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	res.Verdict = VerdictConflicted

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
	for _, line := range rebaseSteps(ws, branch, baseOf(ws)) {
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

// baseOf names the branch this project merges into, for the hint above.
func baseOf(ws *workspace.Workspace) string {
	for _, b := range ws.Task.Branches {
		if b == "develop" {
			return b
		}
	}
	if len(ws.Task.Branches) > 0 {
		return ws.Task.Branches[len(ws.Task.Branches)-1]
	}
	return "main"
}
