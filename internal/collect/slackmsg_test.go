package collect

import (
	"strings"
	"testing"
)

// A notification must never claim something the run did not do. Got wrong
// twice, the same way: first the worktree line, then the checkout line.
func TestTheMergedMessageNeverClaimsWhatDidNotHappen(t *testing.T) {
	pr := PR{URL: "https://pr/1"}

	_, body := msgMerged("FCIA-6", pr, "/repo", true, "fetched and fast-forwarded develop", "develop", "main")
	if !strings.Contains(body, "is up to date") || !strings.Contains(body, "worktree removed") {
		t.Errorf("the happy path should report both: %s", body)
	}
	// The reader's real question is "do I need to pull?". Answer it.
	if !strings.Contains(body, "pulled") || !strings.Contains(body, "nothing to do") {
		t.Errorf("a successful sync must say the work is already local: %s", body)
	}

	// The exact case seen in a real run: the tree had uncommitted changes,
	// so Refresh declined -- and the message still said it had succeeded.
	_, body = msgMerged("FCIA-6", pr, "/repo", false,
		"fetched; not fast-forwarding because the working tree has uncommitted changes.",
		"develop", "main")
	if strings.Contains(body, "is up to date") || strings.Contains(body, "nothing to do") {
		t.Error("claimed a sync that was refused")
	}
	// And when it did NOT sync, hand over the command that fixes it.
	if !strings.Contains(body, "pull --ff-only") {
		t.Errorf("a behind checkout must come with the way to fix it: %s", body)
	}
	if !strings.Contains(body, "BEHIND") || !strings.Contains(body, "uncommitted") {
		t.Errorf("must say the checkout is behind AND why: %s", body)
	}
	if strings.Contains(body, "worktree removed") {
		t.Error("claimed a prune that did not happen")
	}
}

// Landing on the integration branch is not landing in production, and the
// message used to end "Nothing is waiting on you" -- which invites the
// reader to assume a release happened. Something still does wait on them.
func TestTheMergedMessageNamesWhatIsStillOwed(t *testing.T) {
	_, body := msgMerged("FCIA-7", PR{URL: "https://pr/2"}, "/repo", true,
		"fetched and fast-forwarded develop", "develop", "main")

	if strings.Contains(body, "Nothing is waiting on you") {
		t.Errorf("the develop -> main merge is still owed:\n%s", body)
	}
	if !strings.Contains(body, "Over to you for the main merge") {
		t.Errorf("the remaining step must be named:\n%s", body)
	}
	if !strings.Contains(body, "`develop`") {
		t.Errorf("the branch it actually landed on must be named:\n%s", body)
	}
}

// The branch names come from config. "develop" was a literal in this
// message, so a project whose integration branch is called anything else was
// told its work was somewhere it is not.
func TestTheMergedMessageUsesTheProjectsOwnBranchNames(t *testing.T) {
	_, body := msgMerged("X-1", PR{URL: "https://pr/3"}, "/repo", true,
		"fetched and fast-forwarded integration", "integration", "release")

	if strings.Contains(body, "develop") {
		t.Errorf("hardcoded develop leaked into a project that has none:\n%s", body)
	}
	if !strings.Contains(body, "`integration`") || !strings.Contains(body, "release merge") {
		t.Errorf("both configured names should appear:\n%s", body)
	}
}

// A trunk-only project has nowhere further to go, so promising a second
// merge would invent a step that does not exist.
func TestATrunkOnlyProjectIsNotSentToMergeAgain(t *testing.T) {
	_, body := msgMerged("X-1", PR{URL: "https://pr/4"}, "/repo", true,
		"fetched and fast-forwarded main", "main", "main")

	if strings.Contains(body, "Over to you") {
		t.Errorf("there is no second merge in a trunk-only project:\n%s", body)
	}
	if !strings.Contains(body, "Nothing is waiting on you") {
		t.Errorf("say it is genuinely done:\n%s", body)
	}
}

// The approval request is the only message that waits on a person, and so
// the only one that earns a mention. Tagging on a merged notice or a CI
// report trains a room to mute the channel -- and a muted channel delivers
// nothing at all, so a mention attached to news nobody has to act on costs
// the approval request the only advantage it has.
//
// (A CI failure can still be tagged, from the separate slack.mention list
// applied at the call site in collect.go. That list is opt-in, names people
// explicitly, and is not built from merge_approvers.)
func TestOnlyTheApprovalRequestMentionsAnybody(t *testing.T) {
	pr := PR{URL: "https://pr/9", Detail: "3 checks failed"}

	_, approval := msgApprovalWanted("X-1", pr, "orion/x-1", []string{"<@U012ABCDEF>"})
	if !strings.Contains(approval, "<@U012ABCDEF>") {
		t.Fatalf("the one message that waits on a person does not tag them:\n%s", approval)
	}
	if strings.Contains(approval, "<!") {
		t.Errorf("the approval request broadcasts:\n%s", approval)
	}

	_, merged := msgMerged("X-1", pr, "/repo", true,
		"fetched and fast-forwarded develop", "develop", "main")
	_, failed := msgCIFailed("X-1", pr)

	for name, body := range map[string]string{"merged": merged, "ci-failed": failed} {
		if strings.Contains(body, "<@") {
			t.Errorf("the %s notice mentions somebody:\n%s", name, body)
		}
		// @channel and @here reach a whole project room, most of whom have
		// no standing to answer anything.
		if strings.Contains(body, "<!") {
			t.Errorf("the %s notice broadcasts:\n%s", name, body)
		}
	}
}
