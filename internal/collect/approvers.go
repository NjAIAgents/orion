package collect

import (
	"fmt"
	"strings"
)

// Naming the approvers in a way Slack actually notifies.
//
// The approval request is the one message that WAITS on a person, and it used
// to name them in plain prose: "Only navjyot can approve." Slack notifies on
// <@U012ABCDEF> and nothing else, so that sentence styled the approver's name
// like any other word and told them nothing. The ticket then sat in ci-wait
// repeating "nobody has approved it yet" at a channel the one person who could
// answer had no reason to look at.
//
// mention() in mention.go is the other half of this and stays separate: it
// tags whoever slack.mention names on a message that reports a FAILURE, from
// ids that are already ids. This resolves slack.merge_approvers -- usernames
// or emails, per config.go -- into the mention form, and applies to the
// approval request alone. A merged notice, a started notice and a cost report
// do not mention anybody: tagging on those trains a room to mute the channel,
// which costs the approval request the only advantage it has.

// memberResolver turns a configured approver into the member id a mention
// needs. Satisfied by *slack.Client; separate from SlackAPI's other methods
// only so the rendering below can be tested without a workspace.
type memberResolver interface {
	MemberID(who string) (string, error)
}

// approverTags renders slack.merge_approvers for the approval request.
//
// Returns one entry per approver, in order: a mention where the person
// resolved, and otherwise their configured name exactly as before. Never an
// error -- an approval request that fails to send because a lookup failed is
// far worse than one that does not tag -- so every failure comes back in
// unresolved for the run to report instead.
func approverTags(s memberResolver, approvers []string) (tags, unresolved []string) {
	for _, a := range approvers {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		id, err := "", error(nil)
		if s != nil {
			id, err = s.MemberID(a)
		} else {
			err = fmt.Errorf("Slack is not configured for approvals")
		}
		if err != nil || id == "" {
			if err == nil {
				err = fmt.Errorf("no member id came back")
			}
			tags = append(tags, escapeMrkdwn(a))
			unresolved = append(unresolved, fmt.Sprintf(
				"could not mention %q, so the request names them but Slack notified "+
					"nobody: %v", a, err))
			continue
		}
		tags = append(tags, "<@"+id+">")
	}
	return tags, unresolved
}

// escapeMrkdwn neutralises the characters that make Slack read text as
// markup, for the fallback path where a configured value is dropped into the
// message verbatim.
//
// The one that matters is <!channel>: an approver entry Slack cannot resolve
// is printed as-is, and a value shaped like a broadcast would then notify
// every member of a channel that Orion creates per project -- the exact thing
// the mention path refuses to do deliberately.
func escapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}
