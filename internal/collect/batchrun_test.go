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

// OR-314 case 6. A Culprit is the reason the batch went red -- closing it
// would report work as delivered that never reached the trunk.
func TestACulpritIsNotTransitionedRelabelledCommentedOrHasChildrenClosed(t *testing.T) {
	jira := newTracker()
	jira.children["OR-9"] = []tracker.Issue{{Key: "OR-9-1", StatusCategory: "indeterminate"}}
	var buf bytes.Buffer

	closeLanded(nil, "https://forge/pull/396", "orion-ready", Deps{Jira: jira}, &buf)
	// closeLanded only ever touches the keys it is handed; a Culprit is
	// simulated by simply never appearing in that list, exactly as runBatch
	// derives it via b.Members(Landed).

	if jira.transitions["OR-9"] != "" || len(jira.removed["OR-9"]) > 0 ||
		len(jira.comments["OR-9"]) > 0 || jira.transitions["OR-9-1"] != "" {
		t.Errorf("a culprit must be left untouched, but got transitions=%q "+
			"labels=%v comments=%v child=%q", jira.transitions["OR-9"],
			jira.removed["OR-9"], jira.comments["OR-9"], jira.transitions["OR-9-1"])
	}
}

// OR-314 case 7. A Deferred member's branch is still waiting for the next
// batch -- it did not land, so nothing about it is finished yet.
func TestADeferredMemberIsNotTransitionedRelabelledCommentedOrHasChildrenClosed(t *testing.T) {
	jira := newTracker()
	jira.children["OR-8"] = []tracker.Issue{{Key: "OR-8-1", StatusCategory: "indeterminate"}}
	var buf bytes.Buffer

	closeLanded(nil, "https://forge/pull/396", "orion-ready", Deps{Jira: jira}, &buf)

	if jira.transitions["OR-8"] != "" || len(jira.removed["OR-8"]) > 0 ||
		len(jira.comments["OR-8"]) > 0 || jira.transitions["OR-8-1"] != "" {
		t.Errorf("a deferred member must be left untouched, but got transitions=%q "+
			"labels=%v comments=%v child=%q", jira.transitions["OR-8"],
			jira.removed["OR-8"], jira.comments["OR-8"], jira.transitions["OR-8-1"])
	}
}

// OR-314 case 8. The label clear is the record everything else is
// reconciled against; if it fails, closeTicket must stop right there rather
// than transition or comment on a ticket the queue will pick up again.
func TestWhenLabelClearingFailsClosingStopsAndReturnsTheError(t *testing.T) {
	jira := newTracker()
	jira.labelErr = errors.New("tracker unreachable")
	var buf bytes.Buffer

	err := closeTicket("OR-150", "https://forge/pull/396", "orion-ready", Deps{Jira: jira}, &buf)
	if !errors.Is(err, jira.labelErr) {
		t.Fatalf("closeTicket returned %v, want the label error to propagate so the "+
			"ticket is retried", err)
	}
	if jira.transitions["OR-150"] != "" {
		t.Errorf("transitioned to %q despite the label clear failing; processing "+
			"must stop at the failed step", jira.transitions["OR-150"])
	}
	if len(jira.comments["OR-150"]) > 0 {
		t.Errorf("commented despite the label clear failing; processing must stop "+
			"at the failed step, got %v", jira.comments["OR-150"])
	}
}

// OR-314 case 9. A workflow without a Done transition must not turn a
// successful merge into a failure, and the steps after it -- the comment and
// closing the children -- must still run.
func TestWhenTransitioningToDoneFailsAWarningIsLoggedButTheMergeSucceeds(t *testing.T) {
	jira := newTracker()
	jira.children["OR-150"] = []tracker.Issue{{Key: "OR-150-1", StatusCategory: "indeterminate"}}
	// Only the parent's own transition fails here -- jira.transitionErr is one
	// knob for every key, which cannot tell "the parent failed but the child
	// still closed" apart from "neither did" (see childFailTracker below).
	wrapped := &childFailTracker{fakeTracker: jira, failTransition: map[string]bool{"OR-150": true}}
	var buf bytes.Buffer

	err := closeTicket("OR-150", "https://forge/pull/396", "orion-ready", Deps{Jira: wrapped}, &buf)
	if err != nil {
		t.Fatalf("closeTicket returned %v; a missing Done transition must not fail "+
			"the merge", err)
	}
	if !strings.Contains(buf.String(), "OR-150") || !strings.Contains(buf.String(), "Done") {
		t.Errorf("the transition failure must be logged as a warning, got:\n%s", buf.String())
	}
	if len(jira.comments["OR-150"]) == 0 {
		t.Error("the pull request comment must still be posted despite the " +
			"transition failing")
	}
	if jira.transitions["OR-150-1"] != "Done" {
		t.Errorf("the child must still be closed despite the parent's transition "+
			"failing, got %q", jira.transitions["OR-150-1"])
	}
}

