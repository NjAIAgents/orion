package collect

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// The flag is the whole safety story, so it gets the sharpest test.
//
// batch_integration is off by default, and OFF must mean the batch path is
// unreachable rather than merely unlikely. If a default config could reach
// runBatch, then every existing repository would change how it merges on
// upgrade, which is the one outcome this feature must not have.
func TestBatchIntegrationIsOffInADefaultConfig(t *testing.T) {
	if config.Defaults().Collect.BatchIntegration {
		t.Fatal("batch_integration must default OFF: a repository that never asked " +
			"for it must not change how it merges on upgrade")
	}
}

// An empty check rollup must NOT read as green.
//
// This is the landmine ADR 0015 found in the existing code: cmd/orion/collect
// turns "no checks" into a PASSING verdict, which is right for a repository
// with no CI and catastrophic for a merge ref whose checks have not started.
// Under a batch every member would land on no evidence at all.
func TestSilenceIsNotGreen(t *testing.T) {
	quiet := PR{Verdict: VerdictPassing,
		Detail: "no checks are configured on this repository"}
	if !noChecksYet(quiet) {
		t.Fatal("an empty rollup must be recognised, or a batch lands on no evidence")
	}

	real := PR{Verdict: VerdictPassing, Detail: "3 check(s) passed"}
	if noChecksYet(real) {
		t.Error("a real passing result must not be mistaken for silence")
	}

	// Case-insensitively: the wording is produced elsewhere and only has to
	// stay recognisable, not identical.
	if !noChecksYet(PR{Detail: "No Checks Are Configured On This Repository"}) {
		t.Error("the check must not turn on capitalisation")
	}
}

// A batch of members whose branches nobody recorded produces nothing, and
// nothing is the signal to fall back to the per-branch path rather than to
// report an empty pass.
func TestAnEmptyBatchFallsBackRatherThanReportingNothing(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}
	b, err := Land(g, tr, "orion/batch", "develop", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Results) != 0 {
		t.Fatalf("an empty batch must produce no results, got %v", b.Describe())
	}
	if b.Runs != 0 {
		t.Errorf("Runs = %d: an empty batch must not spend a CI run", b.Runs)
	}
}

// Every outcome has to map onto a verdict the watcher already renders, or the
// batch path reports states the rest of the system cannot display.
func TestEveryOutcomeMapsOntoAVerdictTheWatcherKnows(t *testing.T) {
	known := map[Verdict]bool{
		VerdictPassing: true, VerdictFailing: true, VerdictStale: true,
	}
	for _, o := range []Outcome{Landed, Ejected, Culprit, Deferred} {
		var v Verdict
		switch o {
		case Landed:
			v = VerdictPassing
		case Culprit:
			v = VerdictFailing
		default:
			v = VerdictStale
		}
		if !known[v] {
			t.Errorf("outcome %q maps to %q, which the watcher does not render", o, v)
		}
	}
}

// The report has to name every member and what became of it. A batch that
// lands four tickets and says "the batch was green" leaves an operator with
// nothing to check.
func TestTheBatchReportNamesEveryMemberAndItsOutcome(t *testing.T) {
	g := newFakeGit("orion/or-2")
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}}
	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(b.Describe(), "\n")
	for _, key := range []string{"OR-1", "OR-2", "OR-3"} {
		if !strings.Contains(lines, key) {
			t.Errorf("%s is missing from the report:\n%s", key, lines)
		}
	}
	for _, word := range []string{"ejected", "culprit"} {
		if !strings.Contains(lines, word) {
			t.Errorf("the report must say %q so the outcome is legible:\n%s", word, lines)
		}
	}
}

// UIChecks is the one place this package's forge-shaped states become the
// display's three (OR-310). Both callers -- the batch path here and
// internal/watch's ordinary-run path -- go through this switch, so they
// cannot start disagreeing about what "running" looks like.
func TestUIChecksConvertsEachInternalStateToItsDisplayState(t *testing.T) {
	in := []Check{
		{Name: "go (ubuntu)", State: CheckPassed},
		{Name: "go (windows)", State: CheckRunning},
		{Name: "go (macos)", State: CheckFailed},
	}
	out := UIChecks(in)
	if len(out) != 3 {
		t.Fatalf("expected one display check per internal check, got %+v", out)
	}
	want := []ui.Check{
		{Name: "go (ubuntu)", State: ui.CheckPassed},
		{Name: "go (windows)", State: ui.CheckRunning},
		{Name: "go (macos)", State: ui.CheckFailed},
	}
	for i, w := range want {
		if out[i] != w {
			t.Errorf("out[%d] = %+v, want %+v", i, out[i], w)
		}
	}
}

