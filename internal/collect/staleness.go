package collect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Is the verdict Orion is about to merge on still about the code that exists?
//
// A passing check is evidence about ONE commit. When the base moves after CI
// runs, that evidence stops describing what the merge would produce, and
// nothing in the pipeline notices: the check stays green, the pull request
// stays mergeable, and the merge ships a combination no test has ever seen.
//
// This is not hypothetical. FCIA-8 and FCIA-10 were worked in parallel, both
// cut from develop before either merged, and both created src/fcia/cli.py
// from scratch -- one registering `fcia eval`, the other `fcia report`.
// FCIA-8 merged; FCIA-10 conflicted and a human resolved it.
//
// That conflict was LUCK. Git objected only because both touched the same
// path. Had FCIA-8 put its subcommand in any other file, both would have
// merged clean, both CI runs green, and develop would have ended up with one
// cli.py silently missing a command.
//
// GitHub can enforce this ("require branches to be up to date"), and cannot
// be relied on to: branch protection is unavailable for private repositories
// on the free plan, which is every repository Orion currently runs on --
//
//	$ gh api repos/OWNER/REPO/branches/develop/protection
//	{"message":"Upgrade to GitHub Pro or make this repository public ...",
//	 "status":"403"}
//
// and with protection off, gh reports mergeStateStatus CLEAN for a stale
// branch, so even the detection half is missing. One local git command has
// none of those dependencies.

// upToDate reports whether the base's tip is already contained in the branch.
//
// `git merge-base --is-ancestor A B` exits 0 when A is an ancestor of B. With
// A as the base tip, exit 0 means CI ran against current code.
//
// The THIRD return value is whether the question could be answered at all.
// An unfetched remote, a missing branch, a repository in a state git will not
// discuss -- none of those mean the branch is stale, and treating them as
// stale would block every merge in a repository this code failed to read.
// Unknown means proceed: this gate exists to catch a specific, detectable
// situation, not to be the last word on whether a merge is wise.
func upToDate(dir, base, branch string) (ok bool, known bool) {
	if dir == "" || base == "" || branch == "" {
		return false, false
	}
	// Fetch first. The sandbox clone is shared across jobs and may be behind,
	// and answering from a stale remote-tracking ref is exactly the mistake
	// this function exists to prevent -- one level down.
	if err := gitQuiet(dir, "fetch", "--quiet", "origin"); err != nil {
		return false, false
	}
	baseRef := "origin/" + base
	branchRef := "origin/" + branch
	for _, ref := range []string{baseRef, branchRef} {
		if err := gitQuiet(dir, "rev-parse", "--verify", "--quiet", ref); err != nil {
			return false, false
		}
	}
	if err := gitQuiet(dir, "merge-base", "--is-ancestor", baseRef, branchRef); err != nil {
		// Exit 1 is the honest answer "no". Anything else is git failing to
		// answer, and is reported as unknown rather than as staleness.
		if code, isExit := exitCode(err); isExit && code == 1 {
			return false, true
		}
		return false, false
	}
	return true, true
}

// staleBranch is the message shown when a merge is refused for this reason.
//
// Deliberately NOT the conflict wording. A conflict is git refusing; this is
// Orion refusing on evidence git would have accepted, and a person who reads
// "conflicts with its base" and then finds a cleanly mergeable branch will
// conclude the tool is confused.
func staleBranch(key, branch, base string) string {
	return key + ": " + branch + " is behind " + base + ", so its green checks were " +
		"produced against a base that has since moved. Rebasing re-runs them against " +
		"what would actually be merged."
}

func gitQuiet(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return cmd.Run()
}

func exitCode(err error) (int, bool) {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}

// worktreeOrRepo returns the directory to ask git in: the job's worktree when
// it still exists, else the sandbox clone.
func worktreeOrRepo(ws *workspace.Workspace, branch string) string {
	if ws == nil {
		return ""
	}
	if wt := jobTree(ws, branch); wt != "" {
		return wt
	}
	return ws.RepoDir()
}

// jobTree is a ticket's OWN checkout, or "" when it has none -- the same
// question worktreeOrRepo asks, without the fallback.
//
// The distinction matters wherever the answer is about this ticket rather
// than about somewhere convenient to run git: the shared clone's uncommitted
// files belong to whatever else is using it, so reading them as this
// ticket's residue would report somebody else's mess against this branch
// (see internal/collect/done.go).
func jobTree(ws *workspace.Workspace, branch string) string {
	if ws == nil {
		return ""
	}
	wt := filepath.Join(ws.Dir, "worktrees", strings.ReplaceAll(branch, "/", "-"))
	if _, err := os.Stat(wt); err != nil {
		return ""
	}
	return wt
}

