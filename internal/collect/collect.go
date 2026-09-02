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

	"github.com/orion-sdlc/orion/internal/actors"
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
	// VerdictUnknown: no pull request found for the branch. Terminal, not
	// pending: taken out of ci-wait and left for a human rather than polled
	// forever, because nothing about the next tick makes a missing pull
	// request more findable than this one did (OR-173).
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
	// BaseRef is the branch the pull request targets, from the forge. For
	// anything about WHERE a merge went this outranks config: the PR's base
	// is what actually decided it, and a message built from config once
	// announced "on main" for a merge GitHub had put on develop (OR-118).
	BaseRef string
	// Detail is the human-readable why: which check failed, or why nothing
	// could be determined. Carried into the tracker comment, since a bare
	// "CI failed" sends the reader to GitHub to find out anything.
	Detail string
	// Checks is the rollup unflattened: one entry per check, kept as data
	// rather than folded into Detail's sentence.
	//
	// Detail answers "what should the ticket say"; this answers "what is the
	// screen showing right now", and a batch's whole point is that several
	// tickets share one run -- so the operator needs to see WHICH of the
	// three platforms is still going, not a count (OR-264).
	Checks []Check
}

// Check is one CI check and where it got to.
type Check struct {
	Name string
	// State is passed, failed or running. Three values rather than GitHub's
	// dozen conclusions: the display distinguishes what a person acts on,
	// and neutral, skipped and cancelled are all "not something to wait for".
	State CheckState
}

// CheckState is what became of one check.
type CheckState string

const (
	CheckPassed  CheckState = "passed"
	CheckFailed  CheckState = "failed"
	CheckRunning CheckState = "running"
)

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
	// OpenPR publishes a branch for review and for CI. Used by the batch
	// path, which has to open one for its assembled ref: `ci.yml` builds
	// pull requests rather than arbitrary pushed refs, and prStatus reads
	// checks through `gh pr view`, so a ref with no pull request is one whose
	// green run Orion cannot see (OR-253).
	//
	// Nil leaves the batch unable to publish itself, which LandRef reports
	// rather than working around.
	OpenPR func(dir, branch, title, body, base string) (string, error)
	// Fix sends a CI failure back to an agent on the same branch and reports
	// whether it pushed anything. Nil disables the fix loop.
	//
	// denied is non-nil when nothing was pushed because the sandbox itself
	// refused the agent's edit -- a different failure from the agent not
	// knowing what to change, and one no further attempt can fix (OR-174).
	//
	// Takes the event log so the fix run's activity is attributed and recorded
	// the same way every other supervised run's is (OR-176).
	Fix func(ws *workspace.Workspace, key, branch, failure string, log *events.Log) (pushed bool, summary string, denied *PolicyDenial, err error)
	// Judge puts ONE question to a model about a finished, green run: does
	// this diff do what the ticket asked for (OR-244)? It returns the reply
	// verbatim; internal/done parses it.
	//
	// Nil disables only the intent question, never the whole triage. The
	// mechanical checks -- did QA reach a verdict, is a test stranded in the
	// worktree, do the new tests survive -count=2 -- read evidence that
	// already exists, cost nothing and cannot hallucinate, so they are the
	// part that must not depend on a model being configured.
	Judge func(ws *workspace.Workspace, key, prompt string) (string, error)
	// Slack reads approvals. Nil disables the approval path entirely, which
	// is the correct behaviour when the extra OAuth scopes are not granted:
	// Orion then reports that checks pass and waits for a human to merge.
	Slack SlackAPI
	Now   func() time.Time
}

