// Package work runs one tracker issue end to end.
//
// The order of operations is the design. Every step that touches something
// outside Orion -- the tracker, the remote, the user's checkout -- happens
// only after the cheap, local, reversible steps have succeeded, so a run that
// is going to fail fails before it has changed anything anyone else can see.
//
//	resolve   which repository owns this key            (registry, free)
//	preflight budget, sandbox, clean base               (local, free)
//	claim     ORION -> orion-working, To Do -> Progress (tracker, reversible)
//	worktree  a branch nothing else can touch           (local, reversible)
//	run       the supervised agent                      (COSTS MONEY)
//	verify    commits exist                             (local)
//	push      the branch reaches the remote             (public)
//	pr        open it                                   (public)
//	ci-wait   release the job slot                      (tracker)
//
// Claiming before running rather than after is deliberate: two runs must not
// pick up the same ticket, and the label is the lock. Pushing only after
// commits exist is the other half -- an agent that stopped to ask a question
// exits 0 having produced nothing, and pushing an empty branch would open a
// pull request describing no change.
package work

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/advise"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Outcome is how a job ended. Named rather than a bare error because the
// three failure modes need different handling: blocked wants a human,
// failed wants a look at the log, and ci-wait wants patience.
type Outcome string

const (
	OutcomeCIWait  Outcome = "ci-wait" // pushed, PR open, CI running
	OutcomeBlocked Outcome = "blocked" // ran cleanly, produced nothing, asked something
	OutcomeFailed  Outcome = "failed"  // the run or a step after it failed
	OutcomeSkipped Outcome = "skipped" // preflight refused before spending
)

// Result is one job's ending.
type Result struct {
	Key      string
	Outcome  Outcome
	Branch   string
	PR       string
	Question string // the agent's closing message, when it stopped to ask
	// Advice is the last verdict, when an advisor was consulted. A refusal
	// here is the useful part of a blocked outcome: it says the DESIGN is
	// incomplete, not that the agent failed.
	Advice advise.Answer
	// Summary, IssueURL and LogPath are carried so a failure message can be
	// written without re-fetching the ticket -- which would fail for exactly
	// the reasons that caused the failure being reported.
	Summary  string
	IssueURL string
	LogPath  string
	Err      error
}

// Deps are the seams. Every one of these either costs money, mutates a shared
// system, or needs a network -- so each is injectable and the orchestration
// itself is testable without any of them.
type Deps struct {
	Jira      TrackerAPI
	Supervise func(ws *workspace.Workspace, opts supervisor.Options) (*supervisor.Result, error)
	// Advise runs a READ-ONLY agent turn for an advisor or the router.
	// Separate from Supervise because an advisor must not be able to edit:
	// two agents writing to one worktree is a race with no referee, and an
	// architect that "just fixes it while it is there" destroys the
	// separation that makes its answer worth anything.
	Advise advise.Runner
	Push   func(dir, branch string) error
	OpenPR func(dir, branch, title, body, base string) (string, error)
	Now    func() time.Time
}

// TrackerAPI is the slice of the tracker this package needs. Narrow on
// purpose: a wide interface would make the fake in tests larger than the
// code under test.
type TrackerAPI interface {
	GetIssue(key string) (*tracker.Issue, error)
	SetLabels(key string, add, remove []string) error
	TransitionTo(key, status string) error
	Comment(key, text string) error
}

// Options for one invocation.
type Options struct {
	Keys []string
	Out  io.Writer
	Home string
	// DryRun stops before the agent is launched, after everything free has
	// been proved. The one thing worth rehearsing is the part that costs.
	DryRun bool
	// MaxMinutes and MaxTurns bound the agent, passed through to the
	// supervisor which owns the wall clock.
	MaxMinutes int
	MaxTurns   int
}

