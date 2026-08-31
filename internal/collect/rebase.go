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
// commands, in the same order, with no decision taken at any point.
//
// Doing the rebase for EVERY branch at once was the mistake. The count then
// grew with the square of the queue -- the second ticket rebasing once, the
// third twice -- and at depth 2 that was already enough to starve the two
// longest-open branches out of their rebase allowance in a single evening
// (OR-206). The rebase was never the problem; performing it speculatively, on
// branches that were going to be invalidated again before their checks
// finished, was. So one branch takes a turn per pass and the rest hold: see
// the landing queue below.
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
// The one place the choice is made, so the three outcomes cannot drift: rebase
// it, hold it until its turn comes, or hand it over with the commands exactly
// as before.
//
// pass is every ticket this poll is reconciling, and is what the landing queue
// elects a leader from -- Orion is the thing sequencing here, so the ordering
// is decided in Orion rather than delegated to the forge (docs/decisions/0011).
func behind(res Result, key string, pass []string, pr PR, branch string, cfg config.Config,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	// Same source as the caller used to decide we are here (OR-112): the
	// pull request's own base first, config only as the fallback. Nothing
	// downstream chooses a branch by what it is called.
	base, named := baseOf(pr, cfg)

	// Every path below that hands the branch to a person gives up its place
	// in the landing queue first. A ticket waiting for a turn it will never
	// take is a ticket every branch behind it waits for too.
	leave := func() {
		if !opts.DryRun {
			_ = leaveQueue(ws.Dir, key)
		}
	}

	// A person working this branch by hand always wins. Checked first and
	// outside the switch below so it short-circuits before the rebase count
	// or anything else is even read (OR-130).
	if dir := worktreeOrRepo(ws, branch); manuallyLocked(dir) {
		leave()
		ui.Say(w, key, events.ActorOrion, ui.VerbWaiting,
			"%s is locked for manual work (%s); leaving it alone", branch, manualLockName)
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: "skipped auto-rebase: " + manualLockName + " present"})
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	}

	// Everything below rewrites a branch, so every reason not to is checked
	// before anything is touched.
	switch {
	case !named:
		// Can't rebase onto a base we couldn't determine -- report instead
		// of guessing.
		leave()
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	case !cfg.Collect.AutoRebase:
		leave()
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	case pr.Head == "":
		// Without the commit the forge reported, there is no lease to push
		// under, and a force-push with no lease is the thing this must never
		// be. Hand it over instead.
		leave()
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	}

	reqs := loadRequests(ws.Dir)
	if n := reqs.Rebases[key]; n >= maxAutoRebases {
		// Said once, at this commit, and then quietly. A branch already
		// handed over is not news on the next poll, and repeating the whole
		// escalation every two minutes -- three identical pairs in one log,
		// for branches nobody had touched -- teaches the reader to skim
		// exactly the block that was meant to get their attention (OR-206).
		// stale() below goes quiet on the same fact.
		if reqs.Conflicts[key] != pr.Head {
			ui.Warn(w, "%s: %s has been rebased %d times already and is behind %s again, "+
				"so the queue is moving faster than it can land; leaving this one to you",
				key, branch, n, base)
			log.Emit(events.Event{Kind: events.KindEscalate, Actor: events.ActorOrion,
				Msg: fmt.Sprintf("rebased automatically %d times and still behind %s", n, base)})
		}
		leave()
		return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
	}

	// The landing queue (OR-206).
	//
	// This branch is behind, clean, and Orion could rebase it right now --
	// and so, after one merge, is every other open branch. Rebasing them all
	// is what produced the quadratic: each one force-pushes, each one waits
	// out a full CI run, and the next merge lands before most of them finish,
	// so they pay again. One branch takes the turn and the others hold, which
	// makes the cost linear and, because the turn goes to whoever has been
	// behind longest, means waiting is what earns it rather than what costs
	// it. Holding is free: nothing is pushed, no allowance is spent, and the
	// ticket keeps ci-wait so the next pass simply asks again.
	if joinQueue(reqs, key, deps.Now()) && !opts.DryRun {
		if err := writeRequests(ws.Dir, reqs); err != nil {
			// Not fatal: an unrecorded place means this branch competes on
			// equal terms next pass rather than losing its seniority, which
			// is the harmless direction to fail in.
			ui.Warn(w, "%s: could not record its place in the queue (%v)", key, err)
		}
	}
	if next := leader(reqs, pass); next != key {
		res.Verdict = VerdictStale
		ui.Say(w, key, events.ActorOrion, ui.VerbWaiting,
			"%s is behind %s and holding its turn; %s has been waiting longer",
			branch, base, next)
		return res
	}

	if opts.DryRun {
		res.Verdict = VerdictStale
		ui.Ok(w, "would", "%s: rebase %s onto %s and force-push it with a lease", key, branch, base)
		return res
	}

	dir := worktreeOrRepo(ws, branch)
	// A rebase in a job worktree is still git against the SHARED clone: it
	// fetches, rewrites refs and force-pushes through the one object store
	// every other job's worktree hangs off. With tickets running concurrently
	// this can land while another job is creating or removing a worktree, so
	// it takes the same lock they do (see workspace/gitlock.go).
	unlock := workspace.LockRepo(ws)
	err := rebaseOnto(dir, base, branch, pr.Head)
	unlock()
	if err != nil {
		// Loud ONCE, and then exactly the old behaviour. The branch is
		// unchanged, so the three commands are still the right ones to print.
		//
		// Once per HEAD, like every other hand-over on this path. Said on every
		// poll it read as fifteen identical notes in fifteen minutes on OR-217,
		// which is how a reader learns to skim the block that was meant to get
		// their attention -- the same fault the cap escalation above and stale()
		// below were already fixed for (OR-206, OR-232).
		//
		// And it names WHY the worktree is dirty when a breaker parked it. "has
		// uncommitted changes" is a true report of a downstream symptom; it sent
		// the OR-217 operator looking for a person who had left work in a
		// worktree, when what had happened was that a breaker stopped the run
		// and the command to resume it was sitting in a file they had no reason
		// to open.
		if reqs.Conflicts[key] != pr.Head {
			note := ""
			if errors.Is(err, errDirtyWorktree) {
				note = parkedNote(dir, cfg)
			}
			ui.Warn(w, "%s: could not rebase %s automatically (%v)%s", key, branch, err, note)
			log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
				Msg: "automatic rebase did not run: " + err.Error() + note})
		}
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
	if head == "" {
		return errors.New("not enough is known about the branch to rebase it")
	}
	if err := rebaseLocal(dir, base, branch); err != nil {
		return err
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

// errRebaseConflict marks the one failure a person has to resolve.
//
// Every other way rebaseLocal can refuse -- an unreachable remote, a dirty
// worktree, the wrong branch checked out -- is a circumstance, and the caller
// carries on without the rebase. A conflict is a decision, and the caller has
// to say so and print the commands. The two are told apart here rather than by
// matching on the error text, which would break the first time git reworded
// itself.
var errRebaseConflict = errors.New("the rebase does not apply cleanly")

// errDirtyWorktree marks the refusal whose CAUSE is worth chasing.
//
// The other circumstances rebaseLocal declines on -- an unreachable remote, the
// wrong branch checked out -- are about the git it just ran. A tree with
// uncommitted tracked changes is about something that happened to the RUN, and
// on OR-217 that something was a breaker trip whose recovery command existed in
// three places, none of them the log the operator was reading (OR-232). Told
// apart here rather than by matching on the error text, for the same reason
// errRebaseConflict is: the text is a sentence, not an interface.
var errDirtyWorktree = errors.New("uncommitted changes")

// rebaseLocal replays branch onto the fetched tip of its base, touching
// nothing on the remote, or changes nothing at all.
//
// The local half of rebaseOnto, split out so the pre-push path (OR-227) can
// replay a branch that has never been pushed and therefore has no lease to
// push under. One rebase, two callers: a second implementation would be a
// second set of refusals to keep in step.
//
// Every step is refused rather than forced when its precondition does not
// hold. A dirty worktree means somebody is working in it; a branch that is
// not the one checked out means this is not the directory that owns it; a
// rebase that stops is aborted, restoring the branch exactly.
func rebaseLocal(dir, base, branch string) error {
	if dir == "" || base == "" || branch == "" {
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
		return fmt.Errorf("%s has %w", dir, errDirtyWorktree)
	}
	// Proved before the rebase is attempted so that a base git cannot resolve
	// reports itself as what it is. Without this, "invalid upstream" comes
	// back through the same door as a real overlap and a person is told to
	// resolve a conflict that does not exist.
	if err := gitQuiet(dir, "rev-parse", "--verify", "--quiet", "origin/"+base); err != nil {
		return fmt.Errorf("origin/%s is not a branch this clone knows", base)
	}

	if err := gitQuiet(dir, "rebase", "origin/"+base); err != nil {
		// A stopped rebase leaves the worktree mid-operation, which is the
		// one state a person must never be handed by a tool that was trying
		// to help. Abort puts the branch back exactly where it was.
		_ = gitQuiet(dir, "rebase", "--abort")
		return fmt.Errorf("replaying %s onto origin/%s: %w: %v",
			branch, base, errRebaseConflict, err)
	}
	return nil
}

// gitLine runs git and returns its trimmed output.
func gitLine(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
