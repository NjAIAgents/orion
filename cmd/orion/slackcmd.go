package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// orion slack test -- prove delivery end to end, or say exactly what broke.
//
// This exists because "no message arrived" had six possible causes and
// nothing distinguished them: no token, a bad token, Slack disabled in
// orion.json, no channel recorded on the workspace, the bot not in the
// channel, or a missing scope. Every one of those produced the same silence
// and the same desktop-notification fallback, so the visible symptom was
// always "Slack does not work" and never the one thing to fix.
//
// It sends a REAL message. A dry run would test everything except the part
// that keeps failing.
func runSlackCmd(args []string) {
	w := os.Stdout
	if len(args) == 0 || args[0] != "test" {
		fmt.Fprintln(os.Stderr, "usage: orion slack test [KEY]")
		os.Exit(64)
	}

	var key string
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "-") {
			key = strings.ToUpper(a)
		}
	}

	// 1. A token at all, and whose.
	c, err := slack.FromEnv()
	if err != nil {
		ui.Fail(w, "%v", err)
		fmt.Fprintln(w, "  Set one with: orion config --only ORION_SLACK_BOT_TOKEN")
		os.Exit(1)
	}
	id, err := c.AuthTest()
	if err != nil {
		ui.Fail(w, "the token is present but Slack rejected it: %v", err)
		fmt.Fprintln(w, "  A revoked or rotated token fails exactly like this.\n"+
			"  Reissue it at api.slack.com/apps -> OAuth & Permissions -> Install to Workspace.")
		os.Exit(1)
	}
	ui.Ok(w, "bound", "%s in %s (bot id %s)", id.User, id.Team, id.UserID)

	// 2. Which channel, and where that answer came from -- the most common
	// failure is a channel that was created but never recorded, so nothing
	// ever asked Slack to deliver anything.
	channel, name, source, cfgErr := resolveTestChannel(key)
	if cfgErr != nil {
		ui.Fail(w, "%v", cfgErr)
		os.Exit(1)
	}
	ui.Ok(w, "resolved", "#%s (%s) from %s", name, channel, source)

	// 3. The actual send. Attempted BEFORE any membership check, because the
	// send is the only thing that actually answers the question.
	//
	// This used to call conversations.join first and warn when it failed --
	// which it always does for a private channel, since that API does not
	// support them at all. So every successful test printed a warning about
	// a problem that did not exist, directly above the line saying it had
	// worked. A warning that fires on the happy path is worse than no
	// warning: it is the one people learn to scroll past.
	if err := c.Post(channel, "*Orion delivery test*\nIf you can read this, "+
		"notifications for this project will arrive here."); err != nil {
		ui.Fail(w, "the message was rejected: %v", err)
		if strings.Contains(err.Error(), "not_in_channel") ||
			strings.Contains(err.Error(), "channel_not_found") {
			fmt.Fprintf(w, "  The bot is not in #%s. A bot cannot add itself to a PRIVATE\n"+
				"  channel, so invite it from Slack: /invite @%s\n", name, id.User)
		}
		os.Exit(1)
	}
	ui.Ok(w, "ok", "posted to #%s", name)

	// 4. Is anyone in the room?
	//
	// A successful post is NOT proof a person will see it. Slack accepted
	// every message Orion sent to a private channel whose only member was the
	// bot: delivered, stored, unreadable. The channel does not appear in the
	// sidebar, does not appear in search, and generates no notification, so
	// from the outside it is indistinguishable from Slack being broken --
	// which is precisely how it was diagnosed, repeatedly, as Slack being
	// broken.
	//
	// This is the check that separates "the message did not send" from "the
	// message sent to a room you are not in", and those have opposite fixes.
	checkAudience(w, c, channel, name, id.UserID)

	// 5. The approval scopes, which are separate and easy to miss because
	// posting works without them.
	checkApprovalScopes(w, c, channel, id.UserID)
}

// humansAmong returns the members who are not the bot itself.
//
// Split out so the judgement can be tested without a Slack workspace. The
// judgement is the whole point: "the channel has one member" is fine if that
// member is a person and catastrophic if it is the bot, and those two cases
// differ by one string comparison that nothing was checking.
func humansAmong(members []string, botID string) []string {
	var people []string
	for _, m := range members {
		if m != botID && strings.TrimSpace(m) != "" {
			people = append(people, m)
		}
	}
	return people
}

// checkAudience reports whether a human can actually read what was posted.
func checkAudience(w *os.File, c *slack.Client, channel, name, botID string) {
	members, err := c.Members(channel)
	if err != nil {
		// Not fatal. Missing the read scope means this check cannot run; it
		// does not mean the channel is empty, and claiming so would be worse
		// than saying nothing.
		ui.Warn(w, "could not check who is in #%s: %v", name, err)
		return
	}
	people := humansAmong(members, botID)
	if len(people) > 0 {
		ui.Ok(w, "ok", "%d person/people can read #%s -- go and look", len(people), name)
		return
	}
	ui.Fail(w, "#%s has no members except the bot, so NOBODY can read that message.", name)
	fmt.Fprintf(w, "  The post above succeeded. Slack accepted it. It is sitting in a private\n"+
		"  channel you are not in, which is invisible to you -- not in the sidebar,\n"+
		"  not in search, with no notification that it exists.\n\n"+
		"  Fix it one of two ways:\n"+
		"    - add your Slack user ID to slack.invite_users in orion.json, or\n"+
		"    - point the workspace record at a channel you ARE in\n")
}

