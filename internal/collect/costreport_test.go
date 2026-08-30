package collect

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/cost"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// errTracker stands in for any tracker failure at the moment of posting.
var errTracker = errors.New("the tracker is unreachable")

// spend writes a ticket's lifecycle into the workspace event log the way the
// supervisor does: the implementation run, a fix-loop re-entry, and a run
// that died. All three are the ticket's cost.
func spend(t *testing.T, home, key string) {
	t.Helper()
	entry, err := registry.Lookup(home, key)
	if err != nil {
		t.Fatalf("looking up %s: %v", key, err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatalf("opening the workspace: %v", err)
	}
	log, err := events.Open(events.Path(ws.Dir), events.Event{})
	if err != nil {
		t.Fatalf("opening the event log: %v", err)
	}
	defer log.Close()

	cost.Record(log, "", events.ActorImplementer, key, cost.FromBudgetRun(budget.Run{
		Turns: 34, PromptTokens: 12_410, OutputTokens: 8_922,
		CacheCreateTokens: 41_203, CacheReadTokens: 1_203_554, CostUSD: 3.84,
	}, true, false, "completed", 12*time.Minute))
	cost.Record(log, "", events.ActorDevOps, key, cost.FromBudgetRun(budget.Run{
		Turns: 6, PromptTokens: 2_101, OutputTokens: 944,
		CacheReadTokens: 210_388, CostUSD: 0.41,
	}, true, false, "completed", 2*time.Minute))
	cost.Record(log, "", events.ActorDevOps, key, cost.FromBudgetRun(budget.Run{
		Turns: 40, PromptTokens: 300, OutputTokens: 100, CostUSD: 0.05,
	}, true, true, "timed out", 30*time.Minute))
}

func costComments(jira *fakeTracker, key string) []string {
	var out []string
	for _, c := range jira.comments[key] {
		if strings.Contains(c, "cost report") {
			out = append(out, c)
		}
	}
	return out
}

// The report goes to the ticket AND to the terminal, from one render, at the
// moment the merge lands. Only one of the two surfaces existing is the state
// this feature exists to end: a price nobody sees until they go looking.
func TestAMergedTicketReportsItsCostToBothSinks(t *testing.T) {
	home, _ := bound(t)
	spend(t, home, "FCIA-6")
	jira := newTracker()
	_, out, _ := run(t, home, jira, PR{Verdict: VerdictMerged, URL: "u"}, Options{})

	posted := costComments(jira, "FCIA-6")
	if len(posted) != 1 {
		t.Fatalf("posted %d cost comments, want 1: %v", len(posted), jira.comments["FCIA-6"])
	}
	if !strings.Contains(out, "cost report FCIA-6") {
		t.Errorf("the console said nothing about what the ticket cost:\n%s", out)
	}

	// The same numbers on both, because there is one renderer. Compared on
	// the table body rather than the whole string: the comment carries an
	// attribution prefix the terminal does not need.
	for _, want := range []string{
		"$4.30",     // 3.84 + 0.41 + 0.05, every run counted
		"1,413,942", // the cache reads, apart from input
		"41,203",    // and cache creation, apart from cache reads
		"failed",    // the run that died is in the total and marked
	} {
		if !strings.Contains(posted[0], want) {
			t.Errorf("the comment does not mention %q:\n%s", want, posted[0])
		}
		if !strings.Contains(out, want) {
			t.Errorf("the console output does not mention %q:\n%s", want, out)
		}
	}
}

// Collect is a poll, so it is run twice by definition -- a watcher tick, or a
// person re-running it after something downstream failed. A second cost
// comment on a closed ticket is how a useful comment becomes noise.
func TestTheCostReportIsPostedOnce(t *testing.T) {
	home, _ := bound(t)
	spend(t, home, "FCIA-6")
	jira := newTracker()
	pr := PR{Verdict: VerdictMerged, URL: "u"}

	run(t, home, jira, pr, Options{})
	run(t, home, jira, pr, Options{})

	if n := len(costComments(jira, "FCIA-6")); n != 1 {
		t.Errorf("re-running collect posted the cost report %d times, want 1", n)
	}
}

// A run that could not be posted must not be marked as posted. Otherwise the
// one thing this promises -- the number is on the ticket -- silently never
// happens, and nothing retries it.
func TestAFailedPostIsRetriedOnTheNextPass(t *testing.T) {
	home, _ := bound(t)
	spend(t, home, "FCIA-6")
	jira := newTracker()
	jira.commentErr = errTracker
	pr := PR{Verdict: VerdictMerged, URL: "u"}
	run(t, home, jira, pr, Options{})

	jira.commentErr = nil
	run(t, home, jira, pr, Options{})

	if n := len(costComments(jira, "FCIA-6")); n != 1 {
		t.Errorf("after a failed post the report was posted %d times, want 1", n)
	}
}

// A ticket worked before usage was recorded has nothing to aggregate. It must
// say that rather than post a table of zeroes, which reads as "this was free".
func TestATicketWithNoRecordedUsageSaysSo(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	_, out, _ := run(t, home, jira, PR{Verdict: VerdictMerged, URL: "u"}, Options{})

	posted := costComments(jira, "FCIA-6")
	if len(posted) != 1 {
		t.Fatalf("posted %d cost comments, want 1", len(posted))
	}
	if !strings.Contains(posted[0], "No per-run usage was recorded") {
		t.Errorf("an unknown cost was presented as a known one:\n%s", posted[0])
	}
	if !strings.Contains(out, "No per-run usage was recorded") {
		t.Errorf("the console did not say the cost is unknown:\n%s", out)
	}
}
