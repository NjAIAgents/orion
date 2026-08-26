package collect

import (
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
)

// mention builds the @-prefix for a message that needs somebody to act.
//
// Slack only notifies a person when their id appears as <@Uxxxx>; a display
// name in the text is plain prose that nobody is told about. So a message
// carefully written to be actionable still arrives silently unless this is
// present.
//
// Applied ONLY to messages that require action -- blocked, failed, approval
// requests. Mentioning on routine events is how a channel gets muted, and a
// muted channel delivers nothing at all: a mention attached to good news
// costs the delivery of the bad.
func mention(cfg config.Config) string {
	ids := cfg.Slack.Mention
	if len(ids) == 0 {
		// Fall back to whoever was invited to the channel. They are the
		// humans in the room, which is the best available answer to "who
		// should hear about this" without asking for the same list twice.
		ids = cfg.Slack.InviteUsers
	}
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		// Only Slack user IDs can be mentioned. An email address in
		// invite_users works for the invite and cannot be turned into a
		// notification, so it is skipped rather than rendered as <@a@b.com>,
		// which Slack shows literally and looks like a bug.
		if id == "" || strings.Contains(id, "@") {
			continue
		}
		if !strings.HasPrefix(id, "<@") {
			id = "<@" + id + ">"
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " ") + " "
}
