package collect

// The approval gate for a batch (OR-253).
//
// The per-branch path asks per ticket. A batch asks ONCE, for the set: the
// members were tested together and merge together, so approving them
// individually would be asking four questions with one possible answer. It
// also keeps the request count proportional to integrations rather than to
// tickets, which is the difference the two-queue design exists to make.
//
// Everything about HOW the answer is asked for and read is reused from
// approvalflow.go rather than reimplemented: the same channel, the same
// reactions, the same allowlist, the same two-pass shape. Only the subject
// differs.

import (
	"fmt"
	"io"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// batchRequestKey namespaces a batch's approval request away from ticket keys.
//
// The requests file is keyed by ticket, and a batch is not a ticket. A prefix
// that cannot be a Jira key keeps the two from ever colliding -- and if one
// did, the collision would silently approve a merge on the strength of an
// answer given about something else.
func batchRequestKey(ref string) string { return "batch:" + ref }

// batchApprover gates a green batch on a human reaction, or returns nil when
// no gate is configured.
//
// NIL RATHER THAN AN ALWAYS-YES FUNCTION, so the absence of a gate is visible
// at the call site instead of hidden inside one. A repository with no
// approvers configured lands its batches unattended, which is the behaviour
// it has today and the behaviour an autonomous pipeline is supposed to have.
//
// Configured approvers WITHOUT the means to ask them is the one case that
// refuses rather than degrading: it means somebody asked for a gate and would
// otherwise get silent auto-merging, which is the opposite of what they asked
// for.
func batchApprover(cfg config.Config, opts Options, deps Deps,
	ws *workspace.Workspace, log *events.Log, w io.Writer) Approver {

	if len(cfg.Slack.MergeApprovers) == 0 {
		return nil // no gate asked for
	}
	channel := channelOf(ws)
	if deps.Slack == nil || channel == "" {
		return func(ref string, members []string) (bool, error) {
			return false, fmt.Errorf(
				"slack.merge_approvers names %s but Slack is not available to ask them, "+
					"so %s cannot be approved; merge it yourself or clear the approvers",
				strings.Join(cfg.Slack.MergeApprovers, ", "), ref)
		}
	}

	return func(ref string, members []string) (bool, error) {
		key := batchRequestKey(ref)
		existing, asked := loadRequests(ws.Dir).Requests[key]

		// A record whose MEMBERS differ is about a different batch on the
		// same ref name, so it is asked afresh (OR-318). The ref is
		// ephemeral and reused -- every batch is orion/batch -- and the
		// question a human answered was about a set of tickets, not a name.
		if asked && !sameKeys(existing.Members, members) {
			_ = clearRequest(ws.Dir, key)
			asked = false
		}
		// Already decided and acted on: say so once and do nothing. Without
		// this the pass would re-read the reaction and land the same batch
		// twice.
		if asked && existing.Decided {
			ui.Ok(w, "waiting", "%s was already approved and landed", ref)
			return false, nil
		}

		if !asked {
			if opts.DryRun {
				ui.Ok(w, "would", "ask #%s to approve landing %s (%s)",
					channel, ref, strings.Join(members, " "))
				return false, nil
			}
			tags, unresolved := approverTags(deps.Slack, cfg.Slack.MergeApprovers)
			ts, err := deps.Slack.PostTS(channel, batchApprovalMessage(ref, members, tags))
			if err != nil {
				return false, fmt.Errorf("asking for approval to land %s: %w", ref, err)
			}
			for _, u := range unresolved {
				ui.Warn(w, "%s", u)
			}
			// The affordances, so the answer is a tap. Orion's own reactions
			// are excluded when the answer is read, or this line would
			// approve every merge it asked about.
			deps.Slack.React(channel, ts, "white_check_mark")
			deps.Slack.React(channel, ts, "x")

			// MEMBERS are recorded with the ask, not only with the decision
			// (OR-318). Without them the very next pass reads an empty member
			// list, decides the batch has changed, and asks again -- which is
			// the bug this is fixing, arrived at from the other direction.
			if err := saveRequest(ws.Dir, Request{
				Key: key, Channel: channel, TS: ts,
				AskedAt: deps.Now(), Approvers: cfg.Slack.MergeApprovers,
				Members: members,
			}); err != nil {
				ui.Warn(w, "asked, but could not record the request (%v); "+
					"the next pass will ask again", err)
			}
			log.Emitf(events.KindNote, events.ActorOrion,
				"asked for approval to land %s (%s)", ref, strings.Join(members, " "))
			ui.Say(w, "", events.ActorOrion, ui.VerbOK,
				"%d branch(es) green; asked #%s to approve the merge", len(members), channel)
			return false, nil
		}

		d, err := awaitDecision(existing, key, cfg, opts, deps, w)
		if err != nil {
			return false, fmt.Errorf("reading the approval for %s: %w", ref, err)
		}
		switch {
		case d.Rejected:
			// Forgotten, so a later batch on the same ref name asks again
			// rather than inheriting this answer. The ref is ephemeral and
			// reused; the decision was about one set of members in it.
			_ = clearRequest(ws.Dir, key)
			log.Emitf(events.KindBlocked, events.ActorHuman,
				"%s declined the batch merge (%s)", d.By, d.How)
			ui.Warn(w, "%s declined the merge (%s). Nothing was merged; "+
				"the branches are untouched and will be offered again.", d.By, d.How)
			return false, nil
		case !d.Approved:
			age := deps.Now().Sub(existing.AskedAt).Round(60_000_000_000) // minutes
			ui.Ok(w, "waiting", "the batch is green; waiting on approval (%s ago) -- %s",
				age, d.Why)
			return false, nil
		}

		if opts.DryRun {
			ui.Ok(w, "would", "land %s, approved by %s", ref, d.By)
			return false, nil
		}
		// The record is KEPT until the members change (OR-318).
		//
		// Clearing it here was the bug: the batch ref name is constant, so
		// the next pass over orion/batch found no record, decided nobody had
		// been asked, and posted the same request again. Observed as four
		// asks for two batches, each already carrying a green tick -- and
		// every duplicate is a message whose reaction Orion may then read
		// from the wrong one.
		//
		// A rejection still clears, because a decline is about one set of
		// members and a later batch must not inherit it.
		//
		// Recorded with the members it was approved for, so a batch whose
		// membership has changed asks again -- which is the property the
		// clear was reaching for, without forgetting that a request exists.
		if err := saveRequest(ws.Dir, Request{
			Key: key, Channel: existing.Channel, TS: existing.TS,
			AskedAt: existing.AskedAt, Approvers: existing.Approvers,
			Members: members, Decided: true,
		}); err != nil {
			ui.Warn(w, "approved, but could not record the decision (%v); "+
				"the next pass may ask again", err)
		}
		log.Emitf(events.KindNote, events.ActorHuman,
			"%s approved landing %s (%s)", d.By, ref, d.How)
		ui.Say(w, "", events.ActorHuman, ui.VerbOK,
			"%s approved the batch (%s); landing it", d.By, d.How)
		return true, nil
	}
}

// batchApprovalMessage is what the approvers see.
//
// The MEMBERS are the message. "A batch is ready" tells a person nothing they
// can act on; the tickets about to reach the work branch is the thing they
// are being asked about, and it is the only part they can check.
func batchApprovalMessage(ref string, members, tags []string) string {
	var b strings.Builder
	b.WriteString("*A batch is green and ready to merge*\n")
	fmt.Fprintf(&b, "%d ticket(s) were assembled into `%s`, tested together in one CI run, "+
		"and every check passed:\n", len(members), ref)
	for _, m := range members {
		fmt.Fprintf(&b, "  • %s\n", m)
	}
	b.WriteString("\nWhat was tested is what merges: approving lands the whole set as one commit.\n")
	b.WriteString(":white_check_mark: to merge   :x: to decline\n")
	if len(tags) > 0 {
		b.WriteString(strings.Join(tags, " "))
	}
	return b.String()
}

// sameKeys reports whether two member lists name the same tickets, order
// disregarded (OR-318).
//
// Order is not significant: the batch assembles in whatever order the queue
// hands it, and a human approving "OR-309 and OR-310" has approved the same
// thing as "OR-310 and OR-309". Comparing order-sensitively would re-ask on
// a reshuffle that changed nothing.
func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, k := range a {
		seen[k]++
	}
	for _, k := range b {
		seen[k]--
		if seen[k] < 0 {
			return false
		}
	}
	return true
}