// Run works each key in the order given.
//
// Sequential, not concurrent. Two agents in one repository would fight over
// git, and the ordering the user expressed by listing the keys -- or by
// ranking the backlog -- is the whole point of a queue.
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

	var results []Result
	for _, key := range opts.Keys {
		r := one(strings.ToUpper(strings.TrimSpace(key)), opts, deps)
		results = append(results, r)
		// Stop the batch on a hard failure. Continuing would spend money on
		// the next ticket while the reason the last one broke is still true,
		// and a queue that keeps going after a failure produces several
		// wrecks instead of one.
		if r.Outcome == OutcomeFailed {
			ui.Warn(opts.Out, "stopping the batch after %s failed; the next ticket would likely fail the same way", r.Key)
			break
		}
	}
	return results
}

// one works a single ticket.
//
// The return value is NAMED because the rollback below is a defer that
// inspects it. With an unnamed return, `return fail(res, err)` sets the
// caller's value but leaves the local `res` untouched, so the defer sees an
// empty Outcome, decides nothing failed, and leaves the ticket claimed --
// stuck in orion-working, invisible to the queue, retried by nobody. Caught
// by TestAFailedRunReleasesTheTicket.
func one(key string, opts Options, deps Deps) (res Result) {
	w := opts.Out
	res = Result{Key: key}

	entry, err := registry.Lookup(opts.Home, key)
	if err != nil {
		ui.Fail(w, "%v", err)
		return fail(res, err)
	}
	ui.Ok(w, "resolved", "%s -> %s", registry.ProjectOf(key), entry.Source)

	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		return fail(res, fmt.Errorf("sandbox %s is unusable: %w\n"+
			"  Re-create it with: orion init (in %s)", entry.Workspace, err, entry.Source))
	}
	cfg := config.Load(ws.RepoDir())
	// Same reason as in collect: policy is read from the sandbox clone, so a
	// stale clone serves stale policy. Branch bases are already taken from
	// origin/<base> by AddWorktree, so this is only about the config.
	if msg, syncErr := workspace.SyncSandbox(ws, cfg.VCS.WorkBranch); syncErr == nil && msg != "" {
		ui.Ok(w, "refresh", "%s", msg)
		cfg = config.Load(ws.RepoDir())
	}

	log, logErr := events.Open(events.Path(ws.Dir), events.Event{
		Project: registry.ProjectOf(key), Key: key,
		Run: fmt.Sprintf("%d", deps.Now().UnixNano()), Actor: events.ActorOrion,
	})
	if logErr == nil {
		defer log.Close()
	}

	// Say up front when nothing will be reported to Slack. Discovering after
	// an hour that the run you were not watching also was not reporting is
	// the failure this whole notification path exists to prevent, and it is
	// worth one line at the start to rule out.
	if id, why := resolveChannel(ws); id == "" && why != "" {
		ui.Warn(w, "no Slack messages for this run: %s", why)
		log.Emitf(events.KindNote, events.ActorOrion, "no slack channel: %s", why)
	}

	// The budget gate, before anything is claimed. The supervisor checks it
	// too, but by then the ticket is already marked as being worked on and
	// the label has to be rolled back -- so check here as well and never
	// touch the tracker for a run that cannot start.
	if msg, blocked := budgetBlocked(opts.Home, cfg); blocked {
		log.Emitf(events.KindBudget, events.ActorOrion, "refused before claiming: %s", firstLine(msg))
		fmt.Fprint(w, msg)
		res.Outcome = OutcomeSkipped
		return res
	}

	issue, err := deps.Jira.GetIssue(key)
	if err != nil {
		return fail(res, err)
	}
	res.Summary, res.IssueURL = issue.Summary, issue.URL
	ui.Ok(w, "bound", "%s  %s", key, issue.Summary)

	// Claim it. This is the lock: two runs must not pick up one ticket, and
	// the label is what makes that visible to anyone looking at the board.
	//
	// Skipped under --dry-run. A rehearsal must not mutate a shared system:
	// the first dry run written here DID claim, and left the ticket in
	// orion-working with no rollback -- out of the queue, retried by nobody,
	// for a run that never started. A dry run proves the free steps, and
	// writing to the tracker is not one of them.
	if !opts.DryRun {
		if err := deps.Jira.SetLabels(key, []string{tracker.LabelWorking},
			[]string{cfg.Tracker.QueueLabel}); err != nil {
			return fail(res, fmt.Errorf("claiming %s: %w", key, err))
		}
		log.Emitf(events.KindClaimed, events.ActorOrion, "claimed %s: %s", key, issue.Summary)
		ui.Ok(w, "claimed", "%s -> %s", cfg.Tracker.QueueLabel, tracker.LabelWorking)
	}

	// From here a failure must hand the ticket back, or it is stuck in
	// orion-working forever and no later run will pick it up.
	defer func() {
		if opts.DryRun {
			return // nothing was claimed, so there is nothing to hand back
		}
		if res.Outcome == OutcomeFailed || res.Outcome == OutcomeBlocked {
			label := tracker.LabelFailed
			_ = deps.Jira.SetLabels(key, []string{label}, []string{tracker.LabelWorking})
		}
	}()

	if err := transitionUnlessDry(deps, opts, key, "In Progress"); err != nil {
		// Not fatal. A workflow without that status is a configuration
		// difference, not a reason to abandon work that can still be done.
		ui.Warn(w, "could not move %s to In Progress: %v", key, err)
	}

	branch := branchFor(cfg.VCS.BranchPrefix, key)
	job, err := workspace.AddWorktree(ws, cfg.VCS.WorkBranch, branch)
	if err != nil {
		return fail(res, err)
	}
	res.Branch = job.Branch
	log.Emitf(events.KindBranch, events.ActorOrion, "branch %s from %s", job.Branch, cfg.VCS.WorkBranch)
	ui.Ok(w, "created", "branch %s", job.Branch)
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, job.Path))

	// The job runs in its own worktree, not the shared clone.
	jobWS := *ws
	jobWS.RepoPath = job.Path

	prompt := supervisor.TicketPrompt(key, issue.Summary, issue.Description, issue.URL,
		artifactsFor(job.Path, cfg))

	if opts.DryRun {
		// Remove the rehearsal's worktree. Left behind, every dry run
		// consumes a branch name -- orion/fcia-6, then -2, then -3 -- so the
		// real run ends up on a branch whose name says it is the third
		// attempt at a ticket nobody has actually started.
		//
		// Safe to remove without force: nothing ran, so there is nothing to
		// lose, and RemoveWorktree still refuses if that turns out false.
		if rmErr := workspace.RemoveWorktree(ws, job.Path, false); rmErr != nil {
			ui.Warn(w, "left %s behind: %v", job.Path, rmErr)
		} else if _, delErr := gitOut(ws, "branch", "-D", job.Branch); delErr != nil {
			ui.Warn(w, "left the branch %s behind: %v", job.Branch, delErr)
		}
		ui.Ok(w, "skipped", "the agent (--dry-run); everything before this point succeeded")
		res.Outcome = OutcomeSkipped
		return res
	}

	log.Emitf(events.KindRunStart, events.ActorImplementer, "implementing %s", key)
	stTitle, stBody := msgStarted(key, issue.Summary, job.Branch, issue.URL)
	tell(w, log, notify.Event{
		Channel: channelOf(ws), Level: notify.Info, Workspace: ws.ID,
		Title: stTitle, Body: stBody,
	})

	runRes, runErr := deps.Supervise(&jobWS, supervisor.Options{
		Stage: "ticket", Prompt: prompt,
		MaxMinutes: opts.MaxMinutes, MaxTurns: opts.MaxTurns,
		OnActivity: activityLogger(log, w, events.ActorImplementer),
	})
	code := -1
	if runRes != nil {
		code = runRes.ExitCode
		res.LogPath = runRes.LogPath
	}
	log.Emit(events.Event{Kind: events.KindRunEnd, Actor: events.ActorImplementer,
		Msg:    fmt.Sprintf("exit %d", code),
		Detail: map[string]any{"reason": reasonOf(runRes)}})

	if runErr != nil || (runRes != nil && runRes.ExitCode != 0) {
		err := runErr
		if err == nil {
			err = fmt.Errorf("the agent exited %d: %s", runRes.ExitCode, reasonOf(runRes))
		}
		return failAndTell(res, err, key, ws, log, w, deps)
	}

	// Exit 0 does not mean work was done. An agent that stopped to ask a
	// question exits cleanly having produced nothing, and pushing that would
	// open a pull request describing no change.
	commits, err := commitsOn(job.Path, cfg.VCS.WorkBranch)
	if err != nil {
		return failAndTell(res, err, key, ws, log, w, deps)
	}

	// The advisor loop. This is the automation of carrying a question to the
	// model that designed the project and carrying the answer back.
	//
	// It only engages when the run produced NOTHING and said something --
	// which is what "stopped to ask" looks like from outside. A run that
	// committed and also mused about an alternative is finished, not blocked.
	for round := 1; commits == 0 && strings.TrimSpace(runRes.Final) != "" &&
		round <= maxQuestions && deps.Advise != nil; round++ {

		question := strings.TrimSpace(runRes.Final)
		ans, asked := consult(deps, job.Path, question, log, w)
		if !asked || !ans.Answered() {
			res.Question = question
			res.Advice = ans
			break
		}

		// Record it BEFORE resuming, so the implementer can read the file it
		// is being told about, and so a crash mid-loop leaves the reasoning
		// on the branch rather than only in a log.
		path, wErr := WriteDecision(job.Path, key, round, question, ans)
		if wErr != nil {
			return failAndTell(res, wErr, key, ws, log, w, deps)
		}
		if cErr := CommitDecision(job.Path, path, key, ans); cErr != nil {
			return failAndTell(res, cErr, key, ws, log, w, deps)
		}
		rel, _ := filepath.Rel(job.Path, path)
		log.Emitf(events.KindDecision, events.ActorOrion, "recorded %s", rel)
		ui.Ok(w, "created", "%s", rel)

		anTitle, anBody := msgAnswered(key, ans, question)
		tell(w, log, notify.Event{
			Channel: channelOf(ws), Level: notify.Info, Workspace: ws.ID,
			Title: anTitle, Body: anBody,
		})

		if runRes.SessionID == "" {
			// Without a session there is nothing to continue, and re-running
			// from the top would pay for the whole context again and might
			// make different choices. Better to stop and say so.
			res.Question = question
			res.Advice = ans
			ui.Warn(w, "answered, but the session could not be resumed; stopping so the answer is not lost")
			break
		}

		ui.Ok(w, "working", "resuming with the %s's answer", ans.Role)
		runRes, runErr = deps.Supervise(&jobWS, supervisor.Options{
			Stage: "ticket", Resume: runRes.SessionID,
			Prompt:     AnswerMessage(ans, rel),
			MaxMinutes: opts.MaxMinutes, MaxTurns: opts.MaxTurns,
			OnActivity: activityLogger(log, w, events.ActorImplementer),
		})
		if runErr != nil || runRes == nil || runRes.ExitCode != 0 {
			err := runErr
			if err == nil {
				err = fmt.Errorf("the resumed run exited %d: %s", runRes.ExitCode, reasonOf(runRes))
			}
			return failAndTell(res, err, key, ws, log, w, deps)
		}
		if commits, err = commitsOn(job.Path, cfg.VCS.WorkBranch); err != nil {
			return failAndTell(res, err, key, ws, log, w, deps)
		}
	}

	if commits == 0 {
		res.Question = tailOf(runRes)
		res.Outcome = OutcomeBlocked
		log.Emitf(events.KindBlocked, events.ActorImplementer,
			"ran cleanly but produced no commits; treating the closing message as a question")
		ui.Warn(w, "%s produced no commits. It is blocked, not done.", key)
		if res.Question != "" {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, firstLine(res.Question)))
		}
		body := "Orion stopped without making a change.\n\n" + res.Question
		if res.Advice.Verdict != "" && !res.Advice.Answered() {
			// The useful part of a blocked outcome: an advisor looked and
			// could not derive it, which says the DESIGN is incomplete rather
			// than that the agent failed. Whoever picks this up should amend
			// the artifact, not just answer in a comment.
			body += "\n\nThe " + string(res.Advice.Role) + " could not decide it from the committed design:\n" +
				res.Advice.Reason +
				"\n\nDecide it, then amend the artifact so the next ticket does not ask again."
		}
		_ = deps.Jira.Comment(key, body)
		blTitle, blBody := msgBlocked(key, issue.Summary, res.Question, issue.URL, res.Advice)
		tell(w, log, notify.Event{
			Channel: channelOf(ws), Level: notify.Blocked, Workspace: ws.ID,
			Title: blTitle, Body: blBody,
		})
		return res
	}
	log.Emitf(events.KindCommit, events.ActorImplementer, "%d commit(s) on %s", commits, job.Branch)
	ui.Ok(w, "created", "%d commit(s)", commits)

	if err := deps.Push(job.Path, job.Branch); err != nil {
		return failAndTell(res, fmt.Errorf("pushing %s: %w", job.Branch, err), key, ws, log, w, deps)
	}
	log.Emitf(events.KindPush, events.ActorOrion, "pushed %s", job.Branch)
	ui.Ok(w, "pushed", "%s", job.Branch)

	title := key + ": " + issue.Summary
	body := prBody(key, issue.URL, commits)
	url, err := deps.OpenPR(job.Path, job.Branch, title, body, cfg.VCS.WorkBranch)
	if err != nil {
		return failAndTell(res, fmt.Errorf("opening a pull request: %w", err), key, ws, log, w, deps)
	}
	res.PR = url
	log.Emitf(events.KindPR, events.ActorOrion, "opened %s", url)
	ui.Ok(w, "created", "pull request %s", url)

	// Hand the ticket to the CI-wait state and release the job slot. The
	// state lives on the ticket so a crash here does not lose the fact that
	// a pull request exists -- otherwise a retry would run the agent again
	// and open a second one.
	if err := deps.Jira.SetLabels(key, []string{tracker.LabelCIWait},
		[]string{tracker.LabelWorking}); err != nil {
		ui.Warn(w, "could not mark %s as awaiting CI: %v", key, err)
	}
	_ = deps.Jira.Comment(key, "Orion opened "+url+" from "+job.Branch+".")
	_ = deps.Jira.TransitionTo(key, "In Review")

	ciTitle, ciBody := msgCIWait(key, issue.Summary, job.Branch, url, issue.URL, commits)
	tell(w, log, notify.Event{
		Channel: channelOf(ws), Level: notify.Info, Workspace: ws.ID,
		Title: ciTitle, Body: ciBody,
	})
	ui.Ok(w, "bound", "%s awaiting CI; the job slot is free", key)
	res.Outcome = OutcomeCIWait
	return res
}

