package work

import (
	"fmt"
	"strings"

	"github.com/orion-sdlc/orion/internal/advise"
)

// Slack messages.
//
// These are read on a phone, by someone who was not watching. That sets the
// bar: every message must be actionable without opening a terminal, and it
// must say what happens NEXT, not merely what happened. "FCIA-6 failed" tells
// you something is wrong and leaves you to find out what; "the breaker
// tripped at 400 tool calls, the ticket is back in the queue, the log is
// here" lets you decide whether to care right now.
//
// Slack's mrkdwn, not Markdown: *bold* has one asterisk, links are
// <url|label>, and there are no headings. Writing GitHub-flavoured Markdown
// here produces literal asterisks in the message.

// msgStarted announces a claim. Deliberately short -- it is the message you
// see most often and the one you least need to act on.
func msgStarted(key, summary, branch, issueURL string) (string, string) {
	title := fmt.Sprintf("%s started", key)
	body := strings.Join([]string{
		"*" + summary + "*",
		"",
		"• ticket   " + link(issueURL, key),
		"• branch   `" + branch + "`",
	}, "\n")
	return title, body
}

// msgCIWait is the good outcome: something exists to review.
func msgCIWait(key, summary, branch, prURL, issueURL string, commits int) (string, string) {
	title := fmt.Sprintf("%s is ready for review", key)
	body := strings.Join([]string{
		"*" + summary + "*",
		"",
		"• pull request  " + link(prURL, "open it"),
		"• branch        `" + branch + "`",
		"• commits       " + fmt.Sprint(commits),
		"• ticket        " + link(issueURL, key),
		"",
		"_CI is running. The ticket moves on when it finishes; nothing is waiting on you yet._",
	}, "\n")
	return title, body
}

// msgBlocked is the message that has to work hardest.
//
// A blocked run is not a failure, and saying so matters: the agent did the
// right thing by stopping instead of guessing. What the reader needs is the
// question, who already tried to answer it, and the fact that answering it
// once should end with the design document being amended -- otherwise the
// next ticket asks the same thing.
func msgBlocked(key, summary, question, issueURL string, a advise.Answer) (string, string) {
	title := fmt.Sprintf("%s needs a decision from you", key)

	lines := []string{
		"*" + summary + "*",
		"",
		"The implementation stopped rather than guess. That is the correct outcome:",
		"a guess here becomes a confident, wrong implementation that passes its own tests.",
		"",
		"*The question*",
		quote(question),
	}

	switch {
	case a.Verdict == advise.VerdictRefused && a.Reason != "":
		lines = append(lines,
			"",
			fmt.Sprintf("*The %s looked and could not decide it*", a.Role),
			quote(a.Reason),
			"",
			"So the design itself is incomplete. When you answer, amend the artifact it",
			"belongs in — otherwise the next ticket asks the same question and pays again.")
	case a.Verdict == "":
		lines = append(lines,
			"",
			"_No advisor was consulted, so nothing has tried to answer this yet._")
	}

	lines = append(lines,
		"",
		"• ticket  "+link(issueURL, key),
		"• requeue by removing `orion-failed` and adding `ORION`",
	)
	return title, strings.Join(lines, "\n")
}

// msgNoop reports a run that correctly did nothing.
//
// Worded to be unmistakable at a glance, because the whole point of the
// outcome is that it not be read as a failure: someone scrolling a channel
// full of "FCIA-6 failed" needs to see in the title alone that this one is
// fine. What they need to act on is the single line explaining why nothing
// was needed -- if that line is wrong, the work really is missing.
func msgNoop(key, summary, note, issueURL string) (string, string) {
	title := fmt.Sprintf("%s needed no change", key)
	body := strings.Join([]string{
		"*" + summary + "*",
		"",
		"Nothing was done, and nothing failed:",
		quote(note),
		"",
		"• ticket  " + link(issueURL, key),
		"",
		"_The ticket is closed and unlabelled. Nothing is waiting on you. If the work_",
		"_is in fact missing, reopen it and add `ORION` to run it again._",
	}, "\n")
	return title, body
}

// msgFailed is for something broken, as distinct from something undecided.
func msgFailed(key, summary, reason, branch, issueURL, logPath string) (string, string) {
	title := fmt.Sprintf("%s failed", key)
	lines := []string{
		"*" + summary + "*",
		"",
		"*What went wrong*",
		quote(reason),
		"",
		"• ticket  " + link(issueURL, key),
	}
	if branch != "" {
		lines = append(lines, "• branch  `"+branch+"` (kept, so nothing the agent produced is lost)")
	}
	if logPath != "" {
		lines = append(lines, "• log     `"+logPath+"`")
	}
	lines = append(lines,
		"",
		"_The ticket is out of the queue. Nothing will retry it until you requeue it._")
	return title, strings.Join(lines, "\n")
}

// msgAnswered reports an advisor resolving a question without a human.
//
// Worth sending even though nothing is required of the reader: this is the
// loop working, and seeing the grounding is how you notice an advisor citing
// a clause that does not say what it claims.
func msgAnswered(key string, a advise.Answer, question string) (string, string) {
	title := fmt.Sprintf("%s: the %s answered a question", key, a.Role)
	body := strings.Join([]string{
		"*Question*",
		quote(question),
		"",
		"*Decision*",
		quote(a.Decision),
		"",
		"• grounding  " + a.Grounding,
		"• decided by " + string(a.Role) + " (" + a.Model + ")",
		"",
		"_Recorded on the branch and committed. Implementation continues._",
	}, "\n")
	return title, body
}

// link renders Slack's link syntax, degrading to bare text when there is no
// URL so a message never shows an empty <|label>.
func link(url, label string) string {
	if strings.TrimSpace(url) == "" {
		return label
	}
	return "<" + url + "|" + label + ">"
}

// quote renders a block quote, trimming and bounding it. An agent's closing
// message can be pages long, and a Slack notification that has to be scrolled
// is one nobody reads to the end of.
func quote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "> _(nothing said)_"
	}
	const max = 900
	if len(s) > max {
		s = s[:max] + "\n… (truncated; the full text is on the ticket)"
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		b.WriteString("> " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
