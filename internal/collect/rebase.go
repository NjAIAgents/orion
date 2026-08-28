package collect

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The rebase a person was being asked to perform, performed.
//
// require_up_to_date is what makes this necessary. Every merge invalidates
// every other open pull request, so on an evening with three tickets the
// stale warning fired four times -- and every instance was the same three
// commands, in the same order, with no decision taken at any point. The
// count grows with the square of the queue: the second ticket rebases once,
// the third twice.
//
// Both halves of the answer are already computed on the same pass. conflict.go
// asks the forge whether the branch merges cleanly; staleness.go asks git
// whether it is behind. BEHIND AND CLEAN is mechanical, and git has already
// confirmed the outcome -- so Orion does it, and the checks re-run against
// what would actually be merged. That is the whole point of the gate, and it
// is satisfied without involving anybody.
//
// BEHIND AND CONFLICTING is untouched: resolving an overlap is judgement,
// and judgement belongs to a person. The distinction worth holding is that a
// clean rebase does not CHOOSE anything. It has one possible result, and if
// it somehow fails the failure is loud and the branch is left exactly as it
// was.

// maxAutoRebases bounds consecutive automatic rebases for one ticket.
//
// A loop that rebases forever is worse than one that asks. Two matches the
// fix loop's ceiling for the same reason it has one: a ticket that has been
// rebased twice and is behind AGAIN is not in a situation more rebasing
// resolves -- it is in a queue moving faster than it can land, and the
// useful output at that point is a person's attention, not a third push.
const maxAutoRebases = 2

// behind decides what to do about a branch whose base has moved.
//
// The one place the choice is made, so the two outcomes cannot drift: rebase
// it, or hand it over with the commands exactly as before.
func behind(res Result, key string, pr PR, branch string, cfg config.Config,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	// Same source as the caller used to decide we are here (OR-112): the
	// pull request's own base first, config only as the fallback. Nothing
	// downstream chooses a branch by what it is called.
	base, named := baseOf(pr, cfg)

	// Everything below rewrites a branch, so every reason not to is checked
	// before anything is touched.
	switch {
	case !named:
		// Can't rebase onto a base we couldn't determine -- report instead
		// of guessing.
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	case !cfg.Collect.AutoRebase:
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	case pr.Head == "":
		// Without the commit the forge reported, there is no lease to push
		// under, and a force-push with no lease is the thing this must never
		// be. Hand it over instead.
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	}

	if n := loadRequests(ws.Dir).Rebases[key]; n >= maxAutoRebases {
		ui.Warn(w, "%s: %s has been rebased %d times already and is behind %s again, "+
			"so the queue is moving faster than it can land; leaving this one to you",
			key, branch, n, base)
		log.Emit(events.Event{Kind: events.KindEscalate, Actor: events.ActorOrion,
			Msg: fmt.Sprintf("rebased automatically %d times and still behind %s", n, base)})
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	}

	if opts.DryRun {
		res.Verdict = VerdictStale
		ui.Ok(w, "would", "%s: rebase %s onto %s and force-push it with a lease", key, branch, base)
		return res
	}

	dir := worktreeOrRepo(ws, branch)
	if err := rebaseOnto(dir, base, branch, pr.Head); err != nil {
		// Loud, and then exactly the old behaviour. The branch is unchanged,
		// so the three commands are still the right ones to print.
		ui.Warn(w, "%s: could not rebase %s automatically (%v)", key, branch, err)
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: "automatic rebase did not run: " + err.Error()})
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	}

	n, err := countRebase(ws.Dir, key)
	if err != nil {
		// The push has happened; failing to record it only means the bound is
		// looser than intended, which must not be reported as a failed rebase.
		ui.Warn(w, "%s: rebased, but the count could not be recorded (%v)", key, err)
	}
	// A branch that was announced as stuck is no longer stuck. Clearing it
	// means the NEXT genuine problem is announced rather than mistaken for
	// the one already reported.
	_ = clearConflict(ws.Dir, key)

	// Visible rather than mysterious: a branch whose commits changed under a
	// reviewer, with the actor that did it named.
	log.Emit(events.Event{Kind: events.KindPush, Actor: events.ActorOrion,
		Msg: fmt.Sprintf("rebased %s onto %s and force-pushed it (%d of %d); checks re-run "+
			"against what would actually be merged", branch, base, n, maxAutoRebases)})
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorOrion, fmt.Sprintf(
		"`%s` was behind `%s`, so its checks described a base that had moved. It merged "+
			"cleanly, which makes the rebase mechanical -- git had already confirmed the "+
			"result -- so Orion rebased it onto `%s` and force-pushed with a lease.\n\n"+
			"The checks are re-running against what would actually be merged. Nothing else "+
			"is required.\n\n%s", branch, base, base, pr.URL)))

	ui.Ok(w, "rebase", "%s: %s was behind %s; rebased and pushed, checks re-running", key, branch, base)

	// Back to waiting on CI, which is where it already was: the ticket keeps
	// its ci-wait label and the next pass reads the new run's verdict.
	res.Verdict = VerdictPending
	res.Changed = true
	return res
}

