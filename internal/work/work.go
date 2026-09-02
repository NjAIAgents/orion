// Package work runs one tracker issue end to end.
//
// The order of operations is the design. Every step that touches something
// outside Orion -- the tracker, the remote, the user's checkout -- happens
// only after the cheap, local, reversible steps have succeeded, so a run that
// is going to fail fails before it has changed anything anyone else can see.
//
//	resolve   which repository owns this key            (registry, free)
//	preflight budget, sandbox, clean base               (local, free)
//	merged?   has this ticket's PR already landed       (forge, free)
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
//
// The merged check sits above the claim because the lock is only as good as
// the label, and a label survives its ticket: see noop.go.
package work

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/advise"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/claim"
	"github.com/orion-sdlc/orion/internal/collect"
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
	OutcomeCIWait Outcome = "ci-wait" // pushed, PR open, CI running
	// OutcomeReady: pushed, no pull request, waiting for the integration
	// queue to batch it (OR-253). Distinct from ci-wait because nothing is
	// running: ci-wait means a machine is working, and reporting this as that
	// would have the operator waiting on a build nobody started.
	OutcomeReady   Outcome = "ready"
	OutcomeBlocked Outcome = "blocked" // ran cleanly, produced nothing, asked something
	OutcomeFailed  Outcome = "failed"  // the run or a step after it failed
	OutcomeSkipped Outcome = "skipped" // preflight refused before spending
	// OutcomeNoop: there was nothing to do, and that is correct. Either the
	// ticket's work had already merged, or the agent looked, found the change
	// already present, and declined to invent a diff to justify the run.
	//
	// Distinct from blocked because they are opposite results that look
	// identical from the outside -- both end with no commits. An agent that
	// stopped because there is nothing to do and one that stopped because it
	// could not do the thing must not carry the same label, or orion-failed
	// starts to mean "fine, actually" and stops carrying information.
	OutcomeNoop Outcome = "no-op"
	// OutcomeHeld: the ENVIRONMENT stopped this and no work was attempted.
	//
	// Distinct from failed because nothing was attempted -- no turn, no token,
	// no branch work -- and the ticket is untouched. Labelling it orion-failed
	// makes an operator hand-clear a label for a problem that never reached the
	// ticket, and teaches them that the failure label sometimes means "the
	// machine was logged out" (OR-212).
	//
	// It sits BESIDE failure rather than replacing any part of it. A run that
	// spent a turn and failed is still orion-failed, because somebody has to
	// judge why before it costs that again (OR-214). See fault.go and hold.go.
	OutcomeHeld Outcome = "held"
)

// Result is one job's ending.
type Result struct {
	Key      string
	Outcome  Outcome
	Branch   string
	PR       string
	Question string // the agent's closing message, when it stopped to ask
	// Note is why nothing was done, on a no-op outcome. Separate from
	// Question because it is not a question: nobody has to answer it.
	Note string
	// Fault is what stopped the environment, on a held outcome. Zero on
	// every other one, so Outcome remains the thing to switch on.
	Fault Fault
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
	// Limit is the plan's own verdict, as the run reported it. A watcher
	// reads this to decide whether to start anything else, and when to
	// try again if not.
	Limit supervisor.RateLimit
	Err   error
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
	// Describe drafts the pull request text, read-only. Nil falls back to
	// Orion's own two-line description -- which is accurate, and says
	// nothing a reviewer did not already know from the ticket.
	Describe Describer
	OpenPR   func(dir, branch, title, body, base string) (string, error)
	// Merged reports whether this ticket's branch already has a MERGED pull
	// request, and its URL. Asked before the ticket is claimed, so a ticket
	// whose work has already landed is never worked a second time.
	//
	// Injectable and optional for the same reason as everything else here: it
	// shells out to gh, which needs auth and a network. Nil skips the check.
	Merged func(dir, branch string) (bool, string, error)
	// Slack asks about an environmental fault and reads the answer. Nil
	// disables the question entirely, which is the A5 contract: the hold
	// still happens, the fix is still printed, and the environment being
	// healthy again is still what releases it. See hold.go.
	Slack SlackAPI
	// Preflight refuses to claim a ticket the environment cannot work, before
	// anything is written to the tracker. Nil skips it.
	//
	// Injected rather than called directly because the checks it wraps shell
	// out -- and a test that has no `claude` on PATH must not be told the
	// machine is logged out.
	Preflight func() (Fault, bool)
	Now       func() time.Time
}

