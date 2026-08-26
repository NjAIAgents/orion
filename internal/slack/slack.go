// Package slack talks to the Slack Web API with a bot token.
//
// Why this exists alongside the webhook in internal/notify: an incoming
// webhook CANNOT create a channel. It is bound at creation to exactly one
// channel and has no other API surface. A channel per project therefore
// needs a real Slack app with a bot token, which is a different and heavier
// integration, and saying so up front is kinder than discovering it after
// setting one up.
//
// Two behaviours of the Web API shape everything here:
//
// It returns HTTP 200 on failure. An error arrives as {"ok":false,"error":
// "name_taken"} with a 200 status, so any client that checks only the status
// code reports success for every failure. Every call here checks `ok`.
//
// Channel names are constrained: lowercase, no spaces or periods, at most 80
// characters, and only letters, digits, hyphens and underscores. Slack
// rejects anything else rather than normalising it, so normalisation happens
// before the call.
package slack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
)

const api = "https://slack.com/api/"

// Client is a bot-token Slack client.
type Client struct {
	Token string
	HTTP  *http.Client
}

// FromEnv builds a client from ORION_SLACK_TOKEN.
//
// A bot token starts xoxb-. A user token (xoxp-) or an incoming webhook URL
// pasted here will fail in confusing ways later, so both are rejected now
// with an explanation rather than at the first API call.
func FromEnv() (*Client, error) {
	tok := strings.TrimSpace(os.Getenv("ORION_SLACK_TOKEN"))
	if tok == "" {
		return nil, fmt.Errorf("ORION_SLACK_TOKEN is not set.\n" +
			"  A channel per project needs a Slack app bot token (xoxb-), not an\n" +
			"  incoming webhook: a webhook is bound to one channel and cannot create any.\n" +
			"  Scopes required: channels:manage (public) or groups:write (private),\n" +
			"  plus chat:write.")
	}
	if strings.HasPrefix(tok, "https://") {
		return nil, fmt.Errorf("ORION_SLACK_TOKEN looks like a webhook URL.\n" +
			"  Webhooks cannot create channels. Use a bot token (xoxb-) from a Slack app,\n" +
			"  and keep the webhook in ORION_NOTIFY_WEBHOOK if you still want it.")
	}
	if !strings.HasPrefix(tok, "xoxb-") {
		// Not fatal: workspaces do issue other token types, and refusing one
		// that might work would be worse than warning at the point of use.
		fmt.Fprintln(os.Stderr,
			"orion: ORION_SLACK_TOKEN does not start with xoxb-; a bot token is expected")
	}
	return &Client{Token: tok, HTTP: &http.Client{Timeout: 20 * time.Second}}, nil
}

