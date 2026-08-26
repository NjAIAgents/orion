package collect

import (
	"fmt"
	"strings"

	"github.com/orion-sdlc/orion/internal/slack"
)

// Reading a merge approval out of Slack.
//
// This is the one place where a message in a chat room causes code to land on
// a shared branch, so the rules are deliberately narrow and every one of them
// is a refusal:
//
//	the approval must be on the REQUEST message, not anywhere in the channel
//	the approver must be on an allowlist, not merely present in the room
//	the approver must not be the bot, however it reacted
//	a rejection anywhere beats every approval
//
// The last rule matters most. Someone who reacts :x: after a colleague has
// already approved is objecting to something the colleague missed, and a
// scheme where the first answer wins would merge over the objection.

// Decision is what the channel said about a merge request.
type Decision struct {
	Approved bool
	Rejected bool
	By       string // display name, for the audit trail
	How      string // ":white_check_mark:" or the text that carried it
	// Why is the reason nothing was decided: nobody answered, or somebody
	// answered who is not permitted to.
	Why string
}

// Approval emoji and words. Both are supported because a phone user reacts
// and a laptop user types, and refusing one of those makes the whole
// mechanism something people work around.
var (
	approveEmoji = []string{"white_check_mark", "heavy_check_mark", "+1", "shipit", "rocket"}
	rejectEmoji  = []string{"x", "no_entry", "no_entry_sign", "-1", "hand"}
	approveWords = []string{"approve", "approved", "lgtm", "ship it", "merge it", "go ahead"}
	rejectWords  = []string{"reject", "rejected", "no", "hold", "stop", "wait", "do not merge", "don't merge"}
)

// SlackReader is the slice of Slack an approval needs.
type SlackReader interface {
	Reactions(channelID, ts string) ([]slack.Reaction, error)
	Replies(channelID, ts string) ([]slack.Message, error)
	UserName(userID string) string
}

// ReadDecision inspects the reactions and replies on one request message.
//
// botID is excluded from every count. Orion adds the reaction emoji to its
// own message as affordances -- so a human can tap rather than find them --
// and counting those would make every request self-approving. That is not a
// hypothetical: it is the obvious way to build the affordance and the obvious
// way to lose the entire gate.
func ReadDecision(s SlackReader, channelID, ts, botID string, allow []string) (Decision, error) {
	var d Decision

	reactions, rErr := s.Reactions(channelID, ts)
	replies, pErr := s.Replies(channelID, ts)
	// Both failing means nothing can be read; one failing is survivable,
	// because either channel alone can carry an approval.
	if rErr != nil && pErr != nil {
		return d, rErr
	}

	permitted := func(user string) bool {
		if user == "" || user == botID {
			return false
		}
		if len(allow) == 0 {
			// An empty allowlist is not "anyone". A channel can contain
			// people who have no idea what they are approving, and a merge
			// gate that any member can satisfy is decoration.
			return false
		}
		for _, a := range allow {
			if strings.EqualFold(strings.TrimPrefix(a, "@"), user) {
				return true
			}
			if strings.EqualFold(a, s.UserName(user)) {
				return true
			}
		}
		return false
	}

	var sawUnpermitted string

	// Rejections first, and they are final. Reading approvals first and
	// returning early would let an approval win a race against an objection
	// raised precisely because the approver was wrong.
	for _, r := range reactions {
		if !contains(rejectEmoji, r.Name) {
			continue
		}
		for _, u := range r.Users {
			if permitted(u) {
				d.Rejected = true
				d.By, d.How = s.UserName(u), ":"+r.Name+":"
				return d, nil
			}
		}
	}
	for _, m := range replies {
		if m.User == botID {
			continue
		}
		if matchesWord(m.Text, rejectWords) && permitted(m.User) {
			d.Rejected = true
			d.By, d.How = s.UserName(m.User), firstLine(m.Text)
			return d, nil
		}
	}

	for _, r := range reactions {
		if !contains(approveEmoji, r.Name) {
			continue
		}
		for _, u := range r.Users {
			if u == botID {
				continue
			}
			if permitted(u) {
				d.Approved = true
				d.By, d.How = s.UserName(u), ":"+r.Name+":"
				return d, nil
			}
			sawUnpermitted = s.UserName(u)
		}
	}
	for _, m := range replies {
		if m.User == botID {
			continue
		}
		if !matchesWord(m.Text, approveWords) || negated(m.Text) {
			continue
		}
		if permitted(m.User) {
			d.Approved = true
			d.By, d.How = s.UserName(m.User), firstLine(m.Text)
			return d, nil
		}
		sawUnpermitted = s.UserName(m.User)
	}

	switch {
	case sawUnpermitted != "":
		d.Why = fmt.Sprintf("%s approved, but is not on the merge allowlist", sawUnpermitted)
	default:
		d.Why = "nobody has approved it yet"
	}
	return d, nil
}

// negated reports whether an approval word is being hedged or denied.
//
// Word-boundary matching is not enough on its own: "I would not approve this
// yet" contains "approve" as a whole word, and reading it as an approval
// merges code over a plain objection. Caught by
// TestApprovalWordsMatchAsWordsNotSubstrings, which failed the first time it
// was run.
//
// Deliberately blunt, and deliberately biased. A merge gate must fail CLOSED:
// the cost of ignoring a genuine approval is that someone reacts with a tick
// instead, while the cost of misreading a hedge is code landing on a shared
// branch against the reviewer's stated intent. A question mark counts too --
// "approve this?" is asking, not answering.
func negated(text string) bool {
	t := " " + strings.ToLower(strings.Join(strings.Fields(text), " ")) + " "
	if strings.Contains(t, "?") {
		return true
	}
	for _, n := range []string{
		" not ", "n't ", " never ", " unless ", " until ", " yet ",
		" but ", " once ", " after ", " if ",
	} {
		if strings.Contains(t, n) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	// Slack reports skin-toned reactions as "+1::skin-tone-3".
	s = strings.SplitN(s, "::", 2)[0]
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// matchesWord looks for an approval word as a WORD, not a substring.
//
// Substring matching would read "no" inside "not sure, hold off" as a
// rejection -- harmless here -- but also read "approve" inside "I would not
// approve this yet" as an approval, which merges code over an objection.
func matchesWord(text string, words []string) bool {
	t := " " + strings.ToLower(strings.Join(strings.Fields(text), " ")) + " "
	for _, w := range words {
		if strings.Contains(t, " "+w+" ") ||
			strings.Contains(t, " "+w+". ") ||
			strings.Contains(t, " "+w+"! ") ||
			strings.Contains(t, " "+w+", ") {
			return true
		}
	}
	return false
}
