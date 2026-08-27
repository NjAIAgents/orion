// Package collect finishes what work started.
//
// `orion work` deliberately stops at the pull request. It pushes, opens the
// PR, marks the ticket orion-ci-wait and releases the job slot, because
// holding an agent's process open for the twenty minutes CI takes would mean
// one ticket at a time and a laptop that cannot be closed.
//
// That leaves a gap nothing crossed: the CI verdict arrives, the merge
// happens on GitHub, and Orion never hears about either. Tickets sat in
// orion-ci-wait forever, the user's own checkout stayed behind develop, and
// job worktrees accumulated for branches that had long since merged.
//
// This is the other half. It reads the state that lives OUTSIDE Orion -- the
// PR's checks, whether it merged -- and reconciles the tracker, the working
// copy and the sandbox to it.
//
// It is a poll, not a webhook, and deliberately so: a webhook needs a public
// endpoint and a secret to receive an event that this reconstructs from
// scratch in one API call. Polling is also idempotent, which matters more --
// running it twice must be indistinguishable from running it once, because
// a person will run it twice.
package collect

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Verdict is what the outside world says about a pull request.
type Verdict string

const (
	// VerdictPending: CI has not finished. The overwhelmingly common case,
	// and the one that must do nothing at all.
	VerdictPending Verdict = "pending"
	// VerdictPassing: checks green, PR still open. Waiting on a human.
	VerdictPassing Verdict = "passing"
	// VerdictFailing: checks red. The ticket comes out of ci-wait so it is
	// visible again, because nothing will fix it on its own.
	VerdictFailing Verdict = "failing"
	// VerdictMerged: it landed. Everything downstream follows from this.
	VerdictMerged Verdict = "merged"
	// VerdictClosed: closed without merging. A human decided against it,
	// and Orion must not treat that as a failure to retry.
	VerdictClosed Verdict = "closed"
	// VerdictUnknown: no pull request found for the branch.
	VerdictUnknown Verdict = "unknown"
	// VerdictStale: the branch merges cleanly but its checks were produced
	// against a base that has since moved, so they are not evidence about
	// what merging would produce.
	VerdictStale Verdict = "stale"
	// VerdictConflicted: the branch will not merge into its base without a
	// human resolving an overlap. Reported rather than retried: no amount of
	// polling resolves a conflict, and Orion must not rewrite a branch a
	// reviewer is already looking at.
	VerdictConflicted Verdict = "conflicted"
)

// PR is the state of one pull request, reduced to what a decision needs.
type PR struct {
	URL     string
	Verdict Verdict
	// Conflicted means git cannot merge this branch into its base without a
	// human resolving something. Separate from Verdict rather than a value
	// of it, because it is orthogonal: a conflicted pull request usually has
	// PASSING checks, which ran before the base moved underneath it.
	Conflicted bool
	// Head is the branch's current commit, used to notice when somebody has
	// pushed a rebase and the situation has changed.
	Head string
	// Detail is the human-readable why: which check failed, or why nothing
	// could be determined. Carried into the tracker comment, since a bare
	// "CI failed" sends the reader to GitHub to find out anything.
	Detail string
}

// Deps are the seams: everything that touches a network or a disk.
type Deps struct {
	Jira TrackerAPI
	// Status inspects the pull request for a branch. Injected because it
	// shells out to gh, which needs auth and a network.
	Status func(dir, branch string) (PR, error)
	// Refresh fast-forwards the user's own working copy.
	Refresh func(sourcePath, branch string) (string, error)
	// Prune removes the job worktree once its branch has merged.
	Prune func(ws *workspace.Workspace, branch string) error
	// Merge lands an approved pull request. Separate from everything else
	// because it is the only irreversible action in this package, and the
	// only one a person explicitly authorised.
	Merge func(dir, branch, reason, strategy string) error
	// Fix sends a CI failure back to an agent on the same branch and reports
	// whether it pushed anything. Nil disables the fix loop.
	Fix func(ws *workspace.Workspace, key, branch, failure string) (pushed bool, err error)
	// Slack reads approvals. Nil disables the approval path entirely, which
	// is the correct behaviour when the extra OAuth scopes are not granted:
	// Orion then reports that checks pass and waits for a human to merge.
	Slack SlackAPI
	Now   func() time.Time
}

// SlackAPI is what an approval needs from Slack: post the request, offer the
// affordances, read the answer, and name who gave it.
type SlackAPI interface {
	SlackReader
	PostTS(channel, text string) (string, error)
	React(channel, ts, emoji string)
	BotID() string
}

