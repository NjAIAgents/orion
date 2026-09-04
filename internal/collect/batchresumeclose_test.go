package collect

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// OR-314 cases 16-20: landResumed, the half of the fix that closes tickets for
// a batch approved on a LATER tick than the one that tested it -- a real gap,
// since approval is human-length and the process that merges may not be the
// one that opened the pull request.

// noopMerge stands in for the real merge: landResumed's tracker-closing
// behaviour does not depend on what merging actually does to the tree, only
// on it succeeding.
func noopMerge(string, string, string, string) error { return nil }

// landResumedFixture builds one real origin/clone pair with a batch ref ready
// to land, and a batchState whose BaseSHA matches -- exactly what resumeBatch
// hands landResumed once an approver has said yes.
func landResumedFixture(t *testing.T, ms []Member) (batchState, repoGit, *workspace.Workspace, config.Config) {
	t.Helper()
	_, clone := repos(t)
	ws := &workspace.Workspace{Dir: filepath.Dir(clone)}
	baseSHA := head(t, clone, "origin/develop")
	gitRun(t, clone, "branch", "orion/batch", "origin/develop")

	g := repoGit{ws: ws, merge: noopMerge}
	st := batchState{Ref: "orion/batch", Base: "develop", BaseSHA: baseSHA, Members: keysOf(ms)}
	cfg := config.Config{Tracker: config.Tracker{QueueLabel: "orion-ready"}}
	return st, g, ws, cfg
}

// Case 16. The record is checked FIRST, and only an empty record falls back
// to what Test last read in-memory -- the record may be a different process's
// read of a different, later pull request than the one this process saw.
func TestAResumedBatchPrefersTheRecordedPRURLOverTheRememberedOne(t *testing.T) {
	ms := members("OR-150")
	st, g, ws, cfg := landResumedFixture(t, ms)
	st.PRURL = "https://forge/pull/500"
	rememberBatchPR("https://forge/pull/999") // a different, stale in-memory read
	defer rememberBatchPR("")

	jira := newTracker()
	var buf bytes.Buffer

	landResumed(st, ms, cfg, Deps{Jira: jira}, g, ws, &buf)

	comments := strings.Join(jira.comments["OR-150"], "\n")
	if !strings.Contains(comments, "https://forge/pull/500") {
		t.Errorf("the recorded PR URL was not used; comments:\n%s", comments)
	}
	if strings.Contains(comments, "https://forge/pull/999") {
		t.Errorf("the stale in-memory URL was used over the recorded one; comments:\n%s", comments)
	}
}

// Case 16, the fallback half. When the record carries no URL -- a batch
// approved by a process that never itself read the pull request -- the
// in-memory value Test last saw is what the ticket gets commented with.
func TestAResumedBatchFallsBackToTheRememberedPRURLWhenTheRecordIsEmpty(t *testing.T) {
	ms := members("OR-150")
	st, g, ws, cfg := landResumedFixture(t, ms)
	st.PRURL = ""
	rememberBatchPR("https://forge/pull/777")
	defer rememberBatchPR("")

	jira := newTracker()
	var buf bytes.Buffer

	landResumed(st, ms, cfg, Deps{Jira: jira}, g, ws, &buf)

	comments := strings.Join(jira.comments["OR-150"], "\n")
	if !strings.Contains(comments, "https://forge/pull/777") {
		t.Errorf("the remembered PR URL was not used as a fallback; comments:\n%s", comments)
	}
}

// Case 17. A batch resumed on a later tick -- CI already ran, an approver was
// asked, and only now does landResumed run -- still closes every member it
// landed, using the URL the record (or the fallback) supplies.
func TestAResumedBatchClosesAllItsLandedMembers(t *testing.T) {
	ms := members("OR-150", "OR-151", "OR-152")
	st, g, ws, cfg := landResumedFixture(t, ms)
	st.PRURL = "https://forge/pull/500"

	jira := newTracker()
	var buf bytes.Buffer

	landResumed(st, ms, cfg, Deps{Jira: jira}, g, ws, &buf)

	for _, key := range []string{"OR-150", "OR-151", "OR-152"} {
		if got := jira.transitions[key]; got != "Done" {
			t.Errorf("%s: transition = %q, want Done", key, got)
		}
		for _, label := range tracker.Managed("orion-ready") {
			if !hasLabel(jira.removed[key], label) {
				t.Errorf("%s kept %q; a resumed landed batch must clear it too", key, label)
			}
		}
	}
}

