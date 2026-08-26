package collect

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// defaultPoll is how often a waiting collect re-reads the reaction. Slack
// reactions are cheap to read and a person who has just tapped one is
// watching the terminal, so a slow poll would be felt as a hang.
const defaultPoll = 10 * time.Second

// awaitDecision reads the approval, optionally waiting for one to arrive.
//
// With AwaitApproval unset this is a single read and the behaviour is
// unchanged -- which is what `orion watch` needs, since it has somewhere
// better to be and will look again on the next tick.
//
// With it set, this polls until somebody answers, the deadline passes, or
// the person interrupts. It exists because the manual path is used exactly
// when no watcher is running, so "ask and exit" leaves the approval with
// nothing to read it.
//
// Interruption is not an error and is not silent. Ctrl-c during a wait
// leaves a posted request and a reaction that may already be there, so the
// one thing the person must be told is that the state is intact and the
// command is safe to re-run -- otherwise the reasonable guess is that
// stopping it cancelled the request.
func awaitDecision(req Request, key string, cfg config.Config,
	opts Options, deps Deps, w io.Writer) (Decision, error) {

	read := func() (Decision, error) {
		return ReadDecision(deps.Slack, req.Channel, req.TS,
			deps.Slack.BotID(), cfg.Slack.MergeApprovers)
	}
	if opts.AwaitApproval <= 0 {
		return read()
	}

	poll := opts.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	deadline := deps.Now().Add(opts.AwaitApproval)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
		"waiting up to %s for a reaction; ctrl-c to stop waiting",
		opts.AwaitApproval.Round(time.Second))))

	for {
		d, err := read()
		if err != nil {
			return d, err
		}
		if d.Approved || d.Rejected {
			return d, nil
		}
		if !deps.Now().Before(deadline) {
			ui.Warn(w, "%s: nobody answered within %s. Nothing was merged.",
				key, opts.AwaitApproval.Round(time.Second))
			leaveNote(w, key)
			return d, nil
		}
		select {
		case <-stop:
			fmt.Fprintln(w)
			ui.Warn(w, "%s: stopped waiting.", key)
			leaveNote(w, key)
			return d, nil
		case <-time.After(poll):
		}
	}
}

// leaveNote says the request survives, and how to come back to it.
func leaveNote(w io.Writer, key string) {
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
		"the request is still in Slack and still valid -- react there, then run "+
			"`orion collect %s` again. Nothing was cancelled.", key)))
}