// artifactsFor lists the design documents that exist, so the prompt points
// only at real files. Naming a file that is not there invites the agent to
// go looking, or to invent what it would have said.
func artifactsFor(dir string, cfg config.Config) []string {
	var out []string
	for _, rel := range []string{
		"intent.md", "spec.md", "plan.md",
		filepath.Join(cfg.Paths.Specs, "spec.md"),
		filepath.Join(cfg.Paths.Plans, "plan.md"),
		"docs/decisions",
	} {
		clean := filepath.Clean(rel)
		if _, err := os.Stat(filepath.Join(dir, clean)); err == nil {
			if !contains(out, clean) {
				out = append(out, clean)
			}
		}
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// branchFor builds orion/fcia-6 from the prefix and the key.
func branchFor(prefix, key string) string {
	if prefix == "" {
		prefix = "orion/"
	}
	return prefix + strings.ToLower(key)
}

// commitsOn counts IMPLEMENTATION commits: those touching anything outside
// docs/decisions.
//
// Excluding the decision records is not tidiness. Orion commits one per
// question, so an agent that only ever asks produces five commits and no
// code -- and a plain count would read that as work, push it, and open a
// pull request whose entire content is a record of not having decided
// anything. Caught by TestTheAdvisorLoopIsCapped.
func commitsOn(dir, base string) (int, error) {
	args := func(ref string) []string {
		return []string{"-C", dir, "rev-list", "--count", ref + "..HEAD",
			"--", ".", ":(exclude)docs/decisions"}
	}
	out, err := exec.Command("git", args("origin/"+base)...).CombinedOutput()
	if err != nil {
		// Fall back to the local base: a repo whose work branch is not
		// pushed is unusual but not broken.
		out, err = exec.Command("git", args(base)...).CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("counting commits: %s", strings.TrimSpace(string(out)))
		}
	}
	n := 0
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n); scanErr != nil {
		return 0, fmt.Errorf("unexpected rev-list output: %q", strings.TrimSpace(string(out)))
	}
	return n, nil
}