// rebaseOnto replays branch onto its base and force-pushes it, or changes
// nothing at all.
//
// Every step is refused rather than forced when its precondition does not
// hold. A dirty worktree means somebody is working in it; a branch that is
// not the one checked out means this is not the directory that owns it; a
// rebase that stops is aborted, restoring the branch exactly. The caller
// prints the manual commands on any error, so a refusal here costs nothing
// beyond the automation not happening.
//
// head is the commit the forge reported, and is the lease: --force-with-lease
// with an EXPLICIT expected value, because the bare form compares against the
// remote-tracking ref, which the fetch two lines up has just updated. A human
// push that landed in between then aborts the push instead of being destroyed
// by it, which is the entire reason the lease is here.
func rebaseOnto(dir, base, branch, head string) error {
	if dir == "" || base == "" || branch == "" || head == "" {
		return errors.New("not enough is known about the branch to rebase it")
	}
	if err := gitQuiet(dir, "fetch", "--quiet", "origin"); err != nil {
		return fmt.Errorf("fetching origin: %w", err)
	}
	cur, err := gitLine(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("reading the checked-out branch in %s: %w", dir, err)
	}
	if cur != branch {
		return fmt.Errorf("%s has %s checked out, not %s", dir, cur, branch)
	}
	// An uncommitted change is somebody's work in progress, and a rebase
	// would carry it into a replay of commits it was never part of.
	// Untracked files are not that: git leaves them alone across a rebase,
	// and refusing on a stray build artefact would disable the automation on
	// most real worktrees.
	dirty, err := gitLine(dir, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fmt.Errorf("reading the state of %s: %w", dir, err)
	}
	if dirty != "" {
		return fmt.Errorf("%s has uncommitted changes", dir)
	}

	if err := gitQuiet(dir, "rebase", "origin/"+base); err != nil {
		// A stopped rebase leaves the worktree mid-operation, which is the
		// one state a person must never be handed by a tool that was trying
		// to help. Abort puts the branch back exactly where it was.
		_ = gitQuiet(dir, "rebase", "--abort")
		return fmt.Errorf("replaying %s onto origin/%s: %w", branch, base, err)
	}

	lease := "--force-with-lease=refs/heads/" + branch + ":" + head
	if err := gitQuiet(dir, "push", lease, "origin", "HEAD:refs/heads/"+branch); err != nil {
		// The lease held somebody else's push, or the push failed for its own
		// reasons. Either way the remote is untouched, so put the local branch
		// back too -- a half-applied rebase that only exists locally is a
		// worse thing to leave behind than the staleness this was fixing.
		_ = gitQuiet(dir, "reset", "--hard", "ORIG_HEAD")
		return fmt.Errorf("pushing the rebased %s (the lease on %s may have been broken "+
			"by a push that landed in between): %w", branch, head[:min(len(head), 8)], err)
	}
	return nil
}

// gitLine runs git and returns its trimmed output.
func gitLine(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