// TrackerAPI is the slice of the tracker this package needs. Narrow on
// purpose: a wide interface would make the fake in tests larger than the
// code under test.
type TrackerAPI interface {
	GetIssue(key string) (*tracker.Issue, error)
	// Children returns the issue's sub-tasks, ranked. Optional in practice:
	// a tracker that cannot answer returns an error and the ticket is worked
	// as itself, which is what Orion did before hierarchy existed.
	Children(key string) ([]tracker.Issue, error)
	SetLabels(key string, add, remove []string) error
	// AssignSelf puts the ticket on the account Orion authenticates as, so
	// the board's assignee column names whoever is holding it rather than
	// staying empty through the whole run (OR-34).
	AssignSelf(key string) error
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

	// A run of identical lines is held back to be printed once with its
	// count, so the last such run needs somewhere to land. Without this the
	// count for whatever a ticket ended on is never printed at all (OR-217).
	defer ui.Flush(opts.Out)

	var results []Result
	for _, key := range opts.Keys {
		r := one(strings.ToUpper(strings.TrimSpace(key)), opts, deps)
		results = append(results, r)
		// Stop the batch on a hard failure. Continuing would spend money on
		// the next ticket while the reason the last one broke is still true,
		// and a queue that keeps going after a failure produces several
		// wrecks instead of one.
		// The same stop, with the reason named. The heuristic below reaches its
		// conclusion from CORRELATION -- one ticket failed, so the next probably
		// will -- and it is right often enough to keep. When the reason is
		// KNOWN, saying it is the difference between an operator diagnosing a
		// queue of wrecks and an operator running one command (OR-212).
		if r.Outcome == OutcomeHeld {
			ui.Warn(opts.Out, "stopping the batch: %s", r.Note)
			ui.Warn(opts.Out, "the rest of the queue is untouched and nothing is labelled failed; "+
				"work resumes when %s is healthy again", r.Fault.Kind)
			break
		}
		// The environment worked for this ticket, whatever the ticket did.
		// Recorded so a fault it met months ago cannot make its next one
		// escalate immediately (hold.go).
		forgetFault(opts.Home, r.Key)
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
		ui.Say(w, key, events.ActorOrion, ui.VerbFail, "%v", err)
		return fail(res, err)
	}
	ui.Say(w, key, events.ActorOrion, ui.VerbOK, "%s -> %s", registry.ProjectOf(key), entry.Source)

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
		ui.Say(w, key, events.ActorOrion, ui.VerbOK, "%s", msg)
		cfg = config.Load(ws.RepoDir())
	}

	// A branch model with no integration branch is refused before any agent
	// runs, rather than discovered when the merge lands on the release
	// branch. Nothing about this run is salvageable: every branch it cuts
	// bases on, and merges back into, the branch a release is cut from.
	if err := cfg.Validate(); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbFail, "%v", err)
		return fail(res, err)
	}
	if waiver := cfg.ReleaseBranchWaiver(); waiver != "" {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "%s", waiver)
	}

	// The globally configured roster (OR-132): who the implementer is and
	// what it runs on is an operator preference, the same across every
	// project, not something orion.json repeats per checkout. A bad file is
	// reported and then ignored: display names are not worth failing a run
	// over, and the shipped roster still tells the reader who acted.
	agents, agentsErr := config.LoadAgents(workspace.Home())
	if agentsErr != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"keeping the shipped agent names: %v", agentsErr)
	} else if err := actors.Configure(agents); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"keeping the shipped agent names: %v", err)
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
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "no Slack messages for this run: %s", why)
		log.Emitf(events.KindNote, events.ActorOrion, "no slack channel: %s", why)
	}

	// The budget gate, before anything is claimed. The supervisor checks it
	// too, but by then the ticket is already marked as being worked on and
	// the label has to be rolled back -- so check here as well and never
	// touch the tracker for a run that cannot start.
	// The budget gate ASKS rather than only refusing -- in Slack, and at the
	// terminal when somebody is there. See budgetack.go: a gate whose only
	// route forward was to kill the process and run another command was a
	// crash with instructions, and it stopped an unattended watcher without
	// telling anyone.
	//
	// It also RESERVES what this run is expected to cost, released when the
	// run ends. With tickets worked concurrently, a gate that only reads the
	// ledger lets every concurrent run past the same checkpoint and then lets
	// every one of them spend through it (OR-184).
	proceed, releaseBudget, msg := budgetGate(key, opts, cfg, ws, log, w)
	defer releaseBudget()
	if !proceed {
		log.Emitf(events.KindBudget, events.ActorOrion,
			"waiting for the budget checkpoint to be acknowledged")
		if msg != "" {
			fmt.Fprint(w, msg)
		}
		res.Outcome = OutcomeSkipped
		return res
	}

	issue, err := deps.Jira.GetIssue(key)
	if err != nil {
		// A tracker nobody can reach is the machine's problem, and this is
		// before the claim -- nothing was attempted and there is nothing to
		// hand back. Anything else the tracker SAYS is an answer, and an
		// answer is a failure a person has to read.
		if f, env := unreachableFault(FaultTracker, err); env {
			return held(res, key, f, nil, false, cfg, opts, deps, ws, log, w)
		}
		return fail(res, err)
	}
	res.Summary, res.IssueURL = issue.Summary, issue.URL
	ui.Say(w, key, events.ActorOrion, ui.VerbOK, "%s", issue.Summary)

	// Routed once, here, at the top -- before the claim, before the agent
	// runs, before the QA fix loop that must resume whichever actor this
	// picks. Threaded through the rest of the run as actorID rather than
	// re-derived at each site, so the run start, the resume and the fix loop
	// can never disagree about who is working the ticket.
	//
	// Said out loud on every ticket, including the default: a route that
	// falls through in silence is how the frontend actor went unreached for
	// as long as it did (OR-171).
	//
	// A DECISION, not a note. Another actor could have worked this ticket and
	// the reason it did not is right there in the same line -- both halves the
	// rule in internal/events asks for. As a note it was indistinguishable
	// from the ninety-odd other things worth seeing, which is the same as not
	// having been recorded (OR-201).
	actorID, routeWhy := Route(*issue)
	log.Emitf(events.KindDecision, events.ActorOrion, "routed to the %s: %s", actorID, routeWhy)
	ui.Say(w, key, events.ActorOrion, ui.VerbOK, "routed to %s: %s", actors.Display(actorID), routeWhy)

	// Is it already finished? The queue query excludes resolved tickets, but
	// between that search and this claim a person can close one -- and `orion
	// work KEY` typed by hand never went through the queue at all. So ask
	// about THIS ticket, now, before anything is spent.
	//
	// The stale label goes with it. Left on, the ticket returns to the head
	// of the queue on the next tick and this run repeats forever; removing it
	// is what makes the skip stick.
	if issue.Resolved() {
		return alreadyResolved(res, key, issue.Status, cfg, opts, deps, log, w)
	}

	// Has this ticket's work already landed?
	//
	// A merged pull request is the end of a ticket, whatever the labels say.
	// OR-86 merged and was picked up again six minutes later: the claim is the
	// lock, but the lock is a label, and a label that was never cleared -- or
	// was cleared after the next tick had already read the queue -- leaves a
	// window in which a finished ticket is still workable. An agent then reads
	// the repository, finds its own change already there, and stops, having
	// spent a whole run at full token price to produce nothing.
	//
	// Asked BEFORE the claim and before anything is spent, and asked of the
	// forge rather than of Orion's own bookkeeping -- which is what makes it
	// close the window whichever of the two ways it opened.
	//
	// A check that could not be made is not a merged branch. gh may be absent
	// or the network down, and refusing every run over that would be a worse
	// fault than the one this prevents.
	if deps.Merged != nil {
		branch := branchFor(cfg.VCS.BranchPrefix, key)
		merged, prURL, mErr := deps.Merged(ws.RepoDir(), branch)
		switch {
		case mErr != nil:
			// An UNREACHABLE forge is held rather than warned past. The
			// warning above is right for a gh that is absent or a repository
			// it cannot see -- those degrade one check, and refusing every run
			// over them would be the worse fault. A forge that cannot be
			// connected to is different in kind: the push and the pull request
			// at the end of this run need it too, so proceeding buys a full
			// agent run that cannot possibly finish (OR-214).
			if f, env := unreachableFault(FaultForge, mErr); env {
				return held(res, key, f, nil, false, cfg, opts, deps, ws, log, w)
			}
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
				"could not check whether %s has already merged: %v", branch, mErr)
		case merged:
			return alreadyMerged(res, key, actorID, prURL, branch, cfg, opts, deps, ws, log, w)
		}
	}

	// A ticket with sub-tasks is worked WITH them: one branch, one pull
	// request, one approval. See internal/tracker/children.go -- the short
	// version is that a Story's Tasks overlap in the files they touch far
	// more often than unrelated tickets do, so working them as separate jobs
	// manufactures the conflict that parallelism is otherwise only at risk of.
	//
	// A failure to read children is NOT a failure of the run. It means Jira
	// would not answer -- an unusual project shape, a permission, an older
	// deployment -- and the honest fallback is the previous behaviour: work
	// the ticket as itself, and say that is what happened.
	children, cErr := childrenOf(deps, key)
	if cErr != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"could not read its sub-tasks (%v); working it as a single ticket", cErr)
	}
	if len(children) > 0 {
		ui.Say(w, key, events.ActorOrion, ui.VerbOK, "%d sub-task(s), to be done in one branch", len(children))
		for i, c := range children {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w,
				fmt.Sprintf("%d. %s  %s", i+1, c.Key, c.Summary)))
		}
		// Say it is a long one rather than refusing it.
		//
		// A cap used to live here, on the reasoning that a large story would
		// exhaust the turn ceiling and leave a branch half-finished. The
		// reasoning was right about the constraint and wrong about the
		// remedy: stories with twenty-five tasks are ordinary, and the fix is
		// to give the agent room (see turnsFor) rather than to decline work
		// somebody legitimately planned.
		if len(children) >= tracker.ManyChildren {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
				"a large story (%d sub-tasks). One run, one branch, "+
					"one pull request -- expect it to take a while.", len(children))
		}
		// The budget, stated before it costs anything. A wrong number read
		// here is a config problem; the same wrong number discovered at turn
		// 121 of an opus run is $17 (OR-117).
		ui.Say(w, key, events.ActorOrion, ui.VerbOK, "budget: %d turns, %d minutes for %d sub-task(s)",
			turnsFor(opts.MaxTurns, len(children)), minutesFor(opts.MaxMinutes, len(children)), len(children))
	}

	// The environment, checked before the claim rather than discovered by the
	// agent five seconds after it starts.
	//
	// Only the checks that are FREE and LOCAL belong here -- whether the CLI
	// is signed in, whether nj-agents is installed -- because this runs once
	// per ticket. A probe that needs a network would make every claim depend
	// on a round trip to prove something the run itself will establish anyway.
	if deps.Preflight != nil {
		if f, env := deps.Preflight(); env {
			return held(res, key, f, nil, false, cfg, opts, deps, ws, log, w)
		}
	}

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
			// The claim did not land, so the ticket still wears its queue
			// label and sits in To Do: held, with nothing to hand back.
			if f, env := unreachableFault(FaultTracker, err); env {
				return held(res, key, f, nil, false, cfg, opts, deps, ws, log, w)
			}
			return fail(res, fmt.Errorf("claiming %s: %w", key, err))
		}
		log.Emitf(events.KindClaimed, events.ActorOrion, "claimed %s: %s", key, issue.Summary)
		ui.Say(w, key, events.ActorOrion, ui.VerbWorking, "claimed: %s -> %s",
			cfg.Tracker.QueueLabel, tracker.LabelWorking)
		// Which actor holds it, from the instant it is held. Orion is the
		// honest answer here: routing has picked an actor but the worktree
		// does not exist yet, so nothing is spending. Boundary one below
		// swaps this for the actor that actually takes the run.
		//
		// A SEPARATE request from the claim above, never bundled into it. The
		// claim is the lock; a cosmetic label rejected by the tracker must not
		// be able to fail it (OR-225).
		if err := setStage(deps, key, actors.StageLabel(events.ActorOrion), ""); err != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
				"the board will not say which actor holds it: %v", err)
		}
		// And WHO holds it, in the field a board actually shows. Labels are
		// Orion's own vocabulary; the assignee column is the one anybody
		// scanning a sprint board reads, and until now a claimed ticket
		// moved itself to In Progress with that column empty (OR-34).
		//
		// A THIRD request, best-effort, for the same reason the stage label
		// is its own: the claim is the lock, and a tracker that refuses an
		// assignment -- no Assign Issues permission, a deactivated account --
		// must not be able to fail a run over it. A ticket that gets worked
		// but not assigned is far better than a run refused.
		//
		// Nothing clears it on release. On a finished ticket the assignee is
		// the record of who did the work, which is what a reader expects it
		// to mean; on a failed or blocked one it names the person to go to.
		// Unassigning would throw both away to avoid a misreading the status
		// field already rules out.
		if err := deps.Jira.AssignSelf(key); err != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
				"the board will not say who holds it: %v", err)
		}
	}

	// From here a failure must hand the ticket back, or it is stuck in
	// orion-working forever and no later run will pick it up.
	defer func() {
		if opts.DryRun {
			return // nothing was claimed, so there is nothing to hand back
		}
		if res.Outcome == OutcomeFailed || res.Outcome == OutcomeBlocked {
			label := tracker.LabelFailed
			// The stage goes with the lock, in the same request. A ticket that
			// still said orion-stage-implementer while wearing orion-failed
			// would name an actor that stopped working it (OR-225).
			_ = deps.Jira.SetLabels(key, []string{label},
				append([]string{tracker.LabelWorking}, actors.StageLabels()...))
		}
	}()

	if err := transitionUnlessDry(deps, opts, key, "In Progress"); err != nil {
		// Not fatal. A workflow without that status is a configuration
		// difference, not a reason to abandon work that can still be done.
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "could not move it to In Progress: %v", err)
	}

	branch := branchFor(cfg.VCS.BranchPrefix, key)

	// RESUME rather than restart, when an interrupted run left work behind.
	//
	// The claim record names the branch its holder was on. If that claim is
	// dead, the work on that branch is this ticket's own unfinished attempt,
	// and cutting a fresh branch would abandon it -- which is how OR-135
	// reached four worktrees, the last holding an hour of uncommitted work
	// (OR-265).
	resumeFrom := ""
	if rec, err := claim.Read(opts.Home, key); err == nil && rec != nil {
		if dead, _ := claim.Dead(opts.Home, key); dead {
			resumeFrom = rec.Branch
		}
	}
	job, err := workspace.ResumeWorktree(ws, cfg.VCS.WorkBranch, branch, resumeFrom)
	if err != nil {
		return fail(res, err)
	}
	if job.Resumed {
		ui.Say(w, key, events.ActorOrion, ui.VerbOK,
			"resumed %s, where the interrupted run stopped", job.Branch)
		// A tree the previous run left dirty is committed before the agent
		// touches it. The breaker already does this for its own trips
		// (docs/BREAKERS.md): unverified work on a branch can be read,
		// resumed or dropped by a person, and an uncommitted change blocks
		// the next rebase of the branch.
		if n, err := workspace.SnapshotDirty(job.Path, key); err != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
				"the interrupted run left changes that could not be committed: %v", err)
		} else if n > 0 {
			ui.Say(w, key, events.ActorOrion, ui.VerbOK,
				"committed %d file(s) the interrupted run was holding, unverified, so the resume starts clean", n)
		}
	}

	// The claim now names the branch this run is on, so a later interruption
	// can be resumed the same way.
	if err := claim.Take(opts.Home, key, job.Branch, job.Path); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "could not record the claim: %v", err)
	}
	// Attribution hooks live in the sandbox CLONE, not in the worktree and
	// not in the user's checkout. Before this, the clone was never
	// instrumented, so every commit an agent made carried no AI-Attribution
	// trailer at all -- the most agent-written code in the repository was the
	// part with no record that an agent wrote it (OR-193).
	//
	// Best-effort and reported: a missing trailer is a worse record, not a
	// reason to throw away the run that would have produced one.
	if cfg.Attribution.Enabled {
		if err := adopt.EnsureSandboxDun(ws.CloneDir()); err != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "attribution: %v", err)
		}
	}
	res.Branch = job.Branch
	// Record the ACTUAL branch now, while it is known -- AddWorktree may have
	// suffixed it. Best-effort: a write failure here must not lose an agent
	// run over bookkeeping, but it does mean collect falls back to guessing
	// (OR-173).
	if err := workspace.RecordBranch(ws, key, job.Branch); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "could not record the branch for collect: %v", err)
	}
	// Registered here, before anything can run in the worktree, so it fires
	// however this run ends -- pushed, blocked, failed, or killed. A tripped
	// breaker stops the agent from looping and, by the time it fires, from
	// acting at all, so the tidy-up cannot be the agent's job (OR-194).
	defer func() {
		failed := res.Outcome == OutcomeFailed || res.Outcome == OutcomeBlocked
		settleTripResidue(job.Path, job.Branch, key, issue.Summary, issue.URL, failed, cfg, ws,
			func(body string) error {
				return deps.Jira.Comment(key, actors.Comment(events.ActorOrion, body))
			}, log, w)
	}()

	log.Emitf(events.KindBranch, events.ActorOrion, "branch %s from %s", job.Branch, cfg.VCS.WorkBranch)
	ui.Say(w, key, events.ActorOrion, ui.VerbOK, "branch %s", job.Branch)
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, job.Path))

	// The commit before anything about this ticket exists. QA's red-before-
	// green check (OR-156) needs it to prove a test would actually have
	// caught the change; a failure here just means that check degrades with
	// a stated reason later rather than losing the ticket over bookkeeping.
	baseSHA, shaErr := headSHA(job.Path)
	if shaErr != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "could not record the base commit: %v", shaErr)
	}

	// The job runs in its own worktree, not the shared clone.
	jobWS := *ws
	jobWS.RepoPath = job.Path

	prompt := supervisor.TicketPromptWithChildren(key, issue.Summary, issue.Description,
		issue.URL, job.Path, artifactsFor(job.Path, cfg), promptChildren(children))

	if opts.DryRun {
		// Remove the rehearsal's worktree. Left behind, every dry run
		// consumes a branch name -- orion/fcia-6, then -2, then -3 -- so the
		// real run ends up on a branch whose name says it is the third
		// attempt at a ticket nobody has actually started.
		//
		// Safe to remove without force: nothing ran, so there is nothing to
		// lose, and RemoveWorktree still refuses if that turns out false.
		if rmErr := workspace.RemoveWorktree(ws, job.Path, false); rmErr != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "left %s behind: %v", job.Path, rmErr)
		} else if _, delErr := gitOut(ws, "branch", "-D", job.Branch); delErr != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "left the branch %s behind: %v", job.Branch, delErr)
		}
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"skipped the agent (--dry-run); everything before this point succeeded")
		res.Outcome = OutcomeSkipped
		return res
	}

	// The banner, printed on CLAIM only -- not on resume, not per tick, or it
	// stops meaning "something new started". It carries everything somebody
	// scrolling back to the top of a ticket needs at once: the key, the
	// summary, who is working it, on what, and the branch.
	ui.LiveTitle(key, issue.Summary)
	ui.Banner(w, key, issue.Summary, actorID,
		actors.Model(actorID), job.Branch)

	// Boundary one: Orion has finished deciding and an agent starts spending.
	// Marked here rather than at the routing decision above, because routing
	// picks the actor and this is the moment it actually takes the run.
	handoff(w, log, deps, opts, ui.Handoff{Key: key, From: "routing", To: "implementing",
		By: events.ActorOrion, Next: actorID, Detail: "on " + job.Branch})

	log.Emitf(events.KindRunStart, actorID, "implementing %s", key)
	stTitle, stBody := msgStarted(key, issue.Summary, job.Branch, issue.URL)
	tell(w, log, ws, notify.Event{
		Key: key, Level: notify.Info, Workspace: ws.ID, Actor: actorID,
		Title: stTitle, Body: stBody,
	})

	runRes, runErr := deps.Supervise(&jobWS, supervisor.Options{
		Stage: "ticket", Prompt: prompt,
		// The roster's own model and effort, not the operator's CLI defaults.
		// Empty stays empty: the banner above reports what the registry says
		// ran, and a run configured from anywhere else would make that line
		// a claim about a different agent (OR-133).
		Model:      actors.Model(actorID),
		Effort:     actors.Effort(actorID),
		MaxMinutes: minutesFor(opts.MaxMinutes, len(children)),
		MaxTurns:   turnsFor(opts.MaxTurns, len(children)),
		OnActivity: ActivityLogger(log, w, key, actorID),
		Actor:      actorID, Key: key,
	})
	code := -1
	if runRes != nil {
		code = runRes.ExitCode
		res.LogPath = runRes.LogPath
	}
	log.Emit(events.Event{Kind: events.KindRunEnd, Actor: actorID,
		Msg:    fmt.Sprintf("exit %d", code),
		Detail: map[string]any{"reason": reasonOf(runRes)}})

	// Carry the plan's own verdict out of the run. This is what replaced
	// budget.weekly_tokens: the CLI reports the real limit on every run, so
	// nobody has to invent a number that stops work for a reason never true.
	if runRes != nil {
		res.Limit = runRes.Limit
		if !runRes.Limit.OK() {
			log.Emit(events.Event{Kind: events.KindBudget, Actor: events.ActorOrion,
				Msg: runRes.Limit.Describe(deps.Now())})
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "%s", runRes.Limit.Describe(deps.Now()))
		} else if runRes.Limit.UsingOverage {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "%s", runRes.Limit.Describe(deps.Now()))
		}
	}

	// Checked BEFORE the failure path, and it has to be: the supervisor returns
	// an error for any non-zero exit, so a run that never started -- for want
	// of a login, or against a quota wall with no stated reset -- would
	// otherwise be reported as the work failing.
	if f, env := faultOf(runRes); env {
		return held(res, key, f, job, true, cfg, opts, deps, ws, log, w)
	}

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

	// Nothing to do is not the same as nothing done.
	//
	// Checked before the advisor loop, because a run that says "the work is
	// already here" is not asking anything: routing that to an architect pays
	// for an answer to a question nobody asked, and then blocks the ticket on
	// the reply.
	if commits == 0 {
		if why, ok := noopDeclared(tailOf(runRes)); ok {
			return noChange(res, key, actorID, why, cfg, opts, deps, ws, log, w)
		}
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
		ans, asked := consult(deps, key, actorID, job.Path, question, log, w)
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
		// What was chosen and what it was derived from, not just which file
		// now holds it. "recorded docs/decisions/OR-1-1.md" is the same empty
		// line as "the advisor responded": it says a decision happened and
		// leaves the decision out of it (OR-201). The path stays on the end
		// because the record is on the branch and a reader will want it.
		log.Emitf(events.KindDecision, events.ActorOrion, "%s -- grounded in %s; recorded in %s",
			ans.Decision, ans.Grounding, rel)
		ui.Say(w, key, events.ActorOrion, ui.VerbOK, "recorded %s", rel)

		anTitle, anBody := msgAnswered(key, ans, question)
		tell(w, log, ws, notify.Event{
			Key: key, Level: notify.Info, Workspace: ws.ID, Actor: actorFor(ans.Role),
			Title: anTitle, Body: anBody,
		})

		if runRes.SessionID == "" {
			// Without a session there is nothing to continue, and re-running
			// from the top would pay for the whole context again and might
			// make different choices. Better to stop and say so.
			res.Question = question
			res.Advice = ans
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
				"answered, but the session could not be resumed; stopping so the answer is not lost")
			break
		}

		ui.Say(w, key, actorFor(ans.Role), ui.VerbWorking, "resuming with the %s's answer", ans.Role)
		runRes, runErr = deps.Supervise(&jobWS, supervisor.Options{
			Stage: "ticket", Resume: runRes.SessionID,
			Prompt:     AnswerMessage(ans, rel),
			Model:      actors.Model(actorID),
			Effort:     actors.Effort(actorID),
			MaxMinutes: minutesFor(opts.MaxMinutes, len(children)),
			MaxTurns:   turnsFor(opts.MaxTurns, len(children)),
			OnActivity: ActivityLogger(log, w, key, actorID),
			Actor:      actorID, Key: key,
		})
		if f, env := faultOf(runRes); env {
			return held(res, key, f, job, true, cfg, opts, deps, ws, log, w)
		}
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
		// Again, because the advisor loop may have resumed the run and the
		// resumed run may be the one that found there was nothing to do.
		if why, ok := noopDeclared(tailOf(runRes)); ok {
			return noChange(res, key, actorID, why, cfg, opts, deps, ws, log, w)
		}
		res.Question = tailOf(runRes)
		res.Outcome = OutcomeBlocked
		log.Emitf(events.KindBlocked, actorID,
			"ran cleanly but produced no commits; treating the closing message as a question")
		ui.Say(w, key, actorID, ui.VerbFail, "produced no commits. It is blocked, not done.")
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
		_ = deps.Jira.Comment(key, actors.Comment(actorID, body))
		blTitle, blBody := msgBlocked(key, issue.Summary, res.Question, issue.URL, res.Advice)
		tell(w, log, ws, notify.Event{
			Key: key, Level: notify.Blocked, Workspace: ws.ID, Actor: actorID,
			Title: blTitle, Body: blBody,
		})
		return res
	}
	log.Emitf(events.KindCommit, actorID, "%d commit(s) on %s", commits, job.Branch)

	// Boundary two: implementation is over. The commit count is DETAIL on
	// this line rather than a line of its own -- it used to be the last thing
	// printed before QA started, so it stood in for a handoff it never
	// described, and answered "how many commits" when the reader was asking
	// what stage the run had reached (OR-189).
	//
	// Named for where the run actually goes. With QA switched off there is no
	// QA stage to enter, and a boundary announcing one would be a handoff to
	// an actor that never runs.
	nextStage, nextActor := "qa", events.ActorQA
	if !cfg.QA.On() {
		nextStage, nextActor = "push", events.ActorOrion
	}
	handoff(w, log, deps, opts, ui.Handoff{Key: key, From: "implementing", To: nextStage,
		By: actorID, Next: nextActor,
		Detail: fmt.Sprintf("%d commit(s) on %s", commits, job.Branch)})

	// QA, before the branch is pushed. The tests QA writes and any fix its
	// findings force belong in the same pull request as the change they are
	// about; verifying after the pull request opened would leave the reviewer
	// reading the code without its evidence. See qa.go.
	qa := runQA(qaJob{
		Key: key, Summary: issue.Summary, Description: issue.Description,
		ImplSession: runRes.SessionID, Actor: actorID, WS: &jobWS,
		MaxMinutes: minutesFor(opts.MaxMinutes, len(children)),
		MaxTurns:   turnsFor(opts.MaxTurns, len(children)),
		BaseSHA:    baseSHA,
	}, cfg, opts, deps, log, w)

	// Re-counted: QA commits its tests, and a fix round commits too, so the
	// number in the pull request body would otherwise describe the branch as
	// it was before it was verified.
	if n, cErr := commitsOn(job.Path, cfg.VCS.WorkBranch); cErr == nil {
		commits = n
	}

	// Boundary three: QA is over and the branch leaves the machine. Only when
	// QA actually ran -- the boundary above already handed straight to push
	// otherwise, and announcing a stage nobody entered is the same lie in
	// reverse.
	if qa.Ran {
		handoff(w, log, deps, opts, ui.Handoff{Key: key, From: "qa", To: "push",
			By: events.ActorQA, Next: events.ActorOrion, Detail: qa.Verdict()})
	}

	// Last thing before the branch leaves the machine: put it on top of what
	// the base is NOW (OR-227). The base moved while the agent was running --
	// at concurrency 4 it usually does -- and a branch pushed at the base it
	// started from triggers a full CI run against a base that no longer
	// exists, only for the landing pass to rebase it and trigger a second.
	// This never refuses the push: a conflict, a locked worktree or an
	// unreachable remote all end with the branch pushed as it stands.
	collect.RebaseBeforePush(key, job.Path, job.Branch, cfg, ws, log, w)

	if err := deps.Push(job.Path, job.Branch); err != nil {
		return failAndTell(res, fmt.Errorf("pushing %s: %w", job.Branch, err), key, ws, log, w, deps)
	}
	log.Emitf(events.KindPush, events.ActorOrion, "pushed %s", job.Branch)

	// UNDER BATCH INTEGRATION THE BRANCH STOPS HERE (OR-253).
	//
	// A pull request per ticket is what made batching cost MORE than the path
	// it replaced: `ci.yml` builds every `pull_request`, so N tickets bought N
	// CI runs before the batch had run at all, and then each individual merge
	// left the others behind the work branch to be rebased and rebuilt. The
	// batch's own pull request is the single run and the single review
	// surface for the whole set.
	//
	// The branch is pushed either way. What changes is that nothing opens a
	// pull request for it and nothing waits on its checks: the ticket becomes
	// READY, and the integration queue picks it up when it assembles the next
	// batch.
	if cfg.Collect.BatchIntegration {
		return readyForBatch(res, key, issue, job, cfg, opts, deps, ws, log, w)
	}

	// Boundary four: the branch is on the remote and a pull request is next.
	// Orion holds both sides, so it says it continues rather than handing to
	// itself. The pushed branch is the detail.
	handoff(w, log, deps, opts, ui.Handoff{Key: key, From: "push", To: "pull request",
		By: events.ActorOrion, Next: events.ActorOrion, Detail: "pushed " + job.Branch})

	title := key + ": " + issue.Summary
	body := prBody(key, issue.URL, commits)
	// Ask nj-agents' pr-describe for something a reviewer can actually use.
	// Falls back silently to the two lines above: the branch is pushed and
	// the work is done, so refusing to open a pull request over a cosmetic
	// failure would strand finished work for no reason.
	if t, b, ok := describePR(deps.Describe, job.Path, key, title, body); ok {
		title, body = t, b
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorDescriber,
			Model: actors.Model(events.ActorDescriber),
			Msg:   "wrote the pull request description"})
	}
	url, err := deps.OpenPR(job.Path, job.Branch, title, body, cfg.VCS.WorkBranch)
	if err != nil {
		return failAndTell(res, fmt.Errorf("opening a pull request: %w", err), key, ws, log, w, deps)
	}
	res.PR = url
	log.Emitf(events.KindPR, events.ActorOrion, "opened %s", url)
	ui.Say(w, key, events.ActorOrion, ui.VerbOK, "opened %s", url)

	// Hand the ticket to the CI-wait state and release the job slot. The
	// state lives on the ticket so a crash here does not lose the fact that
	// a pull request exists -- otherwise a retry would run the agent again
	// and open a second one.
	//
	// The stage label goes with it. ci-wait already says everything true of
	// this state -- a pull request is open, no agent is running -- and a
	// stage naming an actor who has finished would outlive the work it
	// describes (OR-225).
	if err := deps.Jira.SetLabels(key, []string{tracker.LabelCIWait},
		append([]string{tracker.LabelWorking}, actors.StageLabels()...)); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "could not mark it as awaiting CI: %v", err)
	}
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorOrion,
		"opened "+url+" from "+job.Branch+"."))
	_ = deps.Jira.TransitionTo(key, "In Review")

	ciTitle, ciBody := msgCIWait(key, issue.Summary, job.Branch, url, issue.URL, commits)
	tell(w, log, ws, notify.Event{
		Key: key, Level: notify.Info, Workspace: ws.ID,
		Title: ciTitle, Body: ciBody,
	})

	// Boundary five, and the one most easily got wrong. The next party here
	// is CI: a machine, running no agent and spending nothing. Naming devops
	// -- the agent that only appears if the build goes RED -- would have the
	// operator watching an agent apparently work for the length of the CI
	// run, which is the same defect this line exists to fix, pointed the
	// other way (OR-189). ui.Handoff says "no agent is running" for it.
	handoff(w, log, deps, opts, ui.Handoff{Key: key, From: "pull request", To: "ci",
		By: events.ActorOrion, Next: events.ActorCI, Detail: "the job slot is free"})
	res.Outcome = OutcomeCIWait
	return res
}