// TrackerAPI is the slice of the tracker this package needs.
type TrackerAPI interface {
	Search(jql string, maxResults int) ([]tracker.Issue, error)
	// Children returns an issue's sub-tasks. A tracker that cannot answer
	// returns an error, and the issue is treated as having none.
	Children(key string) ([]tracker.Issue, error)
	SetLabels(key string, add, remove []string) error
	TransitionTo(key, status string) error
	Comment(key, text string) error
}

// Options for one pass.
type Options struct {
	// Keys limits the pass to specific tickets. Empty means every ticket
	// currently in orion-ci-wait, across every registered project.
	Keys []string
	Out  io.Writer
	Home string
	// DryRun reports the verdicts and changes nothing.
	DryRun bool
	// NoPrune keeps merged worktrees. For anyone who wants the checkout
	// after the merge -- reviewing what actually shipped, most often.
	NoPrune bool
	// NoFix disables the CI fix loop for this pass without editing config.
	// For when you want to see the failure yourself before anything spends
	// money reacting to it.
	NoFix bool
	// AwaitApproval makes this pass WAIT for the approval reaction rather
	// than asking and returning. Zero means do not wait.
	//
	// Set only for a person running `orion collect` at a terminal, never by
	// `orion watch`. The distinction is the whole design: a watcher must
	// come straight back to reconcile everything else, so for it the two
	// passes are free -- the next tick is the second pass. But a person
	// running collect by hand is doing so precisely BECAUSE no watcher is
	// running, usually after a run failed midway. For them "asked in Slack"
	// followed by an exit means the approval they then give is read by
	// nobody, and the pipeline stalls with every indication of success.
	AwaitApproval time.Duration
	// Poll is how often to re-read the reaction while waiting. Zero uses a
	// sane default; a test sets it small.
	Poll time.Duration
	// Unattended says this pass is a watcher tick rather than somebody at a
	// terminal, so advice about what to run next is wrong: the next tick is
	// what runs next.
	//
	// Without this, `orion watch` told its own operator to "run `orion
	// collect FCIA-8` again to act on it (orion watch does this on its next
	// tick)" -- inside orion watch. Advice that names the command you are
	// already running teaches people the output is boilerplate.
	Unattended bool
}

// Result is one ticket's reconciliation.
type Result struct {
	Key     string
	Verdict Verdict
	PR      string
	Changed bool // whether anything was actually done
	Err     error
}

// Run reconciles every ticket awaiting CI.
func Run(opts Options, deps Deps) []Result {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Home == "" {
		opts.Home = workspace.Home()
	}
	w := opts.Out

	keys := opts.Keys
	if len(keys) == 0 {
		found, err := waiting(deps.Jira, opts.Home)
		if err != nil {
			ui.Fail(w, "%v", err)
			return []Result{{Err: err}}
		}
		keys = found
	}
	if len(keys) == 0 {
		fmt.Fprintln(w, "nothing is waiting on CI.")
		return nil
	}

	var out []Result
	for _, key := range keys {
		out = append(out, one(strings.ToUpper(strings.TrimSpace(key)), opts, deps))
	}
	return out
}

// waiting asks the tracker which tickets carry the ci-wait label.
//
// Scoped to the projects Orion actually knows about. An unscoped JQL would
// match a label someone applied by hand in an unrelated project, and the
// first thing this does with a match is transition its status.
func waiting(j TrackerAPI, home string) ([]string, error) {
	f, err := registry.Load(home)
	if err != nil {
		return nil, err
	}
	projects := f.Keys()
	if len(projects) == 0 {
		return nil, nil
	}
	jql := fmt.Sprintf("project IN (%s) AND labels = %q ORDER BY updated ASC",
		strings.Join(quoted(projects), ", "), tracker.LabelCIWait)
	issues, err := j.Search(jql, 50)
	if err != nil {
		return nil, fmt.Errorf("searching for tickets awaiting CI: %w", err)
	}
	var keys []string
	for _, is := range issues {
		keys = append(keys, is.Key)
	}
	return keys, nil
}

