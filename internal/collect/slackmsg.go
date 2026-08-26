package collect

import (
	"fmt"
	"strings"
)

// Slack messages for the two endings that are worth interrupting someone for.
//
// Nothing is sent while CI is pending or merely passing. A message per poll
// would train the reader to ignore the channel, and "still running" is not
// news. What is news: it landed, or it broke.

func msgMerged(key string, pr PR, checkout string) (string, string) {
	title := fmt.Sprintf("%s merged", key)
	body := strings.Join([]string{
		"*The work is on " + "`develop`" + ".*",
		"",
		"• pull request  " + link(pr.URL, "what merged"),
		"• your checkout " + "`" + checkout + "` was fast-forwarded",
		"",
		"_The ticket is closed and its worktree removed. Nothing is waiting on you._",
	}, "\n")
	return title, body
}

func msgCIFailed(key string, pr PR) (string, string) {
	title := fmt.Sprintf("%s failed CI", key)
	body := strings.Join([]string{
		"*The agent's work does not pass on the branch.*",
		"",
		"*What failed*",
		quote(pr.Detail),
		"",
		"• pull request  " + link(pr.URL, "open it"),
		"",
		"_The branch is kept, so nothing the agent wrote is lost._",
		"_Fix it there and push, or close the pull request and re-queue the ticket._",
		"",
		"The ticket is out of the queue and labelled `orion-failed`. It is not",
		"re-queued automatically: the branch already has commits, so a fresh run",
		"would cut a second branch for the same ticket and compete with this one.",
	}, "\n")
	return title, body
}

func link(url, label string) string {
	if strings.TrimSpace(url) == "" {
		return label
	}
	return "<" + url + "|" + label + ">"
}

func quote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "> _(no detail reported)_"
	}
	const max = 900
	if len(s) > max {
		s = s[:max] + "\n… (truncated; the full output is on the pull request)"
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		b.WriteString("> " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