// readyForBatch ends a run whose branch is pushed and whose ticket now waits
// for the integration queue (OR-253).
//
// The pull-request path's ending, minus the pull request. Everything that
// makes the state durable is still done, and for the same reason: the claim
// is released, the ticket says what it is waiting for, and the job slot is
// freed. A crash after this point must not leave a ticket that looks claimed
// but has no agent, which is the failure orion-working exists to prevent.
//
// NO PULL REQUEST, and no CI. That is the whole change: N tickets used to buy
// N pull-request runs plus a rebase for every merge after the first, before
// the batch had proved anything. The batch's own pull request is the one run
// and the one review surface for the set.
func readyForBatch(res Result, key string, issue *tracker.Issue, job *workspace.Job,
	cfg config.Config, opts Options, deps Deps, ws *workspace.Workspace,
	log *events.Log, w io.Writer) Result {

	// The claim goes, the readiness arrives, in one request. Two requests
	// would leave a window where the ticket is neither claimed nor ready, and
	// a watcher reconciling in that window would see free work and start a
	// second agent on it.
	if err := deps.Jira.SetLabels(key, []string{tracker.LabelReady},
		append([]string{tracker.LabelWorking}, actors.StageLabels()...)); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"could not mark it ready for the batch: %v", err)
	}
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorOrion,
		"pushed "+job.Branch+". Waiting for the integration queue: it will be "+
			"assembled into the next batch, tested with it, and land with it."))
	_ = deps.Jira.TransitionTo(key, "In Review")

	ui.Say(w, key, events.ActorOrion, ui.VerbOK,
		"ready for the next batch on %s; no pull request, no CI run of its own", job.Branch)
	log.Emitf(events.KindNote, events.ActorOrion,
		"ready for batch integration on %s", job.Branch)

	// The next party is Orion's integration queue, not CI and not an agent.
	// Naming CI here would say a build is running when none is, which is the
	// same defect boundary five exists to avoid, pointed at a different
	// audience.
	handoff(w, log, deps, opts, ui.Handoff{Key: key, From: "push", To: "ready",
		By: events.ActorOrion, Next: events.ActorOrion,
		Detail: "the job slot is free; waiting for the integration queue"})

	res.Outcome = OutcomeReady
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