func (c *Client) call(method string, payload any, out any) error {
	var body io.Reader
	contentType := "application/json; charset=utf-8"
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest("POST", api+method, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", contentType)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	// The envelope check that matters: Slack answers 200 even when it failed.
	var env struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: unparseable response: %s", method, snippet(raw))
	}
	if !env.OK {
		return &APIError{Method: method, Code: env.Error}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// APIError carries Slack's error code so callers can branch on it rather
// than matching substrings of a message.
type APIError struct {
	Method string
	Code   string
}

func (e *APIError) Error() string {
	hint := ""
	switch e.Code {
	case "missing_scope", "not_allowed_token_type":
		hint = "\n  The token lacks a required scope. Creating channels needs channels:manage" +
			"\n  (public) or groups:write (private); posting needs chat:write." +
			"\n  Add the scope in the Slack app config, then REINSTALL the app: a scope" +
			"\n  added without reinstalling does not reach the existing token."
	case "name_taken":
		hint = "\n  A channel with that name already exists."
	case "invalid_name", "invalid_name_specials", "invalid_name_maxlength", "invalid_name_punctuation":
		hint = "\n  Slack rejected the channel name. Names must be lowercase, 80 characters" +
			"\n  or fewer, and contain only letters, digits, hyphens and underscores."
	case "invalid_auth", "account_inactive", "token_revoked":
		hint = "\n  The token is not valid. Check ORION_SLACK_TOKEN, or reinstall the app."
	case "ratelimited":
		hint = "\n  Rate limited by Slack. Retry shortly."
	case "not_in_channel", "channel_not_found":
		hint = "\n  The bot is not a member of that channel. It joins public channels" +
			"\n  automatically, but Slack provides no way to self-join a PRIVATE one:" +
			"\n  invite it in Slack with /invite @Orion, then re-run."
	}
	return "slack " + e.Method + ": " + e.Code + hint
}

// Identity is what auth.test reports, used by doctor.
type Identity struct {
	OK     bool   `json:"ok"`
	Team   string `json:"team"`
	TeamID string `json:"team_id"`
	User   string `json:"user"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
}

// AuthTest verifies the token and names the workspace it belongs to.
// Naming the workspace matters: a token for the wrong workspace authenticates
// perfectly and posts into somewhere nobody is reading.
func (c *Client) AuthTest() (*Identity, error) {
	var id Identity
	if err := c.call("auth.test", map[string]any{}, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

// Channel is a created or resolved conversation.
type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created bool   `json:"-"` // false when an existing channel was reused
}

// CreateChannel makes a channel, or returns the existing one of that name.
//
// name_taken is treated as success, not failure. Provisioning must be
// idempotent: re-running `orion new` for a slug that already has a channel
// should attach to it rather than erroring, and a half-provisioned workspace
// is worse than a reused channel.
func (c *Client) CreateChannel(name string, private bool) (*Channel, error) {
	name = NormalizeChannelName(name)
	var res struct {
		Channel Channel `json:"channel"`
	}
	err := c.call("conversations.create", map[string]any{
		"name": name, "is_private": private,
	}, &res)
	if err == nil {
		res.Channel.Created = true
		return &res.Channel, nil
	}
	var apiErr *APIError
	if ok := asAPIError(err, &apiErr); ok && apiErr.Code == "name_taken" {
		found, findErr := c.FindChannel(name, private)
		if findErr != nil {
			return nil, fmt.Errorf("%s exists but could not be resolved: %w", name, findErr)
		}
		// A bot is automatically a member of a channel it CREATED, but not
		// of one it merely found. conversations.setTopic requires membership
		// and chat.postMessage to a public channel needs either membership or
		// chat:write.public, so a reused channel must be joined first or
		// every later call fails with not_in_channel.
		//
		// A private channel cannot be self-joined: Slack has no API for it,
		// by design. Someone has to invite the bot.
		if !private {
			if joinErr := c.Join(found.ID); joinErr != nil {
				return found, fmt.Errorf(
					"found #%s but could not join it: %w\n"+
						"  Add the channels:join scope, or invite the bot in Slack with /invite @Orion",
					found.Name, joinErr)
			}
		}
		return found, nil
	}
	return nil, err
}

// Join adds the bot to a public channel.
//
// Only public channels. Slack provides no way for an app to add itself to a
// private channel, deliberately: a human must invite it. That is a policy
// choice rather than a gap, and pretending otherwise would produce a
// confusing failure instead of a clear instruction.
func (c *Client) Join(channelID string) error {
	return c.call("conversations.join", map[string]any{"channel": channelID}, nil)
}

// FindChannel locates a channel by name.
//
// Paginates. A workspace with more than a few hundred channels returns the
// first page only, and stopping there would report "not found" for a channel
// that plainly exists.
func (c *Client) FindChannel(name string, private bool) (*Channel, error) {
	name = NormalizeChannelName(name)
	types := "public_channel"
	if private {
		types = "private_channel"
	}
	cursor := ""
	for page := 0; page < 20; page++ {
		var res struct {
			Channels []Channel `json:"channels"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		payload := map[string]any{"types": types, "limit": 200, "exclude_archived": true}
		if cursor != "" {
			payload["cursor"] = cursor
		}
		if err := c.call("conversations.list", payload, &res); err != nil {
			return nil, err
		}
		for _, ch := range res.Channels {
			if ch.Name == name {
				return &Channel{ID: ch.ID, Name: ch.Name}, nil
			}
		}
		cursor = res.Meta.NextCursor
		if cursor == "" {
			break
		}
	}
	return nil, fmt.Errorf("channel %q not found", name)
}

// Post sends a message to a channel id.
func (c *Client) Post(channelID, text string) error {
	return c.call("chat.postMessage", map[string]any{
		"channel": channelID,
		"text":    text,
	}, nil)
}

// SetTopic gives the channel a one-line description of what it is for.
func (c *Client) SetTopic(channelID, topic string) error {
	if len([]rune(topic)) > 250 {
		topic = string([]rune(topic)[:249]) + "…"
	}
	return c.call("conversations.setTopic", map[string]any{
		"channel": channelID, "topic": topic,
	}, nil)
}

// Archive closes a finished project's channel.
//
// Archive rather than delete, deliberately: channels accumulate exactly like
// the Jira projects do, and archiving keeps the record while removing the
// clutter. Deletion needs admin scopes and destroys the history that made
// the channel worth having.
func (c *Client) Archive(channelID string) error {
	return c.call("conversations.archive", map[string]any{"channel": channelID}, nil)
}

// NormalizeChannelName makes a slug Slack will accept.
//
// Slack rejects rather than normalises, so this must be exact: lowercase,
// only letters, digits, hyphens and underscores, no leading or trailing
// separator, at most 80 characters.
func NormalizeChannelName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastSep := true // treat the start as a separator so leading dashes drop
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) && r < 128, unicode.IsDigit(r):
			b.WriteRune(r)
			lastSep = false
		case r == '-' || r == '_' || r == ' ' || r == '.' || r == '/':
			if !lastSep {
				b.WriteRune('-')
				lastSep = true
			}
		default:
			// Non-ASCII letters are dropped rather than transliterated:
			// guessing a romanisation would produce a name the user did not
			// choose and cannot predict.
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 80 {
		out = strings.Trim(out[:80], "-_")
	}
	if out == "" {
		out = "orion"
	}
	return out
}

// ChannelURL is the deep link, so a report can point at the channel.
func ChannelURL(teamID, channelID string) string {
	if teamID == "" || channelID == "" {
		return ""
	}
	return fmt.Sprintf("https://app.slack.com/client/%s/%s",
		url.PathEscape(teamID), url.PathEscape(channelID))
}

func asAPIError(err error, target **APIError) bool {
	if e, ok := err.(*APIError); ok {
		*target = e
		return true
	}
	return false
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 160 {
		s = s[:159] + "…"
	}
	return s
}
