package creds

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Prompt reads one value from the user.
//
// Secrets are read with terminal echo disabled, so a token does not end up in
// scrollback, in a screen share, or in a terminal-recording session. The
// suppression shells out to stty rather than pulling in golang.org/x/term:
// Orion builds with an empty go.sum and adding a dependency for one syscall
// is a poor trade.
//
// If echo cannot be disabled (no tty, or Windows), the value is still read,
// but the user is told it will be visible. Silently echoing a secret while
// implying otherwise would be worse than not trying.
func Prompt(in io.Reader, out io.Writer, label, current string, secret bool) (string, error) {
	shown := current
	if secret && current != "" {
		shown = Mask(current)
	}
	if current != "" {
		fmt.Fprintf(out, "%s\n  current: %s\n  new (enter to keep): ", label, shown)
	} else {
		fmt.Fprintf(out, "%s\n  value (enter to skip): ", label)
	}

	restore := func() {}
	if secret {
		if off := echoOff(); off != nil {
			restore = off
		} else {
			fmt.Fprint(out, "\n  (terminal echo could not be disabled; input will be visible)\n  > ")
		}
	}

	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	restore()
	if secret {
		fmt.Fprintln(out) // the newline the user's Enter did not echo
	}
	if err != nil && line == "" {
		return current, err
	}

	v := strings.TrimSpace(line)
	if v == "" {
		return current, nil
	}
	// A pasted `export KEY='value'` is a very likely input, since that is the
	// shape everyone has been copying around. Accept it rather than storing
	// the whole line as the value.
	if strings.HasPrefix(v, "export ") {
		if _, after, ok := strings.Cut(v, "="); ok {
			v = unquote(strings.TrimSpace(after))
		}
	}
	return unquote(v), nil
}

// echoOff disables terminal echo and returns a function that restores it.
// Returns nil when it cannot be done, so the caller can warn instead.
func echoOff() func() {
	if !isTTY() {
		return nil
	}
	if err := stty("-echo"); err != nil {
		return nil
	}
	return func() { _ = stty("echo") }
}

func stty(arg string) error {
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// isTTY reports whether stdin is a terminal. Without this, a piped or
// redirected run would try to change terminal modes that do not exist.
func isTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Interactive reports whether prompting is possible at all. A wizard invoked
// from cron must refuse rather than block forever on a read that will never
// return.
func Interactive() bool { return isTTY() }

// Label describes a key for the wizard, with the hint that prevents the most
// common mistake for that particular credential.
func Label(key string) string {
	switch key {
	case JiraURL:
		return "Jira base URL\n  e.g. https://yourorg.atlassian.net (no trailing slash)"
	case JiraEmail:
		return "Jira account email\n  must be the account the API token was created under, or auth fails as invalid_auth"
	case JiraToken:
		return "Jira API token\n  from id.atlassian.com/manage-profile/security/api-tokens"
	case SlackToken:
		return "Slack bot token\n  starts xoxb-. NOT an incoming webhook: a webhook cannot create channels"
	case Webhook:
		return "Slack incoming webhook URL (optional)\n  posts to one fixed channel; independent of the bot token above"
	}
	return key
}