// budgetBlocked was replaced by budgetGate. It answered "may this run
// spend?" with a message and nothing else, which was the whole problem: the
// only way to say yes was to stop the process and run another command.

func failAndTell(res Result, err error, key string, ws *workspace.Workspace,
	log *events.Log, w io.Writer, deps Deps) Result {
	log.Emitf(events.KindFailed, events.ActorOrion, "%v", err)
	ui.Say(w, key, events.ActorOrion, ui.VerbFail, "%v", err)
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorOrion,
		"failed on this ticket.\n\n"+err.Error()))

	summary := res.Summary
	if summary == "" {
		summary = key
	}
	title, body := msgFailed(key, summary, err.Error(), res.Branch, res.IssueURL, res.LogPath)
	tell(w, log, ws, notify.Event{
		Key: key, Level: notify.Blocked, Workspace: ws.ID,
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
	// Invite the humans, exactly as init does.
	//
	// This path CREATES a channel when init never got to -- somebody declined
	// the provisioning prompt, or adopted a repository before Slack was
	// configured. Without this it makes a private channel whose only member
	// is the bot, which is the fcia failure precisely: every message
	// delivered, none readable, and no error anywhere.
	//
	// ensureAudience at init time did not cover this, because this is not
	// init. Fixing one entry point and not the other leaves the bug alive
	// behind a door that is opened less often, which is worse than leaving
	// it alone -- it will be rediscovered from scratch.
	//
	// Only on creation. The invite is idempotent, but calling it on every
	// notification would spend an API call per message to re-assert
	// something that was settled the first time.
	if ch.Created && len(cfg.Slack.InviteUsers) > 0 {
		c.Invite(ch.ID, cfg.Slack.InviteUsers)
	}
	ws.Task.Slack = &workspace.SlackChannel{ID: ch.ID, Name: ch.Name}
	if err := ws.SaveTask(); err != nil {
		// Not fatal: the id is good for this run, it just will not be
		// remembered for the next one.
		return ch.ID, ""
	}
	return ch.ID, ""
}

// tell sends a notification, resolving the channel itself, and reports every
// way it can fail to arrive.
//
// notify.Send returns the errors it collected, and every call site here used
// to discard them. A Slack token that had expired therefore produced exactly
// the same output as a successful post: nothing.
//
// It also used to take a pre-resolved Channel from channelOf, which threw away
// the REASON resolveChannel had computed -- so an empty channel returned
// here in silence, holding a precise diagnosis ("slack is disabled in
// orion.json", "slack is enabled but not usable: ...") that nobody would
// ever see. That is the same silent-Slack failure this package exists to
// prevent, one layer down from where it was fixed.
//
// Taking the workspace instead of the channel is also no more work: every
// call site called channelOf(ws), which re-resolved on each call anyway.
func tell(w io.Writer, log *events.Log, ws *workspace.Workspace, e notify.Event) {
	id, why := resolveChannel(ws)
	if id == "" {
		if why == "" {
			why = "no channel is bound to this workspace"
		}
		ui.Warn(w, "not sending to Slack: %s", why)
		if log != nil {
			log.Emitf(events.KindNote, events.ActorOrion, "slack skipped: %s", why)
		}
		return
	}
	e.Channel = id
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
//
// EVERY PATH OUT OF HERE CLOSES THE ASK, with an answer or a refuse. The two
// advisor-unreachable returns below used to leave without emitting either, so
// the log recorded a question and never what became of it -- and the reply the
// implementer then acted on was gone (OR-201).
func consult(deps Deps, key, actorID, dir, question string, log *events.Log, w io.Writer) (advise.Answer, bool) {
	// The whole question, like the whole answer below. Six of the six asks
	// ever recorded were the agent's closing message, and the last of them
	// ends "...worth having on record:" -- cut mid-sentence, one line into
	// the thing it was about to put on record (OR-201). The terminal takes
	// the first line; the log takes what was said.
	log.Emitf(events.KindAsk, actorID, "%s", question)
	ui.Say(w, key, actorID, ui.VerbWorking, "asking: %s", firstLine(question))

	role := advise.Route(deps.Advise, dir, question)
	// The router, not Orion. Haiku makes this call on every escalation and
	// the line used to be attributed to the supervisor, which is the one
	// actor that runs no model at all.
	log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorRouter,
		Model: advise.ModelRouter, Msg: "routed to the " + string(role)})
	ans, err := advise.Ask(deps.Advise, dir, role, question, advise.Artifacts(dir, role))
	if err != nil {
		return unreachable(log, w, key, role, err), false
	}

	if ans.Verdict == advise.VerdictEscalate {
		other := advise.RolePM
		if role == advise.RolePM {
			other = advise.RoleArchitect
		}
		log.Emit(events.Event{Kind: events.KindEscalate, Actor: string(role),
			Model: advise.ModelAdvisor,
			Msg:   fmt.Sprintf("escalated to the %s: %s", other, firstLine(ans.Reason))})
		ui.Say(w, key, actorFor(role), ui.VerbWarn, "this is for the %s", other)
		ans, err = advise.Ask(deps.Advise, dir, other, question, advise.Artifacts(dir, other))
		if err != nil {
			return unreachable(log, w, key, other, err), false
		}
	}

	if ans.Answered() {
		// The decision UNEDITED, the way KindSay records the agent's own
		// prose. A first line is a headline, and an answer reduced to its
		// headline cannot explain what the implementer did next -- the
		// terminal below is the place to be brief, not the record.
		log.Emit(events.Event{Kind: events.KindAnswer, Actor: string(ans.Role),
			Model:  ans.Model,
			Msg:    ans.Decision,
			Detail: map[string]any{"grounding": ans.Grounding}})
		ui.SayModel(w, key, actorFor(ans.Role), ans.Model, ui.VerbOK, "%s", firstLine(ans.Decision))
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, ans.Grounding))
		return ans, true
	}

	// The whole reason, for the same reason: "the artifacts are silent" is
	// the beginning of a refusal, and what it goes on to say is which
	// document a person now has to amend.
	log.Emit(events.Event{Kind: events.KindRefuse, Actor: string(ans.Role),
		Model: ans.Model, Msg: ans.Reason})
	ui.SayModel(w, key, actorFor(ans.Role), ans.Model, ui.VerbWarn,
		"could not decide this: %s", firstLine(ans.Reason))
	return ans, true
}