// recordChannel writes the channel onto the sandbox workspace bound to a
// repository, best effort.
//
// Best effort because adoption has already succeeded by this point and a
// failure here costs notifications, not correctness -- and because the
// sandbox may not exist yet on a first init, in which case the work run's
// own resolver records it instead. Silence on the happy path; a warning only
// when a workspace exists and could not be written.
func recordChannel(dir string, ch *slack.Channel) {
	ws := workspace.FindBySource(dir)
	if ws == nil {
		return
	}
	if ws.Task.Slack != nil && ws.Task.Slack.ID == ch.ID {
		return
	}
	ws.Task.Slack = &workspace.SlackChannel{ID: ch.ID, Name: ch.Name}
	if err := ws.SaveTask(); err != nil {
		ui.Warn(os.Stdout, "could not record the Slack channel on %s: %v\n"+
			"  Notifications will resolve it again on the next run.", ws.ID, err)
	}
}

// resolveTestChannel answers the same question a run answers, the same way.
//
// It used to answer it two different ways. With a KEY it read the workspace
// record; without one it skipped the record entirely and re-derived a channel
// name from config -- so the test and the thing it tests could disagree, and
// did. A passing `orion slack test` proved nothing about where `orion work`
// would actually post.
//
// Worse, the no-key branch slugified the repository's ABSOLUTE PATH, so
// standing in the fcia checkout asked Slack for a channel called
// "users-navjyotnishant-desktop-github-njai" -- correctly not found, and the
// error named a channel nobody had ever created or could recognise.
//
// Both branches now end in one resolver, which reads the workspace record
// first exactly like work.resolveChannel does.
func resolveTestChannel(key string) (id, name, source string, err error) {
	if key != "" {
		entry, lErr := registry.Lookup(workspace.Home(), key)
		if lErr != nil {
			return "", "", "", lErr
		}
		ws, oErr := workspace.Open(entry.Workspace)
		if oErr != nil {
			return "", "", "", oErr
		}
		return channelFor(ws)
	}

	// No key: the repository you are standing in, resolved through ITS
	// workspace, so this reports what a run from here would really use.
	root, fErr := config.FindRoot(".")
	if fErr != nil {
		return "", "", "", fmt.Errorf("not inside an Orion project; name one: orion slack test FCIA")
	}
	ws := workspace.FindBySource(root)
	if ws == nil {
		return "", "", "", fmt.Errorf(
			"%s has no Orion sandbox, so there is no channel bound to it yet.\n"+
				"  Run orion init here, or name a registered project: orion slack test FCIA", root)
	}
	return channelFor(ws)
}

// channelFor mirrors work.resolveChannel: the recorded channel wins, and
// config is consulted only when there is nothing recorded.
//
// The precedence is worth stating out loud because it caused a whole evening
// of silence. Editing channel_prefix in orion.json does NOTHING once a
// channel has been recorded -- the record wins, forever, and the only
// evidence is messages arriving somewhere nobody is looking. So the source
// string here is not decoration; it is the difference between "your config
// is wrong" and "your config is irrelevant".
func channelFor(ws *workspace.Workspace) (id, name, source string, err error) {
	if ws.Task.Slack != nil && ws.Task.Slack.ID != "" {
		return ws.Task.Slack.ID, ws.Task.Slack.Name,
			"the workspace record (this WINS over orion.json; edit " +
				ws.TaskPath() + " to change it)", nil
	}
	cfg := config.Load(ws.RepoDir())
	if !cfg.Slack.Enabled {
		return "", "", "", fmt.Errorf("slack is disabled in %s/orion.json", ws.RepoDir())
	}
	c, cliErr := slack.FromEnv()
	if cliErr != nil {
		return "", "", "", cliErr
	}
	ch, cErr := c.CreateChannel(cfg.Slack.ChannelPrefix+ws.Task.Slug, cfg.Slack.Private)
	if cErr != nil {
		return "", "", "", cErr
	}
	// Record it, so the next run does not have to work this out again -- and
	// so notify, which only sends when a channel is already known, starts
	// working.
	ws.Task.Slack = &workspace.SlackChannel{ID: ch.ID, Name: ch.Name}
	if sErr := ws.SaveTask(); sErr != nil {
		return ch.ID, ch.Name, "Slack (could not record it: " + sErr.Error() + ")", nil
	}
	return ch.ID, ch.Name, "Slack, and recorded on the workspace", nil
}

// checkApprovalScopes probes the two read surfaces an approval needs.
//
// Reported separately from posting because they fail INDEPENDENTLY: a token
// that can post perfectly well may be unable to read a single reaction, and
// discovering that at the moment someone taps a tick on a merge request is
// the worst possible time.
func checkApprovalScopes(w *os.File, c *slack.Client, channel, botID string) {
	fmt.Fprintln(w)
	ok := true
	if _, err := c.Reactions(channel, "0000000000.000000"); err != nil &&
		strings.Contains(err.Error(), "missing") {
		ui.Warn(w, "%v", err)
		ok = false
	}
	if _, err := c.Replies(channel, "0000000000.000000"); err != nil &&
		strings.Contains(err.Error(), "missing") {
		ui.Warn(w, "%v", err)
		ok = false
	}
	// Names are a THIRD, independent scope. Posting works without it,
	// reactions work without it, and an approval will still be honoured by
	// user ID -- so the only symptom is a message naming a raw Uxxxx, which
	// looks like a bug in Orion rather than a missing permission.
	if name, err := c.LookupUser(botID); err != nil {
		ui.Warn(w, "%v", err)
		ok = false
	} else {
		ui.Ok(w, "ok", "names resolve (this bot reads as %q)", name)
	}

	if ok {
		ui.Ok(w, "ok", "the approval scopes are granted; merge approvals can be read")
		return
	}
	fmt.Fprintln(w, "  Notifications work without these. Only slack.require_approval needs them.")
}