// The merge approval loop.
//
// Two passes, never one. The first pass posts the request and stops; a later
// pass reads the answer. Asking and acting in the same pass would mean
// waiting inside the collector for a human to reply -- which is the blocking
// design that made `orion work` release the job slot in the first place.
//
// The gate order is: checks pass, THEN ask. Never the reverse. Asking a
// person to approve a branch whose tests have not finished trains them to
// approve on the strength of the request existing, which is precisely the
// rubber stamp this is supposed to prevent.
func approvalFlow(res Result, key string, pr PR, cfg config.Config, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	channel := channelOf(ws)
	if channel == "" {
		ui.Warn(w, "%s: approval is required but this project has no Slack channel; "+
			"merge it yourself at %s", key, pr.URL)
		return res
	}

	existing, asked := loadRequests(ws.Dir).Requests[key]

	if !asked {
		if opts.DryRun {
			ui.Ok(w, "would", "%s: ask #%s to approve %s", key, channel, pr.URL)
			return res
		}
		title, body := msgApprovalWanted(key, pr, branch, cfg.Slack.MergeApprovers)
		ts, err := deps.Slack.PostTS(channel, title+"\n"+body)
		if err != nil {
			res.Err = err
			ui.Warn(w, "%s: could not ask for approval: %v", key, err)
			return res
		}
		// The affordances, so a phone user taps rather than types. Orion's
		// own reactions are excluded when the answer is read -- otherwise
		// this line alone would approve every merge it requested.
		deps.Slack.React(channel, ts, "white_check_mark")
		deps.Slack.React(channel, ts, "x")

		if err := saveRequest(ws.Dir, Request{
			Key: key, Channel: channel, TS: ts, PR: pr.URL,
			AskedAt: deps.Now(), Approvers: cfg.Slack.MergeApprovers,
		}); err != nil {
			// The message is already posted, so losing the handle means the
			// next pass asks again. Say so rather than leaving a duplicate
			// to be discovered in the channel.
			ui.Warn(w, "%s: asked, but could not record the request (%v); "+
				"the next pass will ask again", key, err)
		}
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: "asked for merge approval in Slack"})
		res.Changed = true
		ui.Ok(w, "ok", "%s: checks pass; asked for approval in Slack", key)

		// Say that a SECOND pass is required, and how to get one.
		//
		// The message used to say only "asked for approval in Slack" and
		// return. Someone who then approved -- promptly, correctly, on their
		// phone -- watched nothing happen and reasonably concluded the
		// approval had not been read. It had not: nothing was looking.
		if opts.AwaitApproval <= 0 {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
				"react there, then run `orion collect %s` again to act on it "+
					"(orion watch does this on its next tick)", key)))
			return res
		}
		// Waiting: carry the request forward and fall through to read it,
		// rather than returning and making the person re-run the command
		// they are already sitting in front of.
		existing = Request{
			Key: key, Channel: channel, TS: ts, PR: pr.URL,
			AskedAt: deps.Now(), Approvers: cfg.Slack.MergeApprovers,
		}
	}

	d, err := awaitDecision(existing, key, cfg, opts, deps, w)
	if err != nil {
		res.Err = err
		ui.Warn(w, "%s: could not read the approval: %v", key, err)
		return res
	}

	switch {
	case d.Rejected:
		if opts.DryRun {
			ui.Ok(w, "would", "%s: record the rejection by %s", key, d.By)
			return res
		}
		// Out of ci-wait so nothing polls it forever, but NOT marked failed:
		// the build was fine and the agent did nothing wrong. A person said
		// no, which is a decision about the change, not a fault to retry.
		_ = deps.Jira.SetLabels(key, nil, []string{tracker.LabelCIWait})
		_ = deps.Jira.Comment(key, fmt.Sprintf(
			"%s declined the merge in Slack (%s).\n\nThe branch and pull request are kept.",
			d.By, d.How))
		_ = clearRequest(ws.Dir, key)
		// Forget any conflict too. This ticket is finished either way, and a
		// stale entry would silence the announcement for a FUTURE conflict on a
		// re-used key.
		_ = clearConflict(ws.Dir, key)
		log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorHuman,
			Msg: d.By + " declined the merge"})
		res.Changed = true
		ui.Warn(w, "%s: %s declined the merge (%s). Nothing was merged.", key, d.By, d.How)
		return res

	case !d.Approved:
		age := deps.Now().Sub(existing.AskedAt).Round(time.Minute)
		ui.Ok(w, "ci-wait", "%s: waiting on approval (%s ago) -- %s", key, age, d.Why)
		return res
	}

	if opts.DryRun {
		ui.Ok(w, "would", "%s: merge %s, approved by %s", key, pr.URL, d.By)
		return res
	}

	// Approved. Merge, and record WHO approved it on the pull request before
	// merging -- the Slack message is not part of the repository's history,
	// and six months from now the PR is the only place anyone will look.
	_ = deps.Jira.Comment(key, fmt.Sprintf("%s approved the merge in Slack (%s).", d.By, d.How))
	if err := deps.Merge(ws.RepoDir(), branch, fmt.Sprintf(
		"Approved by %s in Slack (%s).", d.By, d.How), cfg.VCS.MergeStrategy); err != nil {
		res.Err = err
		ui.Fail(w, "%s: approved by %s, but the merge failed: %v", key, d.By, err)
		// Leave the request in place. The approval still stands, so a later
		// pass retries the merge rather than asking the same person again.
		return res
	}
	_ = clearRequest(ws.Dir, key)
	// Forget any conflict too. This ticket is finished either way, and a
	// stale entry would silence the announcement for a FUTURE conflict on a
	// re-used key.
	_ = clearConflict(ws.Dir, key)
	log.Emit(events.Event{Kind: events.KindMerge, Actor: events.ActorHuman,
		Msg: "merged on " + d.By + "'s approval"})
	ui.Ok(w, "ok", "%s: merged on %s's approval", key, d.By)
	res.Changed = true

	// Re-read rather than assume. The merge command reported success, but
	// everything downstream -- closing the ticket, fast-forwarding the
	// checkout, deleting a worktree -- is destructive enough to be worth
	// confirming against GitHub rather than against our own optimism.
	after, err := deps.Status(ws.RepoDir(), branch)
	if err != nil || after.Verdict != VerdictMerged {
		ui.Warn(w, "%s: merged, but GitHub does not report it yet; "+
			"the next pass will finish closing it", key)
		return res
	}
	entry, lookupErr := lookupEntry(opts.Home, key)
	if lookupErr != nil {
		return res
	}
	return merged(res, key, after, cfg, branch, opts, deps, ws, entry, log, w)
}
