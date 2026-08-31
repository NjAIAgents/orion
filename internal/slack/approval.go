package slack

import (
	"fmt"
	"strings"
)

// Reading an approval back out of Slack.
//
// Posting is enough to ASK for a merge; hearing the answer needs three more
// API surfaces and, importantly, three more OAuth scopes. Those scopes are
// not granted by editing config -- the app must be reinstalled -- so every
// call here reports a missing scope as an instruction rather than an error
// code, because "missing_scope" tells a person nothing about what to click.

// Message is one Slack message, reduced to what an approval needs.
type Message struct {
	TS   string
	User string
	Text string
}

// Reaction is an emoji and who added it.
type Reaction struct {
	Name  string
	Users []string
}

// PostTS sends a message and returns its timestamp.
//
// The timestamp is the whole point: it is the only durable handle on a
// specific message, and without it an approval check would have to scan the
// channel and guess which message was being answered. Orion stores it on the
// ticket so a restarted process can resume watching the same request.
func (c *Client) PostTS(channelID, text string) (string, error) {
	var out struct {
		TS string `json:"ts"`
	}
	if err := c.call("chat.postMessage", map[string]any{
		"channel": channelID,
		"text":    text,
	}, &out); err != nil {
		return "", err
	}
	return out.TS, nil
}

// Reply posts into the thread under one message.
//
// An answer belongs with its question. A fault Orion asked about, confirmed
// fixed and then found still broken has to say so where the person who
// confirmed it is already looking -- a fresh message in the channel reads as
// a second, separate problem, which is exactly the wrong impression.
func (c *Client) Reply(channelID, threadTS, text string) error {
	return c.call("chat.postMessage", map[string]any{
		"channel": channelID, "text": text, "thread_ts": threadTS,
	}, nil)
}

// React adds an emoji to a message, as an affordance for whoever must
// answer it.
//
// Errors are swallowed on purpose: this is a convenience, and the reaction
// most likely to fail -- already_reacted -- means the affordance is already
// there. Failing a merge request because an emoji could not be added would
// be absurd. Requires the reactions:write scope.
func (c *Client) React(channelID, ts, emoji string) {
	_ = c.call("reactions.add", map[string]any{
		"channel": channelID, "timestamp": ts, "name": emoji,
	}, nil)
}

// BotID is this token's own user id, cached after the first lookup.
//
// Needed to exclude Orion's own reactions when reading an approval. Without
// it, the affordances React() adds would be counted as approvals and every
// merge request would approve itself.
func (c *Client) BotID() string {
	if c.botID != "" {
		return c.botID
	}
	id, err := c.AuthTest()
	if err != nil || id == nil {
		return ""
	}
	c.botID = id.UserID
	return c.botID
}

// Reactions returns the emoji on one message.
//
// Requires the reactions:read scope.
func (c *Client) Reactions(channelID, ts string) ([]Reaction, error) {
	var out struct {
		Message struct {
			Reactions []struct {
				Name  string   `json:"name"`
				Users []string `json:"users"`
			} `json:"reactions"`
		} `json:"message"`
	}
	err := c.call("reactions.get", map[string]any{
		"channel": channelID, "timestamp": ts, "full": true,
	}, &out)
	if err != nil {
		return nil, scopeHint(err, "reactions:read", "read emoji reactions")
	}
	var rs []Reaction
	for _, r := range out.Message.Reactions {
		rs = append(rs, Reaction{Name: r.Name, Users: r.Users})
	}
	return rs, nil
}

// Replies returns the thread under a message, excluding the message itself.
//
// Requires channels:history for public channels, groups:history for private
// ones -- and Orion creates private channels by default, so in practice both
// are needed.
func (c *Client) Replies(channelID, ts string) ([]Message, error) {
	var out struct {
		Messages []struct {
			TS   string `json:"ts"`
			User string `json:"user"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	err := c.call("conversations.replies", map[string]any{
		"channel": channelID, "ts": ts, "limit": 50,
	}, &out)
	if err != nil {
		return nil, scopeHint(err, "channels:history and groups:history", "read replies in a thread")
	}
	var msgs []Message
	for _, m := range out.Messages {
		if m.TS == ts {
			continue // the request itself, not an answer to it
		}
		msgs = append(msgs, Message{TS: m.TS, User: m.User, Text: m.Text})
	}
	return msgs, nil
}

// UserName resolves a Slack user id to a display name, for the audit trail.
//
// Best effort by design: an approval that cannot be attributed to a NAME is
// still an approval from a verified id, and failing the merge over a cosmetic
// lookup would be absurd. Falls back to the raw id.
// LookupUser resolves a display name and reports why it could not.
//
// UserName swallows the error because a cosmetic lookup must never fail a
// merge. That is right for the hot path and useless for diagnosis: a name
// that will not resolve looks identical whether the scope is missing, the
// token is stale, or the user simply does not exist. This is the version
// that says which.
func (c *Client) LookupUser(userID string) (string, error) {
	var out struct {
		User struct {
			Name    string `json:"name"`
			Profile struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := c.call("users.info", map[string]any{"user": userID}, &out); err != nil {
		return "", scopeHint(err, "users:read", "resolve a user's display name")
	}
	for _, n := range []string{
		out.User.Profile.DisplayName, out.User.Profile.RealName, out.User.Name,
	} {
		if strings.TrimSpace(n) != "" {
			return n, nil
		}
	}
	return userID, nil
}

func (c *Client) UserName(userID string) string {
	var out struct {
		User struct {
			Name    string `json:"name"`
			Profile struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := c.call("users.info", map[string]any{"user": userID}, &out); err != nil {
		return userID
	}
	for _, n := range []string{
		out.User.Profile.DisplayName, out.User.Profile.RealName, out.User.Name,
	} {
		if strings.TrimSpace(n) != "" {
			return n
		}
	}
	return userID
}

// scopeHint turns Slack's missing_scope into something actionable.
//
// "missing_scope" names nothing a person can act on: not which scope, not
// where to add it, and not the fact that adding it is insufficient without a
// reinstall. Every one of those is a separate place to get stuck.
func scopeHint(err error, scopes, what string) error {
	if err == nil || !strings.Contains(err.Error(), "missing_scope") {
		return err
	}
	return fmt.Errorf("the Slack app cannot %s: it is missing %s.\n"+
		"  Add it at api.slack.com/apps -> your app -> OAuth & Permissions -> Bot Token Scopes,\n"+
		"  then REINSTALL the app to the workspace -- new scopes do not apply to an already-issued\n"+
		"  token, so the existing one keeps failing until it is reissued.\n"+
		"  Finally: orion config --only ORION_SLACK_BOT_TOKEN", what, scopes)
}