// Case 18. Every landed member of the same batch is commented with the SAME
// pull request -- they merged together, as one ref, and the address for
// "where did this land" cannot differ member to member.
func TestAResumedBatchCommentsEveryLandedMemberWithTheSameBatchPRURL(t *testing.T) {
	ms := members("OR-150", "OR-151")
	st, g, ws, cfg := landResumedFixture(t, ms)
	st.PRURL = "https://forge/pull/500"

	jira := newTracker()
	var buf bytes.Buffer

	landResumed(st, ms, cfg, Deps{Jira: jira}, g, ws, &buf)

	for _, key := range []string{"OR-150", "OR-151"} {
		comments := strings.Join(jira.comments[key], "\n")
		if !strings.Contains(comments, "https://forge/pull/500") {
			t.Errorf("%s was not commented with the batch's PR URL; comments:\n%s", key, comments)
		}
	}
}

// Case 19. A batch of one member lands and closes correctly -- the smallest
// shape a batch takes, and the one that most resembles the per-branch path it
// must not silently regress to something less than.
func TestAResumedSingleMemberBatchClosesCorrectly(t *testing.T) {
	ms := members("OR-150")
	st, g, ws, cfg := landResumedFixture(t, ms)
	st.PRURL = "https://forge/pull/500"

	jira := newTracker()
	var buf bytes.Buffer

	landResumed(st, ms, cfg, Deps{Jira: jira}, g, ws, &buf)

	if got := jira.transitions["OR-150"]; got != "Done" {
		t.Errorf("transition = %q, want Done", got)
	}
	comments := strings.Join(jira.comments["OR-150"], "\n")
	if !strings.Contains(comments, "https://forge/pull/500") {
		t.Errorf("the single member was not commented with the PR URL; comments:\n%s", comments)
	}
}

// Case 20. A batch where nothing landed -- every member ejected or isolated
// as the culprit -- must not touch the tracker at all. This is runBatch's own
// derivation (b.Members(Landed)) rather than landResumed's: a batch that never
// gets approval never reaches landResumed, so the "nothing landed" case is
// exercised the same way runBatch itself produces it.
func TestABatchWhereNothingLandedClosesNoTickets(t *testing.T) {
	b := Batch{Results: []MemberResult{
		{Member: Member{Key: "OR-9"}, Outcome: Culprit},
		{Member: Member{Key: "OR-152"}, Outcome: Ejected},
		{Member: Member{Key: "OR-153"}, Outcome: Ejected},
	}}
	landed := b.Members(Landed)
	if len(landed) != 0 {
		t.Fatalf("fixture landed %v, want none", landed)
	}

	jira := newTracker()
	var buf bytes.Buffer
	closeLanded(landed, "https://forge/pull/500", "orion-ready", Deps{Jira: jira}, &buf)

	for _, key := range []string{"OR-9", "OR-152", "OR-153"} {
		if jira.transitions[key] != "" || len(jira.removed[key]) > 0 || len(jira.comments[key]) > 0 {
			t.Errorf("%s was touched though nothing in the batch landed: "+
				"transition=%q labels=%v comments=%v",
				key, jira.transitions[key], jira.removed[key], jira.comments[key])
		}
	}
}

// The landing line says WHAT was skipped (OR-336).
//
// It read "landed 1 approved branch(es) as one, with no further CI run",
// which sounds like a claim about CI in general -- and merging to the work
// branch starts that branch's own checks a second later, so the next thing
// on screen appeared to contradict it. The saving is real but narrower: the
// BATCH REF was not tested again, because it was already green and the tree
// that merges is the tree that was tested.
func TestTheLandingLineNamesWhatWasNotTestedAgain(t *testing.T) {
	ms := members("OR-150")
	st, g, ws, cfg := landResumedFixture(t, ms)

	var buf bytes.Buffer
	landResumed(st, ms, cfg, Deps{Jira: newTracker()}, g, ws, &buf)

	got := buf.String()
	// The ref that was not re-tested, and the branch that now runs its own.
	for _, want := range []string{"orion/batch", "develop", "already green"} {
		if !strings.Contains(got, want) {
			t.Errorf("the landing line does not mention %q:\n%s", want, got)
		}
	}
	// The old wording claimed more than it meant.
	if strings.Contains(got, "no further CI run") {
		t.Errorf("the line still reads as a claim about CI in general:\n%s", got)
	}
}