func quoted(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

func one(key string, opts Options, deps Deps) (res Result) {
	w := opts.Out
	res = Result{Key: key}

	entry, err := registry.Lookup(opts.Home, key)
	if err != nil {
		ui.Fail(w, "%v", err)
		res.Err = err
		return res
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		res.Err = err
		ui.Fail(w, "%s: %v", key, err)
		return res
	}
	// Freshen the sandbox BEFORE reading policy from it. Its orion.json is
	// the committed config, and a clone made days ago serves a days-old one
	// -- which looks exactly like a setting that does not work.
	cfg := config.Load(ws.RepoDir())
	if msg, syncErr := workspace.SyncSandbox(ws, cfg.VCS.WorkBranch); syncErr != nil {
		ui.Warn(w, "could not refresh the sandbox: %v", syncErr)
	} else if msg != "" {
		ui.Ok(w, "refresh", "%s", msg)
		cfg = config.Load(ws.RepoDir()) // re-read: the config may have moved
	}
	branch := branchFor(cfg.VCS.BranchPrefix, key)

	log, logErr := events.Open(events.Path(ws.Dir), events.Event{
		Project: registry.ProjectOf(key), Key: key,
		Run: fmt.Sprintf("%d", deps.Now().UnixNano()), Actor: events.ActorCI,
	})
	if logErr == nil {
		defer log.Close()
	}

	pr, err := deps.Status(ws.RepoDir(), branch)
	if err != nil {
		res.Err = err
		ui.Fail(w, "%s: could not read the pull request: %v", key, err)
		return res
	}
	res.Verdict, res.PR = pr.Verdict, pr.URL
	log.Emit(events.Event{Kind: events.KindCI, Actor: events.ActorCI,
		Msg: string(pr.Verdict) + ": " + firstLine(pr.Detail)})

	// A branch that cannot be merged is not a branch to ask about.
	//
	// Checked BEFORE the verdict switch, because a conflicted pull request is
	// usually also a PASSING one: its checks ran, and they ran against a base
	// that has since moved. Asking someone to approve it would be asking them
	// to authorise a merge git will refuse, and the old code then retried that
	// refusal every tick forever -- it never asked gh for `mergeable`, so it
	// could not tell a conflict from any other failure, and "leave the request
	// in place so a later pass retries" became an unbounded loop with no exit.
	//
	// Stays in ci-wait deliberately. The moment somebody rebases the branch
	// the conflict clears, CI re-runs, and the normal flow resumes without
	// anyone having to re-label anything.
	if pr.Conflicted {
		return conflicted(res, key, pr, branch, opts, deps, ws, log, w)
	}

	// A green check on a base that has moved is not evidence about the merge.
	//
	// Checked here, alongside the conflict gate, and for the same reason: a
	// stale branch is usually a PASSING one, so leaving it to the verdict
	// switch would ask a person to approve a merge nothing has tested.
	//
	// Only when the branch is genuinely behind AND git was able to say so.
	// An unreadable repository is not a stale branch, and refusing every
	// merge because a fetch failed would be a worse fault than the one this
	// prevents.
	if cfg.CI.RequireUpToDate {
		if ok, known := upToDate(worktreeOrRepo(ws, branch), cfg.VCS.WorkBranch, branch); known && !ok {
			return stale(res, key, pr, branch, cfg, opts, deps, ws, log, w)
		}
	}

	switch pr.Verdict {
	case VerdictPending:
		// The common case, and the whole reason this is safe to run on a
		// timer: it does nothing, says so, and costs one API call.
		ui.Ok(w, "ci-wait", "%s: checks still running", key)
		return res

	case VerdictPassing:
		// Green but unmerged. Orion still does not decide this: it either
		// asks a person in Slack and acts on their answer, or says the
		// checks pass and waits. What it never does is merge on its own
		// judgement -- a tool that approves its own work has removed the
		// review it exists to produce.
		if cfg.Slack.RequireApproval && deps.Slack != nil {
			return approvalFlow(res, key, pr, cfg, branch, opts, deps, ws, log, w)
		}
		ui.Ok(w, "ok", "%s: checks pass; waiting for you to merge %s", key, pr.URL)
		return res

	case VerdictFailing:
		return failing(res, key, pr, cfg, branch, opts, deps, ws, log, w)

	case VerdictClosed:
		// Closed without merging is a decision, not a fault. Take it out of
		// ci-wait so nothing polls it forever, and leave the status alone
		// for a human to set.
		if opts.DryRun {
			ui.Ok(w, "would", "%s: release it; the pull request was closed unmerged", key)
			return res
		}
		if err := deps.Jira.SetLabels(key, nil, []string{tracker.LabelCIWait}); err != nil {
			res.Err = err
			ui.Warn(w, "%s: could not clear the ci-wait label: %v", key, err)
			return res
		}
		res.Changed = true
		ui.Warn(w, "%s: the pull request was closed without merging; released from the queue", key)
		return res

	case VerdictMerged:
		return merged(res, key, pr, cfg, branch, opts, deps, ws, entry, log, w)
	}

	ui.Warn(w, "%s: no pull request found for %s.\n"+
		"  It may have been opened by hand under another branch, or never opened at all.", key, branch)
	return res
}

// failing takes the ticket out of ci-wait and makes it visible.
//
// Marked orion-failed rather than returned to the queue on purpose: the
// branch already exists with commits on it, so a re-queue would start a
// second agent from develop and produce a competing branch for the same
// ticket. A person decides whether to fix the branch or discard it.
func failing(res Result, key string, pr PR, cfg config.Config, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	// Try to fix it first, if that is switched on and there is budget left.
	// A red build is the one failure the agent that caused it is best placed
	// to resolve: the branch is its own, the failure is specific, and the
	// alternative is a person reading a CI log to relay it back by hand.
	if cfg.CI.AutoFix && deps.Fix != nil && !opts.NoFix {
		handled, r := tryFix(res, key, pr, cfg, branch, opts, deps, ws, log, w)
		if handled {
			return r
		}
		// Not handled, but the attempt may still have failed in a way worth
		// reporting. Carrying the error forward matters because the CLI
		// exits on it -- dropping it here made a fix run that died look
		// exactly like one that was never tried.
		res.Err = r.Err
	}

	if opts.DryRun {
		ui.Ok(w, "would", "%s: mark failed; %s", key, firstLine(pr.Detail))
		return res
	}
	if err := deps.Jira.SetLabels(key, []string{tracker.LabelFailed},
		[]string{tracker.LabelCIWait}); err != nil {
		res.Err = err
		ui.Warn(w, "%s: could not relabel: %v", key, err)
		return res
	}
	_ = deps.Jira.Comment(key, "CI failed on "+pr.URL+".\n\n"+pr.Detail+
		"\n\nThe branch is kept. Fix it there and push, or close the pull "+
		"request and re-queue the ticket to start again.")

	log.Emit(events.Event{Kind: events.KindFailed, Actor: events.ActorCI,
		Msg: "CI failed: " + firstLine(pr.Detail)})
	res.Changed = true

	title, body := msgCIFailed(key, pr)
	tell(w, log, notify.Event{
		Channel: channelOf(ws), Level: notify.Blocked, Workspace: ws.ID,
		Title: title, Body: mention(cfg) + body,
	})
	ui.Fail(w, "%s: CI failed. %s", key, firstLine(pr.Detail))
	return res
}

// merged is the good ending, and the only branch that changes several
// systems at once.
//
// Order matters. The tracker is updated FIRST, because it is the record
// everything else is reconciled against and the only step whose failure
// means this ticket gets processed again on the next pass. Refreshing the
// checkout and pruning the worktree are conveniences: if either fails, the
// ticket is still correctly closed and the user is told what was left.
func merged(res Result, key string, pr PR, cfg config.Config, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, entry *registry.Entry,
	log *events.Log, w io.Writer) Result {

	if opts.DryRun {
		ui.Ok(w, "would", "%s: close it, refresh %s, prune %s", key, entry.Source, branch)
		return res
	}

	// Clear EVERY label Orion owns, not just the one that brought us here.
	//
	// A ticket that failed earlier, was fixed, and then merged kept its
	// orion-failed label forever -- so `orion queue` reported it as "failed"
	// on the same line as its status, "Done". Orion contradicting itself in
	// one line is worse than either state alone, because the reader cannot
	// tell which half to believe.
	//
	// The ticket is finished. Nothing Orion tracked about it is true any more.
	if err := deps.Jira.SetLabels(key, nil,
		tracker.Managed(cfg.Tracker.QueueLabel)); err != nil {
		res.Err = err
		ui.Warn(w, "%s: merged, but its labels could not be cleared: %v", key, err)
		return res
	}
	// Best effort: a workflow without a Done transition must not turn a
	// successful merge into a failure.
	if err := deps.Jira.TransitionTo(key, "Done"); err != nil {
		ui.Warn(w, "%s: merged and released, but could not transition to Done: %v", key, err)
	}
	_ = deps.Jira.Comment(key, "Merged: "+pr.URL)
	closeChildren(key, pr.URL, deps, w)
	// A branch that went red and then merged is a mistake with its own
	// correction attached, which is the one shape a lesson can be built from
	// without an agent inferring anything. Read the history BEFORE it is
	// cleared below -- this is the only moment both halves exist.
	proposeLesson(key, pr, loadFixes(ws.Dir).States[key], entry.Source, ws, log, w)
	// Forget the fix history. A ticket reopened later must not start with
	// its attempts already spent.
	_ = clearFixes(ws.Dir, key)
	log.Emit(events.Event{Kind: events.KindMerge, Actor: events.ActorCI, Msg: "merged " + pr.URL})
	res.Changed = true
	ui.Ok(w, "ok", "%s merged  %s", key, pr.URL)

	// The user's own checkout. Until now nothing ever did this, so a merged
	// ticket left develop behind on the machine its owner works on -- and
	// the next `orion work` preflight refuses on a stale base.
	refreshed := ""
	if deps.Refresh != nil {
		msg, err := deps.Refresh(entry.Source, cfg.VCS.WorkBranch)
		switch {
		case err != nil:
			ui.Warn(w, "could not refresh %s: %v", entry.Source, err)
			refreshed = err.Error()
		default:
			refreshed = msg
			ui.Ok(w, "refresh", "%s", msg)
			log.Emit(events.Event{Kind: events.KindRefresh, Actor: events.ActorOrion, Msg: msg})
		}
	}

	// The worktree, the local branch, and the remote branch.
	//
	// Merged means the forge accepted the work, so the checkout holds
	// nothing the repository does not. Note that it does NOT mean the
	// branch's commits are reachable from the work branch: a rebase merge
	// replays them as new objects, leaving the originals unreachable. Prune
	// has to trust this verdict rather than re-derive it from ancestry,
	// which is exactly the bug that stranded every merged branch (OR-88).
	pruned := false
	if !opts.NoPrune && deps.Prune != nil {
		if err := deps.Prune(ws, branch); err != nil {
			// "no worktree" is not a failure to keep one -- there was
			// nothing there. Warning about keeping something that does not
			// exist is the kind of untrue warning people learn to ignore.
			if strings.Contains(err.Error(), "no worktree") {
				pruned = true
			} else {
				ui.Warn(w, "kept the worktree for %s: %v", branch, err)
			}
		} else {
			ui.Ok(w, "removed", "the worktree for %s", branch)
			pruned = true
		}
	}

	title, body := msgMerged(key, pr, entry.Source, pruned, refreshed,
		cfg.VCS.WorkBranch, cfg.VCS.DefaultBranch)
	tell(w, log, notify.Event{
		Channel: channelOf(ws), Level: notify.Info, Workspace: ws.ID,
		Title: title, Body: body,
	})
	return res
}

// lookupEntry re-resolves a project after a merge, for the paths that need
// the user's own checkout.
func lookupEntry(home, key string) (*registry.Entry, error) {
	return registry.Lookup(home, key)
}

func branchFor(prefix, key string) string {
	if prefix == "" {
		prefix = "orion/"
	}
	return prefix + strings.ToLower(key)
}

func channelOf(ws *workspace.Workspace) string {
	if ws != nil && ws.Task.Slack != nil {
		return ws.Task.Slack.ID
	}
	return ""
}

func tell(w io.Writer, log *events.Log, e notify.Event) {
	if e.Channel == "" {
		return
	}
	for _, err := range notify.Send(e) {
		ui.Warn(w, "%v", err)
		if log != nil {
			log.Emitf(events.KindNote, events.ActorOrion, "notification failed: %v", err)
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// closeChildren moves a merged story's sub-tasks to Done.
//
// The work of a sub-task landed in the story's pull request -- one branch,
// one PR, one merge -- so leaving the children open would report work as
// outstanding that is already on the trunk. A board that says a story is
// done while its tasks are open is a board people stop trusting.
//
// Entirely best-effort, and deliberately so. The merge has HAPPENED by the
// time this runs; the code is on develop. Turning a tracker hiccup into a
// failed collect would report a successful merge as a failure, which is the
// worse error by a distance. Every problem here is a warning.
//
// Only sub-tasks Orion can see, and only ones not already Done -- a task
// somebody closed by hand is left alone rather than re-transitioned.
func closeChildren(key, prURL string, deps Deps, w io.Writer) {
	kids, err := deps.Jira.Children(key)
	if err != nil {
		// Usually a tracker without a parent field, which is the ordinary
		// case for a project that does not decompose. Not worth a warning.
		return
	}
	kids = tracker.Workable(kids)
	if len(kids) == 0 {
		return
	}
	var closed []string
	for _, c := range kids {
		if err := deps.Jira.TransitionTo(c.Key, "Done"); err != nil {
			ui.Warn(w, "%s: merged, but %s could not be closed: %v", key, c.Key, err)
			continue
		}
		_ = deps.Jira.Comment(c.Key, "Delivered in "+key+" and merged: "+prURL)
		closed = append(closed, c.Key)
	}
	if len(closed) > 0 {
		ui.Ok(w, "closed", "%d sub-task(s) delivered by %s: %s",
			len(closed), key, strings.Join(closed, ", "))
	}
}
