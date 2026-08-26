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

	// 3. Membership. A bot cannot post to a channel it is not in, and for a
	// private channel it cannot even see one.
	if err := c.Join(channel); err != nil && !strings.Contains(err.Error(), "already_in_channel") {
		ui.Warn(w, "could not join #%s: %v", name, err)
		fmt.Fprintln(w, "  For a PRIVATE channel a bot cannot join itself: invite it from Slack\n"+
			"  with /invite @"+id.User+" in that channel, then run this again.")
	}

	// 4. The actual send.
	if err := c.Post(channel, "*Orion delivery test*\nIf you can read this, "+
		"notifications for this project will arrive here."); err != nil {
		ui.Fail(w, "the message was rejected: %v", err)
		os.Exit(1)
	}
	ui.Ok(w, "ok", "posted to #%s -- go and look", name)

	// 5. The approval scopes, which are separate and easy to miss because
	// posting works without them.
	checkApprovalScopes(w, c, channel)
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

func resolveTestChannel(key string) (id, name, source string, err error) {
	home := workspace.Home()
	if key != "" {
		entry, lErr := registry.Lookup(home, key)
		if lErr != nil {
			return "", "", "", lErr
		}
		ws, oErr := workspace.Open(entry.Workspace)
		if oErr != nil {
			return "", "", "", oErr
		}
		if ws.Task.Slack != nil && ws.Task.Slack.ID != "" {
			return ws.Task.Slack.ID, ws.Task.Slack.Name, "the workspace record", nil
		}
		cfg := config.Load(ws.RepoDir())
		if !cfg.Slack.Enabled {
			return "", "", "", fmt.Errorf("slack is disabled in %s/orion.json", ws.RepoDir())
		}
		c, _ := slack.FromEnv()
		ch, cErr := c.CreateChannel(cfg.Slack.ChannelPrefix+ws.Task.Slug, cfg.Slack.Private)
		if cErr != nil {
			return "", "", "", cErr
		}
		// Record it, so the next run does not have to work this out again --
		// and so notify, which only sends when a channel is already known,
		// starts working.
		ws.Task.Slack = &workspace.SlackChannel{ID: ch.ID, Name: ch.Name}
		if sErr := ws.SaveTask(); sErr != nil {
			return ch.ID, ch.Name, "Slack (could not record it: " + sErr.Error() + ")", nil
		}
		return ch.ID, ch.Name, "Slack, and recorded on the workspace", nil
	}

	// No key: use the current repository's own configuration.
	root, fErr := config.FindRoot(".")
	if fErr != nil {
		return "", "", "", fmt.Errorf("not inside an Orion project; name one: orion slack test FCIA")
	}
	cfg := config.Load(root)
	if !cfg.Slack.Enabled {
		return "", "", "", fmt.Errorf("slack is disabled in %s/orion.json", root)
	}
	c, _ := slack.FromEnv()
	ch, cErr := c.FindChannel(cfg.Slack.ChannelPrefix+workspace.Slugify(root), cfg.Slack.Private)
	if cErr != nil {
		return "", "", "", cErr
	}
	return ch.ID, ch.Name, "this repository's config", nil
}

// checkApprovalScopes probes the two read surfaces an approval needs.
//
// Reported separately from posting because they fail INDEPENDENTLY: a token
// that can post perfectly well may be unable to read a single reaction, and
// discovering that at the moment someone taps a tick on a merge request is
// the worst possible time.
func checkApprovalScopes(w *os.File, c *slack.Client, channel string) {
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
	if ok {
		ui.Ok(w, "ok", "the approval scopes are granted; merge approvals can be read")
		return
	}
	fmt.Fprintln(w, "  Notifications work without these. Only slack.require_approval needs them.")
}
