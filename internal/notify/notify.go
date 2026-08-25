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
	"net/http"
	"os"
	"os/exec"
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

type Event struct {
	Level     Level     `json:"level"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Workspace string    `json:"workspace,omitempty"`
	At        time.Time `json:"at"`
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
	fmt.Printf("\n[orion:%s] %s\n%s\n\n", e.Level, e.Title, e.Body)

	if err := desktop(e); err != nil {
		errs = append(errs, fmt.Errorf("desktop notify: %w", err))
	}
	if url := strings.TrimSpace(os.Getenv("ORION_NOTIFY_WEBHOOK")); url != "" {
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

// desktop raises a native notification. Silently a no-op where no
// mechanism exists, since a missing notifier is not an error worth
// reporting on every event.
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
