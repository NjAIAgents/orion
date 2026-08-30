// Package notify tells the user something happened while they were not
// watching.
//
// Orion runs long and unattended. A quota wall at minute forty of an
// unattended run is exactly the moment a person needs to know, and
// exactly the moment nobody is looking at the terminal.
//
// Every channel is best-effort and non-fatal: a notification that fails
// must never take down the run it was reporting on. Failures are returned
// so the caller can log them, never so the caller can abort.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/ui"
	"runtime"
	"strings"
	"time"
)

// Level shapes how loudly a message is delivered.
type Level string

const (
	Info    Level = "info"
	Warning Level = "warning"
	Blocked Level = "blocked"
)

// Channel, when set, is a Slack channel id the event should also go to.
// Set by the supervisor from the workspace's own channel, so a project's
// messages land in that project's room rather than one shared firehose.

type Event struct {
	Channel string
	// Actor is the stable identifier of whoever this message is about.
	//
	// A Slack message is read with no surrounding context, often on a phone,
	// by somebody who did not watch the run -- the column layout that names
	// the actor in the terminal does not exist there, so the name and job
	// title have to travel inside the message. Empty attributes it to Orion
	// itself.
	Actor string
	// Key is the ticket this message is about, for the terminal echo's key
	// column. Empty renders an empty column rather than a guess: `orion
	// notify` and a quota warning from the supervisor are genuinely about no
	// single ticket, and inventing one would be worse than the blank.
	Key       string    `json:"key,omitempty"`
	Level     Level     `json:"level"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Workspace string    `json:"workspace,omitempty"`
	At        time.Time `json:"at"`
}

// Out is where the always-on terminal line goes. A variable so a test can
// read what a person would see.
var Out io.Writer = os.Stdout

// mrkdwnLink matches Slack's <url|label> syntax, which renders as a link in
// Slack and as literal angle brackets anywhere else.
var mrkdwnLink = regexp.MustCompile(`<([^<>|]+)\|([^<>]+)>`)

// Plain renders Slack mrkdwn as text for a terminal.
//
// Defensive rather than decorative: the title is plain today, and this is
// what keeps it plain when somebody composes the next one for Slack and
// forgets that it is also printed here.
func Plain(s string) string {
	s = mrkdwnLink.ReplaceAllString(s, "$2 ($1)")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "•", "-")
	return strings.TrimSpace(s)
}

// Send delivers on every configured channel and returns the errors that
// occurred, having already tried them all. Partial delivery is normal and
// is not treated as failure by the caller.
func Send(e Event) []error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	var errs []error

	// stdout is the one channel that always runs. If everything else is
	// unconfigured, the terminal and the log still carry the event.
	//
	// ONE LINE, and only the title. The body is composed for Slack -- *bold*
	// with one asterisk, links as <url|label>, bullets -- and echoing it here
	// printed markup meant for a different renderer straight into a terminal:
	//
	//	• pull request  <https://github.com/x/y/pull/3|open it>
	//
	// One body cannot serve both surfaces, which is how that happened. The
	// terminal gets a summary and Slack carries the formatted version; the
	// detail the terminal lost is already in the run's own output, line by
	// line, as it happened.
	//
	// TITLE-ONLY IS DEFENDED ABOVE. The COLUMNS were not, and were simply
	// incidental: this used to print `[orion:info] <title>` straight to Out,
	// so an echo landed in the middle of a run with no timestamp, no ticket
	// key, no icon and no verb, breaking the one alignment the log has
	// (OR-189). It renders through internal/ui now, in the same columns as
	// every other line, and the level is carried by the verb column -- which
	// is the axis this renderer already has for exactly that question.
	ui.Print(Out, ui.Line{
		At: e.At, Key: e.Key, Actor: echoActor(e),
		Verb: verbFor(e.Level),
		// Prefixed, because the echo is not a status line: several callers
		// print their own `ui.Say` about the same fact a line earlier, and
		// without this the two read as two separate things happening.
		Msg: "notified: " + Plain(e.Title),
	})

	if err := desktopSend(e); err != nil {
		errs = append(errs, fmt.Errorf("desktop notify: %w", err))
	}
	if e.Channel != "" {
		text := "*" + actors.Attribution(echoActor(e)) + "*\n" + e.Title + "\n" + e.Body
		if err := slackSend(e.Channel, text, e.Level); err != nil {
			errs = append(errs, fmt.Errorf("slack: %w", err))
		}
	}
	// Resolved through the same hook as the Slack token, so a webhook stored
	// in Orion's config file works under cron where a shell profile does not.
	if url := webhookURL(); url != "" {
		if err := webhook(url, e); err != nil {
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		}
	}
	if cmd := strings.TrimSpace(os.Getenv("ORION_NOTIFY_COMMAND")); cmd != "" {
		if err := custom(cmd, e); err != nil {
			errs = append(errs, fmt.Errorf("notify command: %w", err))
		}
	}
	return errs
}

// echoActor is who a message is about. An event with no actor is Orion's own
// and must still be attributed rather than arriving anonymously.
func echoActor(e Event) string {
	if e.Actor == "" {
		return events.ActorOrion
	}
	return e.Actor
}

// verbFor puts the notification's level in the renderer's status column.
//
// Many-to-few, the same way ui.VerbFor is: the column answers "do I have to
// do something", and a level answers exactly that question already. blocked
// means a person is needed, which is what `failed` says here; info means it
// worked and is being reported.
func verbFor(l Level) string {
	switch l {
	case Warning:
		return ui.VerbWarn
	case Blocked:
		return ui.VerbFail
	}
	return ui.VerbOK
}

// slackSend posts to a channel. A package variable rather than a direct call
// so the delivery path can be exercised without a live workspace -- and it
// was the absence of that seam that let the bug below survive untested.
//
// The bug: this used to be `if c, err := slack.FromEnv(); err == nil { ... }`,
// which DISCARDED the error. A caller that asked for a Slack notification
// with a missing or malformed token got no message and no error, so the
// alert silently did not arrive and nothing said so. For a package whose
// entire job is telling you something happened while you were not watching,
// that is the worst possible failure.
//
// level travels through to Slack rather than being dropped, which is what
// used to happen here: Level was computed on every Event and then Post took
// only a channel and text, so an approval request and a status update
// arrived looking identical.
var slackSend = func(channel, text string, level Level) error {
	c, err := slack.FromEnv()
	if err != nil {
		return err
	}
	return c.PostLevel(channel, slack.Level(level), text)
}

// SetSlackSender replaces the Slack delivery function. Used by tests, and
// available to a caller that already holds a configured client.
func SetSlackSender(f func(channel, text string, level Level) error) func(channel, text string, level Level) error {
	prev := slackSend
	if f != nil {
		slackSend = f
	}
	return prev
}

// desktopSend raises a native notification. Silently a no-op where no
// mechanism exists, since a missing notifier is not an error worth
// reporting on every event. A var, like slackSend, so a test can replace it:
// the real desktop() shells out to whatever notifier the host has, and on a
// headless CI runner that binary can exist yet fail to actually deliver
// (no display session), which is a real error a production caller should
// see -- but not one a test asserting an exact error count should have to
// tolerate.
var desktopSend = desktop

// SetDesktopSender replaces the desktop delivery function. Used by tests.
func SetDesktopSender(f func(Event) error) func(Event) error {
	prev := desktopSend
	if f != nil {
		desktopSend = f
	}
	return prev
}

// webhookURL is supplied by the CLI at startup; the fallback keeps the
// environment working when nothing set a resolver.
var webhookURL = func() string { return strings.TrimSpace(os.Getenv("ORION_NOTIFY_WEBHOOK")) }

// SetWebhookResolver wires in credential lookup without importing workspace.
func SetWebhookResolver(f func() string) {
	if f != nil {
		webhookURL = f
	}
}

func desktop(e Event) error {
	title := "Orion"
	if e.Workspace != "" {
		title = "Orion: " + e.Workspace
	}
	body := firstLine(e.Body)

	switch runtime.GOOS {
	case "darwin":
		bin, err := exec.LookPath("osascript")
		if err != nil {
			return nil
		}
		script := fmt.Sprintf("display notification %s with title %s",
			appleQuote(body), appleQuote(title))
		return exec.Command(bin, "-e", script).Run()

	case "linux":
		bin, err := exec.LookPath("notify-send")
		if err != nil {
			return nil
		}
		urgency := "normal"
		if e.Level == Blocked {
			urgency = "critical"
		}
		return exec.Command(bin, "-u", urgency, title, body).Run()

	case "windows":
		bin, err := exec.LookPath("powershell")
		if err != nil {
			return nil
		}
		ps := fmt.Sprintf(
			`[reflection.assembly]::loadwithpartialname('System.Windows.Forms') > $null;`+
				`$n = New-Object System.Windows.Forms.NotifyIcon;`+
				`$n.Icon = [System.Drawing.SystemIcons]::Information;`+
				`$n.Visible = $true; $n.ShowBalloonTip(10000, %s, %s, 'Info')`,
			psQuote(title), psQuote(body))
		return exec.Command(bin, "-NoProfile", "-Command", ps).Run()
	}
	return nil
}

// webhook posts the event as JSON, with a Slack-compatible "text" field so
// a raw Slack incoming webhook URL works with no adapter.
func webhook(url string, e Event) error {
	payload := map[string]any{
		"text":      fmt.Sprintf("*%s*\n%s", e.Title, e.Body),
		"level":     string(e.Level),
		"title":     e.Title,
		"body":      e.Body,
		"workspace": e.Workspace,
		"at":        e.At.Format(time.RFC3339),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}

// custom runs a user-supplied command with the event in the environment,
// for anyone whose notification path is not a webhook.
func custom(command string, e Event) error {
	shell, flag := "/bin/sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	}
	cmd := exec.Command(shell, flag, command)
	cmd.Env = append(os.Environ(),
		"ORION_EVENT_LEVEL="+string(e.Level),
		"ORION_EVENT_TITLE="+e.Title,
		"ORION_EVENT_BODY="+e.Body,
		"ORION_EVENT_WORKSPACE="+e.Workspace,
	)
	return cmd.Run()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// appleQuote escapes for AppleScript string literals.
func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// psQuote escapes for PowerShell single-quoted strings.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