// unreachable closes an ask that never reached an advisor.
//
// A transport failure is a refusal like any other -- the question is
// unanswered and a person has to decide -- and it is recorded as one so that
// the ask it closes is not left dangling. Attributed to the role that was
// being asked, matching the Answer this returns, because "the architect could
// not be reached" is the fact; who failed to reach it is Orion either way.
func unreachable(log *events.Log, w io.Writer, key string, role advise.Role, err error) advise.Answer {
	ans := advise.Answer{Role: role, Verdict: advise.VerdictRefused,
		Reason: "the advisor could not be reached: " + err.Error()}
	log.Emit(events.Event{Kind: events.KindRefuse, Actor: string(role), Msg: ans.Reason})
	ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "could not reach the %s: %v", role, err)
	return ans
}

// actorFor maps an advisor's role to its actor identifier.
//
// They happen to be the same strings today, and mapping them anyway is the
// point: the role is advise's vocabulary and the identifier is what gets
// persisted into the event log, so one may be renamed without silently
// rewriting the other's history.
func actorFor(role advise.Role) string {
	switch role {
	case advise.RoleArchitect:
		return events.ActorArchitect
	case advise.RolePM:
		return events.ActorPM
	case advise.RoleHuman:
		return events.ActorHuman
	}
	return events.ActorOrion
}

