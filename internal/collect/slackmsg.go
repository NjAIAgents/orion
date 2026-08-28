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

// msgMerged reports the good ending.
//
// Nothing here is assumed. Both the prune and the checkout refresh are told
// to it, because both can legitimately not happen and the message must say
// which.
//
// This was got wrong TWICE, the same way. First the message claimed "its
// worktree removed" while the terminal two lines above said it had been
// kept. Fixed -- and the very next real merge printed "your checkout was
// fast-forwarded" while the terminal said it had refused, because the tree
// had uncommitted changes.
//
// A notification that contradicts what it reports is worse than none: it is
// the version most people read, and it teaches them that Orion's messages
// are approximately true. Once a reader believes that, every message is
// worth less, including the ones that matter.
// work and prod name the integration branch this landed on and the branch a
// release goes to. Passed in rather than hardcoded: "develop" was written
// into this message as a literal, so a project using any other name was told
// its work was somewhere it is not.
func msgMerged(key string, pr PR, checkout string, pruned bool, refreshed, work, prod string) (string, string) {
	if work == "" {
		work = "develop"
	}
	title := fmt.Sprintf("%s merged", key)

	// The sync line answers one question: do I need to pull?
	//
	// "was fast-forwarded" is git's word for it and assumes the reader
	// translates it. Say the thing they actually care about -- the merged
	// work is already in their working copy and there is nothing to pull --
	// and keep the branch name so it is checkable rather than reassuring.
	checkoutLine := "• your local copy `" + checkout + "` is up to date — " +
		"the merged changes have been pulled into `" + work + "`, nothing to do"
	if !strings.Contains(refreshed, "fast-forwarded") {
		// It did not move. Say what stopped it, since the reader's next
		// action -- pulling by hand -- depends on which reason it was.
		reason := firstLine(refreshed)
		if reason == "" {
			reason = "it was not updated"
		}
		checkoutLine = "• your local copy `" + checkout + "` is BEHIND: " + reason +
			"\n• pull it yourself: `git -C " + checkout + " pull --ff-only`"
	}

	// What is left, said plainly.
	//
	// This used to end "Nothing is waiting on you", which is false: landing
	// on the integration branch is not landing in production. Somebody still
	// has to take work -> prod, and a message that says the opposite invites
	// the reader to assume a release happened.
	closing := "Over to you for the " + prod + " merge."
	if prod == "" || prod == work {
		closing = "Nothing is waiting on you."
	}
	tail := "_The ticket is closed and its worktree removed. " + closing + "_"
	if !pruned {
		tail = "_The ticket is closed. The worktree was kept -- see `orion sandbox " +
			key + "`. " + closing + "_"
	}
	body := strings.Join([]string{
		"*The work is on the " + branchRole(work, prod) + " `" + work + "`.*",
		"",
		"• pull request  " + link(pr.URL, "what merged"),
		checkoutLine,
		"",
		tail,
	}, "\n")
	return title, body
}

// branchRole names a branch by what it is FOR, not only by what it is
// called. "merged into develop" is only correct if the reader knows what
// develop is for in this repository; "merged into the integration branch
// develop" reads correctly whatever it is named -- and a repository where
// the two collapse says "release branch", which is the thing a reader
// needs to notice rather than the thing they skim past.
func branchRole(branch, release string) string {
	if release != "" && branch == release {
		return "release branch"
	}
	return "integration branch"
}

// msgApprovalWanted is the one message in this system that asks for
// authority rather than reporting a fact.
//
// It must make the reader capable of refusing. That means naming what will
// happen the moment they tap, saying who is allowed to answer -- so a person
// who is not on the list stops waiting for their own tap to work -- and
// linking the diff rather than merely the ticket. An approval request that
// is easier to accept than to check is a rubber stamp with extra steps.
func msgApprovalWanted(key string, pr PR, branch string, approvers []string) (string, string) {
	title := fmt.Sprintf("%s is ready to merge — approve?", key)

	who := "nobody is configured, so no approval can succeed"
	if len(approvers) > 0 {
		who = strings.Join(approvers, ", ")
	}

	body := strings.Join([]string{
		"*Checks pass on " + "`" + branch + "`" + ".*",
		"",
		"• review the diff  " + link(pr.URL, "pull request"),
		"• checks           " + link(pr.URL+"/checks", pr.Detail),
		"",
		"React ✅ to merge, or ❌ to decline. Replying `approve` or `no` works too.",
		"",
		"_Only " + who + " can approve._",
		"_Approving merges it and closes the ticket. Declining keeps the branch and changes nothing._",
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