// SlackAPI is what an approval needs from Slack: post the request, offer the
// affordances, read the answer, and name who gave it.
//
// MemberID is the outward direction of the same question UserName answers
// inward. Asking needs it as much as reading does: the request has to tag the
// approver to notify them, and slack.merge_approvers holds a username or an
// email rather than the member id a mention is built from.
type SlackAPI interface {
	SlackReader
	PostTS(channel, text string) (string, error)
	React(channel, ts, emoji string)
	BotID() string
	MemberID(who string) (string, error)
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
		// A line before the search, not after it. The Jira client itself
		// times out at 20s, but a person who typed `orion collect` has no way
		// to tell "about to check" from "hung" until SOMETHING appears --
		// and this is the first network call the pass makes (OR-128).
		//
		// NOT on a watcher tick. There, this pair -- "checking..." then
		// "nothing is waiting on CI." -- printed every sixty seconds while
		// two agents worked hard on two tickets, and it is worse than
		// silence: it states that the system is idle when it is not. The
		// watcher answers OR-128's question differently now, with a live
		// region that says what each run is doing four times a second
		// (OR-240), so the tick has no need of a line that says nothing.
		if !opts.Unattended {
			fmt.Fprintln(w, "checking for tickets awaiting CI...")
		}
		found, err := waiting(deps.Jira, opts.Home)
		if err != nil {
			ui.Fail(w, "%v", err)
			return []Result{{Err: err}}
		}
		keys = found
	}
	if len(keys) == 0 {
		// Silence on a tick, deliberately: with nothing in flight and nothing
		// awaiting CI there is nothing to report, and a watcher left running
		// overnight must not bury the one line that matters under eight
		// hundred saying nothing happened.
		if !opts.Unattended {
			fmt.Fprintln(w, "nothing is waiting on CI.")
		}
		return nil
	}

	// Normalised once, up front, because the whole list is now an input and
	// not merely something to loop over: the landing queue elects a leader
	// from the tickets THIS pass is reconciling (OR-206), and a queue whose
	// members are spelled differently from the keys being processed elects
	// nobody and holds everybody.
	pass := make([]string, len(keys))
	for i, key := range keys {
		pass[i] = strings.ToUpper(strings.TrimSpace(key))
	}
	// Batch integration (OR-236), when the repository has turned it on.
	//
	// Checked here rather than inside one(): the per-branch path below is what
	// every repository uses today, and threading a flag through it would make
	// its behaviour depend on a feature that is not enabled. Off, this block
	// is a config read and nothing else changes.
	//
	// The config comes from the FIRST ticket's workspace. Every ticket in a
	// pass belongs to one registered project in practice, and a batch spanning
	// two repositories is not a thing that can be assembled anyway -- so
	// reading one and using it for the set is honest rather than convenient.
	if ws, log, w, cfg, ok := batchContext(pass, opts, deps); ok && cfg.Collect.BatchIntegration {
		if log != nil {
			defer log.Close()
		}
		if res := runBatch(pass, cfg, opts, deps, ws, log, w); res != nil {
			return res
		}
		// Nothing was assemblable -- no pull requests open yet, say. Fall
		// through rather than reporting an empty pass, so a ticket that the
		// per-branch path CAN say something about still gets said.
	}

	var out []Result
	for _, key := range pass {
		out = append(out, one(key, pass, opts, deps))
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
	jql := tracker.JQLAnd(
		tracker.JQLIn("project", projects...),
		tracker.JQLEq("labels", tracker.LabelCIWait),
	) + " ORDER BY updated ASC"
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

func one(key string, pass []string, opts Options, deps Deps) (res Result) {
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
	// Config comes from the USER'S checkout -- entry.Source, the path the
	// registry maps this project to -- never from the sandbox clone.
	//
	// The sandbox looks like the right source (its orion.json is committed
	// config) and is exactly the wrong one, because its position depends on
	// the value being loaded: read work_branch from a stale sandbox, sync
	// the sandbox to that stale branch, and the checkout that holds the old
	// config is now pinned to the branch the old config names. A work_branch
	// change in the user's repo was deferred indefinitely that way, and one
	// merge was announced as "on main" when the PR had merged into develop
	// (OR-118). The user's checkout is where a person edits orion.json, and
	// where they expect the edit to take effect on the next tick.
	cfg := config.Load(entry.Source)
	// Collect is the step that merges, so it is the last place a branch
	// model with no integration branch can be caught before agent work
	// lands on the release branch. Refuse rather than merge and report it.
	if vErr := cfg.Validate(); vErr != nil {
		ui.Fail(w, "%s: %v", key, vErr)
		res.Err = vErr
		return res
	}
	if waiver := cfg.ReleaseBranchWaiver(); waiver != "" {
		ui.Warn(w, "%s", waiver)
	}
	if msg, syncErr := workspace.SyncSandbox(ws, cfg.VCS.WorkBranch); syncErr != nil {
		ui.Warn(w, "could not refresh the sandbox: %v", syncErr)
	} else if msg != "" {
		ui.Ok(w, "refresh", "%s", msg)
	}
	// The branch a job actually used, not the one convention would predict.
	// AddWorktree may have suffixed it (orion/or-156-2) to keep a retry off a
	// prior attempt's still-open pull request, and recomputing the name here
	// desynchronised from that the moment any ticket was ever retried --
	// every tick after polled a branch that did not exist (OR-173).
	// branchFor is now only the fallback for a ticket no job ever recorded,
	// and callers are told when that happened.
	branch, recordedBranch := workspace.BranchOf(ws, key)
	if !recordedBranch {
		branch = branchFor(cfg.VCS.BranchPrefix, key)
	}

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
		// Out of the landing queue: a conflict is a person's, and a ticket
		// that cannot take its turn must not be the one everything else is
		// waiting for (OR-206).
		if !opts.DryRun {
			_ = leaveQueue(ws.Dir, key)
		}
		return conflicted(res, key, pr, cfg, branch, opts, deps, ws, log, w)
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
	//
	// Measured against the SAME base the conflict path names -- the pull
	// request's, config only as the fallback. Asking git whether the branch
	// is behind a branch it does not merge into answers a question nobody
	// asked (OR-112). Reached only when the branch merges cleanly -- the
	// conflict gate above has already returned otherwise -- so behind() has
	// both facts it needs: behind and clean is a mechanical rebase Orion
	// performs itself, behind and conflicting was handed to a person three
	// lines up.
	if cfg.CI.RequireUpToDate {
		if base, named := baseOf(pr, cfg); named {
			if ok, known := upToDate(worktreeOrRepo(ws, branch), base, branch); known && !ok {
				return behind(res, key, pass, pr, branch, cfg, opts, deps, ws, log, w)
			}
		}
	}
	// Not behind, so it is not waiting for a turn to be rebased. Given up
	// here rather than only on merge, because a branch that has stopped
	// waiting must stop being the branch everything else waits FOR: it can
	// no longer reach behind(), so nothing else would ever release it
	// (OR-206).
	if !opts.DryRun {
		_ = leaveQueue(ws.Dir, key)
	}

	switch pr.Verdict {
	case VerdictPending:
		// The common case, and the whole reason this is safe to run on a
		// timer: it does nothing, says so, and costs one API call.
		ui.Ok(w, "ci-wait", "%s: checks still running", key)
		return res

	case VerdictPassing:
		// Green is not done (OR-244). Before anybody is asked to approve
		// this, read the run against the diff -- three times on 2026-08-30 a
		// green pull request was evidence of nothing, and each time a person
		// caught it by reading rather than by looking at the status.
		//
		// Ahead of BOTH endings below, not only the Slack one: a repository
		// without approvals still offers the branch to a human to merge, and
		// "checks pass" is exactly the sentence this pass exists to qualify.
		if ok, r := triageDone(res, key, pr, cfg, branch, opts, deps, ws, log, w); !ok {
			return r
		}
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

	return noPullRequest(res, key, branch, !recordedBranch, opts, deps, log, w)
}

// noPullRequest ends the search rather than repeating it.
//
// VerdictUnknown used to fall through to a bare warning and leave ci-wait in
// place, so a ticket whose pull request could not be found was polled again
// on the next tick, and every tick after that, forever -- no escalation, no
// bound, work sitting finished and unmerged behind a watcher stuck warning
// about it (OR-173). Treated like a CI failure instead: out of ci-wait,
// orion-failed, and told which branch was searched and whether that name was
// only a guess, so a human knows where to look next.
func noPullRequest(res Result, key, branch string, guessed bool, opts Options,
	deps Deps, log *events.Log, w io.Writer) Result {

	detail := "no pull request found for " + branch + "."
	if guessed {
		detail += " That name is a guess from convention, not a recorded branch -- " +
			"it may be suffixed if this ticket was ever retried."
	}
	detail += "\n  It may have been opened by hand under another branch, or never opened at all."

	if opts.DryRun {
		ui.Ok(w, "would", "%s: release it; %s", key, detail)
		return res
	}
	if err := deps.Jira.SetLabels(key, []string{tracker.LabelFailed},
		[]string{tracker.LabelCIWait}); err != nil {
		res.Err = err
		ui.Warn(w, "%s: could not relabel: %v", key, err)
		return res
	}
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorCI, detail+
		"\n\nTaken out of ci-wait so nothing polls it forever. If the branch exists "+
		"under a different name, find its pull request and finish it by hand; "+
		"otherwise re-queue the ticket to start again."))

	log.Emit(events.Event{Kind: events.KindFailed, Actor: events.ActorCI,
		Msg: "no pull request found for " + branch})
	res.Changed = true
	ui.Warn(w, "%s: %s", key, detail)
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
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorCI, "CI failed on "+pr.URL+".\n\n"+pr.Detail+
		"\n\nThe branch is kept. Fix it there and push, or close the pull "+
		"request and re-queue the ticket to start again."))

	log.Emit(events.Event{Kind: events.KindFailed, Actor: events.ActorCI,
		Msg: "CI failed: " + firstLine(pr.Detail)})
	res.Changed = true

	title, body := msgCIFailed(key, pr)
	tell(w, log, notify.Event{
		Key: key, Channel: channelOf(ws), Level: notify.Blocked, Workspace: ws.ID,
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
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorOrion, "merged: "+pr.URL))
	closeChildren(key, pr.URL, cfg.Tracker.QueueLabel, deps, w)
	// A branch that went red and then merged is a mistake with its own
	// correction attached, which is the one shape a lesson can be built from
	// without an agent inferring anything. Read the history BEFORE it is
	// cleared below -- this is the only moment both halves exist. The
	// resulting notice is not printed until after the merge report closes
	// (OR-178); only the recording happens here.
	lesson := recordLesson(key, pr, loadFixes(ws.Dir).States[key], entry.Source, ws, opts.Home, log)
	// Forget the fix history, and the rebase count with it. A ticket reopened
	// later must not start with either allowance already spent.
	_ = clearFixes(ws.Dir, key)
	_ = clearRebases(ws.Dir, key)
	// And the triage verdict, for the same reason: it is a statement about a
	// commit nobody would be approving any more.
	_ = clearTriaged(ws.Dir, key)
	// The branch the PR actually merged into, per its BaseRef; config is only
	// the fallback when the forge did not say. Everything below that talks
	// about WHERE the merge went uses this, because a message built from
	// config once announced "on main" for a merge GitHub put on develop.
	//
	// Reported by ROLE as well as by name. "OR-99 merged" followed by "the
	// work is on main" is a true sentence that reads as routine; "merged
	// into the release branch main" is the same fact stated so that a
	// misconfigured repository is obvious rather than plausible.
	mergedInto := pr.BaseRef
	if mergedInto == "" {
		mergedInto = cfg.VCS.WorkBranch
	}
	role := branchRole(mergedInto, cfg.VCS.DefaultBranch)

	log.Emit(events.Event{Kind: events.KindMerge, Actor: events.ActorCI,
		Msg: "merged into the " + role + " " + mergedInto + ": " + pr.URL})
	res.Changed = true
	ui.Ok(w, "ok", "%s merged into the %s %s  %s", key, role, mergedInto, pr.URL)

	// Only now, once the merge itself has been fully reported -- not a line
	// earlier -- so a retrospective lesson can never be mistaken for a fact
	// about the merge just announced (OR-178).
	announceLesson(lesson, w, log)

	// What the ticket cost, on the ticket and on the terminal, immediately
	// after the merge is announced. Here rather than at the end of this
	// function because the steps below -- refreshing the checkout, pruning
	// the worktree -- are conveniences that can fail, and the price of the
	// work is not contingent on whether a worktree was removed.
	reportCost(key, ws, deps, w)

	// The user's own checkout. Until now nothing ever did this, so a merged
	// ticket left develop behind on the machine its owner works on -- and
	// the next `orion work` preflight refuses on a stale base.
	refreshed := ""
	if deps.Refresh != nil {
		msg, err := deps.Refresh(entry.Source, mergedInto)
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
		mergedInto, cfg.VCS.DefaultBranch)
	tell(w, log, notify.Event{
		Key: key, Channel: channelOf(ws), Level: notify.Info, Workspace: ws.ID,
		Title: title, Body: body,
	})
	return res
}