func prBody(key, url string, commits int) string {
	return fmt.Sprintf(
		"Implements %s.\n\n%s\n\n%d commit(s), produced by an Orion run.\n\n"+
			"The agent was instructed to change only what this issue requires. "+
			"Anything it noticed and deliberately left alone is in its closing "+
			"message on the ticket.\n", key, url, commits)
}

func budgetBlocked(home string, cfg config.Config) (string, bool) {
	st, ok := budgetStatus(home, cfg)
	if !ok || st.Crossed == 0 {
		return "", false
	}
	return st.Message(), true
}

func failAndTell(res Result, err error, key string, ws *workspace.Workspace,
	log *events.Log, w io.Writer, deps Deps) Result {
	log.Emitf(events.KindFailed, events.ActorOrion, "%v", err)
	ui.Fail(w, "%v", err)
	_ = deps.Jira.Comment(key, "Orion failed on this ticket.\n\n"+err.Error())

	summary := res.Summary
	if summary == "" {
		summary = key
	}
	title, body := msgFailed(key, summary, err.Error(), res.Branch, res.IssueURL, res.LogPath)
	tell(w, log, notify.Event{
		Channel: channelOf(ws), Level: notify.Blocked, Workspace: ws.ID,
		Title: title, Body: body,
	})
	return fail(res, err)
}