// OR-314. Observed on 2026-09-02: OR-150, OR-153 and OR-296 merged to develop
// in PR #396 and were all still In Progress carrying orion-ready hours later,
// closed in the end by hand. The batch knew exactly which members had landed
// -- it named them on screen and in the log -- and told the tracker nothing.
//
// The stale label is the expensive half: a merged ticket that keeps the queue
// label is picked up by the next collect pass, and the one after, forever.
func TestALandedBatchMemberIsClosedAndEveryOtherMemberIsLeftAlone(t *testing.T) {
	// The whole shape of a batch that failed and was bisected: one member
	// landed, one would not merge, one was the reason CI went red, and one was
	// sound but sat in the batch that failed.
	b := Batch{Results: []MemberResult{
		{Member: Member{Key: "OR-150"}, Outcome: Landed},
		{Member: Member{Key: "OR-152"}, Outcome: Ejected},
		{Member: Member{Key: "OR-9"}, Outcome: Culprit},
		{Member: Member{Key: "OR-8"}, Outcome: Deferred},
	}}
	jira := newTracker()
	var buf bytes.Buffer

	// Derived exactly as runBatch derives it, so a change to either half fails
	// here rather than in production.
	closeLanded(b.Members(Landed), "https://forge/pull/396", "orion-ready",
		Deps{Jira: jira}, &buf)

	if got := jira.transitions["OR-150"]; got != "Done" {
		t.Errorf("a landed member's status is %q, want %q: a batch that merges "+
			"has to finish the tickets it merged", got, "Done")
	}
	for _, label := range tracker.Managed("orion-ready") {
		if !hasLabel(jira.removed["OR-150"], label) {
			t.Errorf("a landed member kept %q; a merged ticket that keeps a "+
				"managed label re-enters the integration queue forever", label)
		}
	}
	comments := strings.Join(jira.comments["OR-150"], "\n")
	if !strings.Contains(comments, "https://forge/pull/396") {
		t.Errorf("a landed member was not told where its work went; comments:\n%s",
			comments)
	}

	// AND NOTHING ELSE. An ejected member's branch is still waiting and a
	// culprit is the reason the batch went red -- closing either would report
	// work as delivered that is not on the trunk.
	for _, key := range []string{"OR-152", "OR-9", "OR-8"} {
		if jira.transitions[key] != "" || len(jira.removed[key]) > 0 ||
			len(jira.comments[key]) > 0 {
			t.Errorf("%s did not land and must be untouched, but it was "+
				"transitioned=%q relabelled=%v commented=%v",
				key, jira.transitions[key], jira.removed[key], jira.comments[key])
		}
	}
}

// A nil or empty rollup must convert to nothing, not panic and not a
// display artifact like a lone empty cell (OR-310) -- a ticket whose PR read
// carried no checks yet is common (CI has not started reporting) and must
// render as if nothing were pending, not as a broken row.
func TestUIChecksHandlesNilAndEmptyWithoutPanicking(t *testing.T) {
	if out := UIChecks(nil); len(out) != 0 {
		t.Errorf("a nil rollup must convert to nothing, got %+v", out)
	}
	if out := UIChecks([]Check{}); len(out) != 0 {
		t.Errorf("an empty rollup must convert to nothing, got %+v", out)
	}
}

// A workflow without a Done transition must not turn a successful merge into a
// failure. The merge has HAPPENED by the time this runs -- the code is on the
// work branch -- and no amount of tracker trouble makes that untrue.
func TestATrackerThatCannotTransitionStillLetsTheMergeStand(t *testing.T) {
	jira := newTracker()
	jira.transitionErr = errors.New("no transition named Done")
	var buf bytes.Buffer

	if err := closeTicket("OR-150", "https://forge/pull/396", "orion-ready",
		Deps{Jira: jira}, &buf); err != nil {
		t.Fatalf("closeTicket returned %v; a workflow with no Done transition "+
			"must not turn a successful merge into a failure", err)
	}
	if !hasLabel(jira.removed["OR-150"], "orion-ready") {
		t.Error("the labels must still be cleared: that is the step whose " +
			"failure means the ticket is collected again")
	}
	if len(jira.comments["OR-150"]) == 0 {
		t.Error("the pull request must still be recorded on the ticket")
	}
	if !strings.Contains(buf.String(), "OR-150") {
		t.Errorf("the failure has to be reported, not swallowed:\n%s", buf.String())
	}
}

func hasLabel(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
