package collect

import (
	"strings"
	"testing"
)

// A notification must never claim something the run did not do. Got wrong
// twice, the same way: first the worktree line, then the checkout line.
func TestTheMergedMessageNeverClaimsWhatDidNotHappen(t *testing.T) {
	pr := PR{URL: "https://pr/1"}

	_, body := msgMerged("FCIA-6", pr, "/repo", true, "fetched and fast-forwarded develop")
	if !strings.Contains(body, "was fast-forwarded") || !strings.Contains(body, "worktree removed") {
		t.Errorf("the happy path should report both: %s", body)
	}

	// The exact case seen in a real run: the tree had uncommitted changes,
	// so Refresh declined -- and the message still said it had succeeded.
	_, body = msgMerged("FCIA-6", pr, "/repo", false,
		"fetched; not fast-forwarding because the working tree has uncommitted changes.")
	if strings.Contains(body, "was fast-forwarded") {
		t.Error("claimed a fast-forward that was refused")
	}
	if !strings.Contains(body, "BEHIND") || !strings.Contains(body, "uncommitted") {
		t.Errorf("must say the checkout is behind AND why: %s", body)
	}
	if strings.Contains(body, "worktree removed") {
		t.Error("claimed a prune that did not happen")
	}
}
