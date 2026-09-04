package supervisor

import (
	"strings"
	"testing"
)

// The ticket's own acceptance criterion: "Single-question explore does not
// print cost shape, roster, or landing announcements (no noise for the
// backwards-compatible case)." A single explore call still goes through
// Fan with exactly one job (cmd/orion/explore.go's exploreAll always calls
// supervisor.Fan), so Fan itself must stay quiet for a fan of one.
func TestFanStaysQuietForASingleJob(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	w := ws(t, "")
	Fan(w, []Options{
		{Stage: "explore", Prompt: "x", MaxMinutes: 1, MaxTurns: 1, Actor: "explore"},
	})

	if got := out.String(); got != "" {
		t.Errorf("a fan-out of one child printed announcements, but a single question must be "+
			"silent (no cost shape, roster, or landing line): %q", got)
	}
}

// The batch case still has to say when it landed. This is not new coverage
// of the multi-job path (fan_announce_test.go already covers it); it's here
// so the single-job assertion above can't be satisfied by simply deleting
// the landing print altogether.
func TestFanStillAnnouncesForTwoJobs(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	out := captureFanOut(t)

	w := ws(t, "")
	Fan(w, []Options{
		{Stage: "explore", Prompt: "x", MaxMinutes: 1, MaxTurns: 1, Actor: "explore"},
		{Stage: "explore", Prompt: "y", MaxMinutes: 1, MaxTurns: 1, Actor: "explore"},
	})

	if got := out.String(); !strings.Contains(got, "fanning out") {
		t.Errorf("a fan-out of two children printed nothing, want the cost-shape line: %q", got)
	}
}
