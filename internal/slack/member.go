package slack

import (
	"fmt"
	"regexp"
	"strings"
)

// Resolving a configured person to the id a MENTION needs.
//
// approval.go resolves the other direction -- an id to a name -- because that
// is what reading an approval back needs. This is the direction that lets
// Orion ask in the first place: slack.merge_approvers holds whatever a person
// could copy (an id, a username, a display name, an email address), and only
// the raw member id notifies. <@U012ABCDEF> raises a notification; the same
// person's name in the message text is styled like any other word and reaches
// nobody, which is how an approval request came to wait on somebody who was
// never told it existed.

// memberIDPattern is Slack's id shape: U for a person, W for an
// Enterprise-Grid person, then uppercase alphanumerics. Recognising one
// avoids a lookup for the form that already is what we need.
var memberIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)

// broadcasts are the mentions that reach everybody in the room.
//
// Refused rather than resolved. The allowlist names who may APPROVE, and a
// channel -- especially one created per project -- contains people with no
// standing to answer; tagging them is noise that teaches the room to mute the
// channel, which costs the approval request the only advantage it has.
var broadcasts = map[string]bool{"channel": true, "here": true, "everyone": true}

// memberLookup is one resolution attempt, remembered whether it succeeded.
//
// Failures are cached too: a typo in merge_approvers resolves to nothing on
// every message otherwise, which is an API call per approver per message for
// an answer that will not change during the run.
type memberLookup struct {
	id  string
	err error
}

// MemberID resolves a configured approver to the member id a mention needs.
//
// Accepts a member id, a username, a display name or an email address --
// every form slack.merge_approvers documents. An email needs the
// users:read.email scope and a name needs users:read, so failure here is an
// ordinary outcome rather than an exception: the caller falls back to naming
// the person in plain text, exactly as before.
//
// Cached on the client, including the workspace directory a name lookup
// needs, so a run resolves each approver at most once.
func (c *Client) MemberID(who string) (string, error) {
	who = normalizeMember(who)
	if who == "" {
		return "", fmt.Errorf("empty approver")
	}
	if broadcasts[strings.ToLower(who)] {
		return "", fmt.Errorf("@%s addresses the whole channel rather than a person, "+
			"and only the people on slack.merge_approvers can approve", strings.ToLower(who))
	}
	if c.members == nil {
		c.members = map[string]memberLookup{}
	}
	if got, ok := c.members[who]; ok {
		return got.id, got.err
	}
	id, err := c.lookupMember(who)
	c.members[who] = memberLookup{id: id, err: err}
	return id, err
}

// normalizeMember strips the decoration a person copies out of Slack, so
// "<@U1>", "@navjyot" and "navjyot" all arrive here as the bare value. The
// same three forms the allowlist check already accepts.
func normalizeMember(who string) string {
	who = strings.TrimSpace(who)
	who = strings.TrimSuffix(strings.TrimPrefix(who, "<@"), ">")
	who = strings.TrimPrefix(who, "@")
	return strings.TrimSpace(who)
}

func (c *Client) lookupMember(who string) (string, error) {
	if memberIDPattern.MatchString(who) {
		return who, nil
	}
	if strings.Contains(who, "@") {
		id, err := c.LookupUserByEmail(who)
		if err != nil {
			return "", scopeHint(err, "users:read.email",
				"resolve an email address to the member id a mention needs")
		}
		return id, nil
	}
	dir, err := c.userDirectory()
	if err != nil {
		return "", err
	}
	if id, ok := dir[strings.ToLower(who)]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no Slack user is named %q", who)
}

// userDirectory maps every name a person could have been configured under --
// username, display name, real name -- to their member id.
//
// One call for the whole workspace rather than one per approver: there is no
// API that resolves a NAME to an id, so the only way to answer is to read the
// list and match. Cached, error included, because the answer will not change
// mid-run and a workspace that cannot be listed cannot be listed twice either.
func (c *Client) userDirectory() (map[string]string, error) {
	if c.directory != nil || c.directoryErr != nil {
		return c.directory, c.directoryErr
	}
	dir := map[string]string{}
	cursor := ""
	for page := 0; page < 20; page++ {
		var res struct {
			Members []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Deleted bool   `json:"deleted"`
				IsBot   bool   `json:"is_bot"`
				Profile struct {
					DisplayName string `json:"display_name"`
					RealName    string `json:"real_name"`
				} `json:"profile"`
			} `json:"members"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		payload := map[string]any{"limit": 200}
		if cursor != "" {
			payload["cursor"] = cursor
		}
		if err := c.call("users.list", payload, &res); err != nil {
			c.directoryErr = scopeHint(err, "users:read",
				"resolve a username to the member id a mention needs")
			return nil, c.directoryErr
		}
		for _, m := range res.Members {
			// A deleted account and a bot are both names that would resolve
			// and then notify nobody who can approve.
			if m.Deleted || m.IsBot || m.ID == "" {
				continue
			}
			for _, n := range []string{m.Name, m.Profile.DisplayName, m.Profile.RealName} {
				n = strings.ToLower(strings.TrimSpace(n))
				// First writer wins: a username is Slack's unique handle,
				// while two people can share a real name, so the earlier and
				// more specific match must not be overwritten by a later one.
				if n != "" && dir[n] == "" {
					dir[n] = m.ID
				}
			}
		}
		cursor = res.Meta.NextCursor
		if cursor == "" {
			break
		}
	}
	c.directory = dir
	return dir, nil
}