// remindEvery is how often a branch already handed to a person is mentioned
// again while nothing about it changes.
//
// The hand-over itself is still announced the moment it happens, and a branch
// that MOVES is announced afresh -- neither is throttled, because both are
// news. This bounds only the "still yours" reminder, whose whole content is
// that nothing has happened.
//
// Fifteen minutes because that is the interval over which the fault was
// observed: OR-217 produced 15+ identical lines in 15 minutes at a one-minute
// poll, and one line in that window is the smallest change that turns the
// reminder back into something a reader looks at. Long enough to stop being
// wallpaper, short enough that a branch cannot go missing from the log.
const remindEvery = 15 * time.Minute

// stale reports a branch whose base has moved, and refuses the merge.
//
// Shaped exactly like conflicted(): announced once per HEAD, the ticket kept
// in ci-wait so a pushed rebase resumes the flow with no re-labelling, and
// the rebase commands given for the directory the branch is actually in.
//
// The difference is the wording. A conflict is git refusing; this is Orion
// refusing something git would happily do, and telling somebody their branch
// "conflicts" when it merges cleanly would make the tool look confused at the
// exact moment it is being most careful.
func stale(res Result, key string, pr PR, branch string, cfg config.Config,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	// baseOf, not cfg.VCS.WorkBranch directly: the conflict path prints a
	// rebase command too, and the two must never name different branches for
	// the same pull request (OR-112).
	base, _ := baseOf(pr, cfg)
	res.Verdict = VerdictStale

	if opts.DryRun {
		ui.Ok(w, "would", "%s: refuse to merge %s until it is rebased on %s", key, branch, base)
		return res
	}

	// Once per HEAD, sharing the conflict store: both are "this branch needs
	// a human to move it", both clear the same way, and two stores would
	// drift on the one question they both answer.
	//
	// Checked BEFORE anything is printed, not only before the tracker is
	// written (OR-206). Handing a branch over is a single event, and the
	// terminal was repeating the whole of it -- the warning and the three
	// commands -- on every poll, for branches nobody had touched between
	// polls. Three identical pairs appeared in one log. A block that reprints
	// unchanged every two minutes reads as boilerplate, and the reader stops
	// seeing the one that is new. Said once, then a line to say it is still
	// true and still theirs.
	//
	// Quiet, and now also PERIODIC. Saying it once per poll made the reminder
	// itself the noise: fifteen identical lines in fifteen minutes on OR-217,
	// each reporting a symptom and none of them naming the breaker that had
	// parked the worktree or the command that would have released it (OR-232).
	// Fifteen copies of a line is not fifteen times the information, and a
	// reader who has learned to skip it will skip the one that changes.
	reqs := loadRequests(ws.Dir)
	if already := reqs.Conflicts[key]; already != "" && already == pr.Head {
		now := deps.Now()
		if last, seen := reqs.Reminded[key]; seen && now.Sub(last) < remindEvery {
			return res
		}
		reqs.Reminded[key] = now
		// Unrecorded means the reminder comes again next poll, which is the
		// behaviour that existed before this and is not worth a line of its own
		// -- a warning about failing to suppress a warning is two lines where
		// there was one.
		_ = writeRequests(ws.Dir, reqs)
		ui.Say(w, key, events.ActorOrion, ui.VerbWaiting,
			"%s is still behind %s and still yours; nothing has moved since it was reported%s",
			branch, base, parkedNote(worktreeOrRepo(ws, branch), cfg))
		return res
	}

	ui.Warn(w, "%s", staleBranch(key, branch, base))
	for _, line := range rebaseSteps(ws, branch, base) {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
	}

	log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorOrion,
		Msg: "branch is behind " + base + "; its checks describe a base that has moved"})

	_ = deps.Jira.Comment(key, actors.Comment(events.ActorOrion, fmt.Sprintf(
		"`%s` is behind `%s`, so the checks on it were produced against a base that "+
			"has since moved.\n\nThe pull request would still merge cleanly -- git has no "+
			"objection. The objection is that nothing has tested the combination. Another "+
			"ticket landed first, and two changes can each pass alone and fail together.\n\n"+
			"Rebase and push; CI re-runs against what would actually be merged, and Orion "+
			"continues from there.\n\n%s", branch, base, pr.URL)))

	if ch := channelOf(ws); ch != "" {
		tell(w, log, notify.Event{
			Key: key, Channel: ch, Level: notify.Blocked, Workspace: ws.ID,
			Title: key + " needs a rebase before it can merge",
			Body: fmt.Sprintf("`%s` is behind `%s`.\n\nIts checks are green, and they "+
				"describe a base that has moved. Git would merge this; nothing has tested "+
				"the result.\n\nRebase and push, and Orion carries on: %s", branch, base, pr.URL),
		})
	}

	if err := markConflict(ws.Dir, key, pr.Head); err != nil {
		ui.Warn(w, "%s: could not record it (%v); it will be reported again", key, err)
	}
	res.Changed = true
	return res
}
