package collect

import (
	"strings"
	"testing"
)

// Two tickets in flight is the normal state under `orion watch`, and it is
// the state that produces this: FCIA-9's branch is cut from origin/develop
// before FCIA-8 lands, so once FCIA-8 merges the base has moved and the two
// changes may overlap.
//
// Nothing is ever overwritten -- separate worktrees, separate branches -- and
// git refuses the merge rather than guessing. The bug was entirely in the
// response. Orion never asked gh for `mergeable`, so a conflict was
// indistinguishable from any other merge failure, and the recovery path
// ("leave the request in place, a later pass retries") re-attempted an
// impossible merge every tick, forever, while never once saying that a human
// had to rebase.

func TestAConflictedBranchIsReportedNotRetried(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, out, c := run(t, home, jira, PR{
		// The realistic shape: checks PASSED, against a base that has since
		// moved. A conflict is not a CI failure and does not look like one.
		Verdict: VerdictPassing, Conflicted: true, Head: "abc123",
		URL: "https://example/pr/2", Detail: "3 passed",
	}, Options{})

	if res[0].Verdict != VerdictConflicted {
		t.Fatalf("verdict = %q, want conflicted", res[0].Verdict)
	}
	if c.pruned != 0 || c.refreshed != 0 {
		t.Error("a conflicted branch must not be pruned or fast-forwarded")
	}
	if !strings.Contains(out, "conflicts with its base") {
		t.Errorf("the reason must be named:\n%s", out)
	}
	if !strings.Contains(out, "rebase") {
		t.Errorf("the only action that resolves this must be named:\n%s", out)
	}
	// The ticket stays in ci-wait on purpose: the moment somebody pushes a
	// rebase, the conflict clears and the normal flow resumes with no
	// re-labelling by hand.
	for _, r := range jira.removed {
		if strings.Contains(strings.Join(r, ","), "ci-wait") {
			t.Error("released from ci-wait; a rebase would then be picked up by nothing")
		}
	}
}

// Said once, not every two minutes. A warning that repeats on a timer is one
// people mute, and muting the channel loses every later message too.
func TestTheSameConflictIsAnnouncedOnce(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	pr := PR{Verdict: VerdictPassing, Conflicted: true, Head: "abc123",
		URL: "https://example/pr/2"}

	run(t, home, jira, pr, Options{})
	first := len(jira.comments["FCIA-6"])
	run(t, home, jira, pr, Options{}) // same HEAD: nothing has changed

	// len(jira.comments) would count KEYS, not comments, and would pass
	// whatever happened. Count the comments on the ticket itself.
	if got := len(jira.comments["FCIA-6"]); got != first {
		t.Errorf("commented %d times for one unchanged conflict, want %d", got, first)
	}
	if first == 0 {
		t.Error("the first pass must say something")
	}
}

// A pushed rebase that STILL conflicts is a new situation and must be
// reported again -- somebody tried something and it did not work, and they
// need to know that rather than assuming silence means success.
func TestARebaseThatStillConflictsIsAnnouncedAgain(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()

	run(t, home, jira, PR{Verdict: VerdictPassing, Conflicted: true,
		Head: "abc123", URL: "https://example/pr/2"}, Options{})
	first := len(jira.comments["FCIA-6"])

	run(t, home, jira, PR{Verdict: VerdictPassing, Conflicted: true,
		Head: "def456", URL: "https://example/pr/2"}, Options{}) // new commit

	if len(jira.comments["FCIA-6"]) <= first {
		t.Error("a new HEAD is a new situation and must be reported")
	}
}

// UNKNOWN is not CONFLICTING. GitHub reports UNKNOWN for a few seconds after
// a push while it computes mergeability, and announcing a rebase then would
// send someone to fix a conflict that does not exist.
func TestOnlyAConfirmedConflictCounts(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, out, _ := run(t, home, jira, PR{
		Verdict: VerdictPassing, Conflicted: false, Head: "abc123",
		URL: "https://example/pr/2", Detail: "3 passed",
	}, Options{})

	if res[0].Verdict == VerdictConflicted {
		t.Error("mergeability was not CONFLICTING; nothing should have been claimed")
	}
	if strings.Contains(out, "conflicts with its base") {
		t.Errorf("a conflict was announced without one:\n%s", out)
	}
}
