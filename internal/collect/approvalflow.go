package collect

import (
	"fmt"
	"io"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

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
		return res
	}

	d, err := ReadDecision(deps.Slack, existing.Channel, existing.TS,
		deps.Slack.BotID(), cfg.Slack.MergeApprovers)
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