// childrenOf reads the sub-tasks worth working.
//
// Done children are dropped rather than passed along: a finished sub-task is
// context, not work, and listing it invites the agent to redo something a
// person completed by hand -- which it cannot reliably tell from the text.
func childrenOf(deps Deps, key string) ([]tracker.Issue, error) {
	if deps.Jira == nil {
		return nil, nil
	}
	kids, err := deps.Jira.Children(key)
	if err != nil {
		return nil, err
	}
	return tracker.Workable(kids), nil
}

// promptChildren converts tracker issues into the prompt's own shape, so the
// supervisor package does not import the tracker for one struct.
func promptChildren(kids []tracker.Issue) []supervisor.Child {
	var out []supervisor.Child
	for _, k := range kids {
		out = append(out, supervisor.Child{
			Key: k.Key, Summary: k.Summary, Description: k.Description,
		})
	}
	return out
}

// A story's budget grows with the number of sub-tasks it carries.
//
// The supervisor's defaults -- 120 turns, 30 minutes -- were sized for one
// ticket. A story with twenty-five tasks given the same allowance gets about
// five turns per task, which is not enough to read a file, change it and run
// the suite. It would stop at the ceiling somewhere in the middle and leave
// a branch half-finished, having spent the whole run.
//
// That failure is what an earlier version tried to prevent by REFUSING large
// stories. Refusing was the wrong remedy: stories with twenty-five tasks are
// ordinary, and a tool that will not work them does not fit how people
// decompose. Giving the agent room is the right one.
//
// An EXPLICIT --max-turns or --max-minutes always wins. Somebody who names a
// bound is bounding this run deliberately, and silently raising it would
// make the flag advisory -- which is worse than not having it.
func turnsFor(explicit, children int) int {
	if explicit > 0 {
		return explicit
	}
	if children == 0 {
		return 120
	}
	// Roughly a task's worth of work per task, on top of the base allowance
	// for reading the repo and getting oriented. Bounded: past a point the
	// wall clock, the budget checkpoint and the plan limit are the real
	// brakes, and a turn ceiling in the thousands is not a ceiling.
	return clamp(120+25*children, 120, 600)
}

// minutesFor mirrors turnsFor. The base is 90 -- the wall-clock default a
// childless ticket has always had -- NOT the 30 an earlier draft used. That
// 30 was written when the watch path always passed an explicit 90, so it
// never actually applied; resurrecting it once the sentinel fix made it
// reachable would have silently cut every single-ticket run to a third of
// its time. Decision recorded on OR-117.
func minutesFor(explicit, children int) int {
	if explicit > 0 {
		return explicit
	}
	if children == 0 {
		return 90
	}
	return clamp(90+10*children, 90, 180)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