// lookupEntry re-resolves a project after a merge, for the paths that need
// the user's own checkout.
func lookupEntry(home, key string) (*registry.Entry, error) {
	return registry.Lookup(home, key)
}

// branchFor guesses a ticket's branch by convention. It is only correct for
// a ticket's FIRST attempt -- AddWorktree suffixes a retry's branch to avoid
// colliding with a prior attempt's still-open pull request, and this has no
// way to know a suffix was ever applied. Used only as a fallback when no job
// has recorded a real branch for the key (OR-173); callers must say so.
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
func closeChildren(key, prURL, queueLabel string, deps Deps, w io.Writer) {
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
		// Closing without clearing would leave a Done sub-task holding the
		// claim lock, which is the state that wedges the watcher (OR-125).
		// A ticket transitioned to Done gives up every label Orion owns, on
		// every path that does the transitioning.
		if err := deps.Jira.SetLabels(c.Key, nil, tracker.Managed(queueLabel)); err != nil {
			ui.Warn(w, "%s: closed, but its labels could not be cleared: %v", c.Key, err)
		}
		_ = deps.Jira.Comment(c.Key, actors.Comment(events.ActorOrion,
			"delivered in "+key+" and merged: "+prURL))
		closed = append(closed, c.Key)
	}
	if len(closed) > 0 {
		ui.Ok(w, "closed", "%d sub-task(s) delivered by %s: %s",
			len(closed), key, strings.Join(closed, ", "))
	}
}