// transitionUnlessDry keeps the dry-run guard in one place rather than
// repeating the condition at every tracker write.
func transitionUnlessDry(deps Deps, opts Options, key, status string) error {
	if opts.DryRun {
		return nil
	}
	return deps.Jira.TransitionTo(key, status)
}

// gitOut runs git in the sandbox clone.
func gitOut(ws *workspace.Workspace, args ...string) (string, error) {
	repo := filepath.Join(ws.Dir, "repo")
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func fail(res Result, err error) Result {
	res.Outcome = OutcomeFailed
	res.Err = err
	return res
}

// channelOf finds the Slack channel this project reports to, resolving and
// recording it if the workspace has never been told.
//
// It used to be a bare `if ws.Task.Slack != nil` and nothing else, which is
// how a whole run completed without a single Slack message. `orion init`
// creates the channel and enables Slack in the config, but for an ADOPTED
// repository it never wrote the channel back into the sandbox's task.json --
// so the field stayed nil, notify was handed an empty channel, and the Slack
// branch was skipped without ever being attempted. No error, because nothing
// had failed: nobody had asked.
//
// Resolving here rather than only fixing init means workspaces bound by an
// older build heal on their next run instead of needing to be re-created.
func channelOf(ws *workspace.Workspace) string {
	id, _ := resolveChannel(ws)
	return id
}

// resolveChannel returns the channel and, when there is none, WHY -- so the
// caller can say so instead of going quiet. Silence is the failure mode this
// whole package exists to avoid.
func resolveChannel(ws *workspace.Workspace) (string, string) {
	if ws == nil {
		return "", "no workspace"
	}
	if ws.Task.Slack != nil && ws.Task.Slack.ID != "" {
		return ws.Task.Slack.ID, ""
	}
	cfg := config.Load(ws.RepoDir())
	if !cfg.Slack.Enabled {
		return "", "slack is disabled in orion.json"
	}
	c, err := slack.FromEnv()
	if err != nil {
		return "", "slack is enabled but not usable: " + err.Error()
	}
	// CreateChannel returns the existing channel of that name rather than
	// failing, so this binds to the room init already made instead of
	// making a second one.
	ch, err := c.CreateChannel(cfg.Slack.ChannelPrefix+ws.Task.Slug, cfg.Slack.Private)
	if err != nil {
		return "", "could not resolve #" + cfg.Slack.ChannelPrefix + ws.Task.Slug + ": " + err.Error()
	}
	ws.Task.Slack = &workspace.SlackChannel{ID: ch.ID, Name: ch.Name}
	if err := ws.SaveTask(); err != nil {
		// Not fatal: the id is good for this run, it just will not be
		// remembered for the next one.
		return ch.ID, ""
	}
	return ch.ID, ""
}

// tell sends a notification and reports what failed.
//
// notify.Send returns the errors it collected, and every call site here used
// to discard them. A Slack token that had expired therefore produced exactly
// the same output as a successful post: nothing.
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

func reasonOf(r *supervisor.Result) string {
	if r == nil {
		return "no result"
	}
	return r.Reason
}

// tailOf returns the agent's closing message, which is where a stop-to-ask
// question lands.
//
// Final first: that is what the model actually said. Reason is Orion's own
// classification ("completed", "timed out"), which would put the word
// "completed" on a ticket as though it were the agent's explanation for
// having done nothing.
func tailOf(r *supervisor.Result) string {
	if r == nil {
		return ""
	}
	if f := strings.TrimSpace(r.Final); f != "" {
		return f
	}
	return strings.TrimSpace(r.Reason)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// budgetStatus reads the ledger against the project's configured limits.
func budgetStatus(home string, cfg config.Config) (budget.Status, bool) {
	lim := budget.Limits{WeeklyUSD: cfg.Budget.WeeklyUSD, WeeklyTokens: cfg.Budget.WeeklyTokens}
	if !lim.Set() {
		return budget.Status{}, false
	}
	l, err := budget.Load(home)
	if err != nil && l == nil {
		return budget.Status{}, false
	}
	return l.Status(lim), true
}

// maxQuestions caps the advisor loop.
//
// An implementer that dislikes an answer can ask again, and a pair of agents
// can converse indefinitely at full price while producing nothing. Five is
// generous for one ticket: past that, the design is the problem, not the
// question, and a person should look.
const maxQuestions = 5

// consult routes a question and asks the right advisor, trying the other role
// once when the first escalates.
//
// The retry exists because routing is a guess. A product question sent to the
// architect comes back as "escalate", and forwarding it costs one more cheap
// call -- far better than declaring the run blocked over a misclassification.
func consult(deps Deps, dir, question string, log *events.Log, w io.Writer) (advise.Answer, bool) {
	log.Emitf(events.KindAsk, events.ActorImplementer, "%s", firstLine(question))
	ui.Ok(w, "working", "asking: %s", firstLine(question))

	role := advise.Route(deps.Advise, dir, question)
	log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
		Model: advise.ModelRouter, Msg: "routed to the " + string(role)})
	ans, err := advise.Ask(deps.Advise, dir, role, question, advise.Artifacts(dir, role))
	if err != nil {
		ui.Warn(w, "could not reach the %s: %v", role, err)
		return advise.Answer{Role: role, Verdict: advise.VerdictRefused,
			Reason: "the advisor could not be reached: " + err.Error()}, false
	}

	if ans.Verdict == advise.VerdictEscalate {
		other := advise.RolePM
		if role == advise.RolePM {
			other = advise.RoleArchitect
		}
		log.Emit(events.Event{Kind: events.KindEscalate, Actor: string(role),
			Model: advise.ModelAdvisor,
			Msg:   fmt.Sprintf("escalated to the %s: %s", other, firstLine(ans.Reason))})
		ui.Ok(w, "working", "the %s says this is for the %s", role, other)
		ans, err = advise.Ask(deps.Advise, dir, other, question, advise.Artifacts(dir, other))
		if err != nil {
			return advise.Answer{Role: other, Verdict: advise.VerdictRefused,
				Reason: "the advisor could not be reached: " + err.Error()}, false
		}
	}

	if ans.Answered() {
		log.Emit(events.Event{Kind: events.KindAnswer, Actor: string(ans.Role),
			Model:  ans.Model,
			Msg:    firstLine(ans.Decision),
			Detail: map[string]any{"grounding": ans.Grounding}})
		ui.Ok(w, "ok", "%s: %s", ans.Role, firstLine(ans.Decision))
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, ans.Grounding))
		return ans, true
	}

	log.Emit(events.Event{Kind: events.KindRefuse, Actor: string(ans.Role),
		Model: ans.Model, Msg: firstLine(ans.Reason)})
	ui.Warn(w, "the %s could not decide this: %s", ans.Role, firstLine(ans.Reason))
	return ans, true
}