// OR-314 case 10. Commenting is best effort: its failure must not fail the
// merge or mark the member as failed.
//
// NOTE: the ticket's wording asks for a warning to be logged on a comment
// failure. closeTicket calls Comment through `_ = deps.Jira.Comment(...)` --
// the error is discarded with no ui.Warn, unlike the transition failure a few
// lines above it. That half of case 10 does not hold as the code is written;
// this test covers only the part that does (the failure is swallowed rather
// than propagated, and every other step still runs) without editing the code
// under test to make the warning claim true.
func TestWhenCommentingFailsTheMergeSucceedsAndTheMemberIsNotMarkedFailed(t *testing.T) {
	jira := newTracker()
	jira.commentErr = errors.New("comment API rejected the request")
	jira.children["OR-150"] = []tracker.Issue{{Key: "OR-150-1", StatusCategory: "indeterminate"}}
	var buf bytes.Buffer

	err := closeTicket("OR-150", "https://forge/pull/396", "orion-ready", Deps{Jira: jira}, &buf)
	if err != nil {
		t.Fatalf("closeTicket returned %v; a comment failure must not mark the "+
			"member as failed", err)
	}
	if jira.transitions["OR-150"] != "Done" {
		t.Errorf("the transition must still happen despite the comment failing, "+
			"got %q", jira.transitions["OR-150"])
	}
	if !hasLabel(jira.removed["OR-150"], "orion-ready") {
		t.Error("the labels must still be cleared despite the comment failing")
	}
	if jira.transitions["OR-150-1"] != "Done" {
		t.Errorf("closing the children must still run despite the comment "+
			"failing, got %q", jira.transitions["OR-150-1"])
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

// OR-314. A landed member's sub-tasks did the same work its parent did -- one
// branch, one PR, one merge -- so leaving them open would report work as
// outstanding that already landed. closeLanded reaches every landed member's
// children through the same closeTicket sequence the per-branch path uses.
func TestALandedBatchMembersSubTasksAreClosedToo(t *testing.T) {
	jira := newTracker()
	jira.children = map[string][]tracker.Issue{
		"OR-150": {{Key: "OR-151", Status: "In Progress"}},
		// OR-152 was ejected, not landed. Its child must never be reached.
		"OR-152": {{Key: "OR-153", Status: "In Progress"}},
	}
	var buf bytes.Buffer

	closeLanded([]string{"OR-150"}, "https://forge/pull/396", "orion-ready",
		Deps{Jira: jira}, &buf)

	if got := jira.transitions["OR-151"]; got != "Done" {
		t.Errorf("a landed member's sub-task status is %q, want %q: the work it "+
			"held landed with its parent", got, "Done")
	}
	comments := strings.Join(jira.comments["OR-151"], "\n")
	if !strings.Contains(comments, "OR-150") || !strings.Contains(comments, "https://forge/pull/396") {
		t.Errorf("a closed sub-task must say which parent delivered it and where: %q", comments)
	}

	if jira.transitions["OR-153"] != "" || len(jira.comments["OR-153"]) > 0 {
		t.Errorf("OR-153 belongs to an ejected member and must be untouched, but was "+
			"transitioned=%q commented=%v", jira.transitions["OR-153"], jira.comments["OR-153"])
	}
}

// childFailTracker fails TransitionTo for exactly the keys it is told to,
// leaving every other call to run through the embedded fakeTracker unchanged.
// The shared fakeTracker's transitionErr is one knob for every key, which
// cannot tell "the parent closed but its child didn't" apart from "neither
// did" -- and that distinction is the whole of case 11.
type childFailTracker struct {
	*fakeTracker
	failTransition map[string]bool
}

func (f *childFailTracker) TransitionTo(key, status string) error {
	if f.failTransition[key] {
		return errors.New("child close rejected")
	}
	return f.fakeTracker.TransitionTo(key, status)
}

// OR-314 case 11. A sub-task that will not transition must not undo the
// close of the parent it belongs to -- the parent landed regardless of what
// its children do, so its own transition, labels and comment must all stand,
// with only a warning to say the child was left open.
func TestWhenClosingChildrenFailsAWarningIsLoggedButTheMergeSucceeds(t *testing.T) {
	jira := newTracker()
	jira.children["OR-150"] = []tracker.Issue{{Key: "OR-150-1", StatusCategory: "indeterminate"}}
	wrapped := &childFailTracker{fakeTracker: jira, failTransition: map[string]bool{"OR-150-1": true}}
	var buf bytes.Buffer

	closeLanded([]string{"OR-150"}, "https://forge/pull/396", "orion-ready",
		Deps{Jira: wrapped}, &buf)

	if got := jira.transitions["OR-150"]; got != "Done" {
		t.Errorf("the parent's own transition = %q, want Done: a child's failure "+
			"must not undo the close of the ticket that landed", got)
	}
	for _, label := range tracker.Managed("orion-ready") {
		if !hasLabel(jira.removed["OR-150"], label) {
			t.Errorf("the parent kept %q despite landing", label)
		}
	}
	if len(jira.comments["OR-150"]) == 0 {
		t.Error("the parent must still be commented despite its child failing to close")
	}
	if got := jira.transitions["OR-150-1"]; got != "" {
		t.Errorf("the child transitioned to %q despite TransitionTo rejecting it", got)
	}
	if !strings.Contains(buf.String(), "OR-150-1") {
		t.Errorf("the child's failure must be logged as a warning, got:\n%s", buf.String())
	}
}

// OR-314 case 12. A repository with no tracker configured (deps.Jira == nil)
// must not be asked to update one -- there is nothing to reconcile a nil
// client against, and closeLanded has to recognise that before it dereferences it.
func TestWhenTheTrackerIsNilCloseLandedAttemptsNoUpdates(t *testing.T) {
	var buf bytes.Buffer
	closeLanded([]string{"OR-150"}, "https://forge/pull/396", "orion-ready",
		Deps{Jira: nil}, &buf)

	if buf.Len() != 0 {
		t.Errorf("closeLanded wrote %q with no tracker configured; nothing should "+
			"have been attempted, let alone reported", buf.String())
	}
}

// OR-314 case 13. Exercised through closeLanded rather than closeTicket
// directly (case 9 covers that call): the batch path must reach the same
// outcome for a workflow with no Done transition -- the labels still clear,
// the comment still lands, and the batch reports success with a warning
// rather than failing tickets that are already on the trunk.
func TestWhenTheWorkflowHasNoDoneTransitionLabelsAndCommentStillLandAndTheMergeSucceeds(t *testing.T) {
	jira := newTracker()
	jira.transitionErr = errors.New("no transition named Done")
	var buf bytes.Buffer

	closeLanded([]string{"OR-150"}, "https://forge/pull/396", "orion-ready",
		Deps{Jira: jira}, &buf)

	for _, label := range tracker.Managed("orion-ready") {
		if !hasLabel(jira.removed["OR-150"], label) {
			t.Errorf("the labels must still clear despite the missing transition, "+
				"missing %q", label)
		}
	}
	comments := strings.Join(jira.comments["OR-150"], "\n")
	if !strings.Contains(comments, "https://forge/pull/396") {
		t.Errorf("the comment must still be recorded despite the missing "+
			"transition, got:\n%s", comments)
	}
	if !strings.Contains(buf.String(), "OR-150") {
		t.Errorf("the missing transition must be reported as a warning, got:\n%s",
			buf.String())
	}
}

// OR-314 case 14. Test() reads the pull request URL on every tick, which for
// every tick after the first is the ordinary case: openPR returns nothing
// once the pull request already exists, so this read is the only one that
// answers. rememberBatchPR/batchPR are the package state that carries the
// answer from Test() (which learns it) to runBatch (which needs it), across
// however many ticks the batch takes.
func TestTheBatchPRURLIsCapturedFromTheStatusReadAndRememberedAcrossTicks(t *testing.T) {
	rememberBatchPR("https://forge/pull/396")
	if got := batchPR(); got != "https://forge/pull/396" {
		t.Fatalf("batchPR() = %q after one tick, want the URL that tick read", got)
	}

	// A later tick's status read finds no URL to report -- rememberBatchPR
	// must not overwrite the answer an earlier tick already gave.
	rememberBatchPR("")
	if got := batchPR(); got != "https://forge/pull/396" {
		t.Errorf("batchPR() = %q after an empty read, want the earlier tick's URL "+
			"still remembered", got)
	}

	// A pull request opened fresh for a later batch replaces the old answer.
	rememberBatchPR("https://forge/pull/402")
	if got := batchPR(); got != "https://forge/pull/402" {
		t.Errorf("batchPR() = %q, want the most recently read URL", got)
	}
}

// OR-314 case 15. The pull request URL has to survive a restart: a batch
// waiting on an approver spans ticks and may span processes, and the URL is
// what a landed member's ticket gets commented with.
func TestTheBatchPRURLIsStoredInThePersistedBatchStateRecord(t *testing.T) {
	dir := t.TempDir()
	st := batchState{
		Ref: "orion/batch", Base: "develop", Members: []string{"OR-150"},
		Status: batchValidated, BaseSHA: "abc123", PRURL: "https://forge/pull/396",
	}
	if err := saveBatchState(dir, st); err != nil {
		t.Fatalf("saveBatchState: %v", err)
	}

	got, ok := loadBatchState(dir)
	if !ok {
		t.Fatal("loadBatchState reported no record for one that was just saved")
	}
	if got.PRURL != "https://forge/pull/396" {
		t.Errorf("PRURL round-tripped as %q, want the URL the batch was saved with",
			got.PRURL)
	}
}
