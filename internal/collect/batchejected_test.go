package collect

import (
	"bytes"
	"strings"
	"testing"
)

// OR-554: LTA-2's branch was ejected at assembly (it conflicted), the batch
// record still listed it, and the landing closed it with its 28 sub-tasks --
// work that never reached the base. Only what the record says MERGED lands.
func TestALandingClosesOnlyTheMembersThatMerged(t *testing.T) {
	offered := members("OR-150", "OR-151")
	st, g, ws, cfg := landResumedFixture(t, offered)
	st.Members, st.Offered = []string{"OR-150"}, []string{"OR-150", "OR-151"}

	jira := newTracker()
	res := landResumed(st, offered, cfg, Deps{Jira: jira}, g, ws, &bytes.Buffer{})

	verdict := map[string]Verdict{}
	for _, r := range res {
		verdict[r.Key] = r.Verdict
	}
	if verdict["OR-150"] != VerdictMerged || verdict["OR-151"] != VerdictStale {
		t.Fatalf("verdicts = %v, want OR-150 merged and OR-151 left for the next batch", verdict)
	}
	if _, closed := jira.transitions["OR-151"]; closed || len(jira.comments["OR-151"]) > 0 {
		t.Fatalf("the ejected member was touched in the tracker: transition %q, comments %v",
			jira.transitions["OR-151"], jira.comments["OR-151"])
	}
	if jira.transitions["OR-150"] == "" {
		t.Fatal("the merged member was not closed")
	}
}

// The merged set excludes ejected and deferred members, keeping order.
func TestMergedMembersDropsEjectedAndDeferred(t *testing.T) {
	ms := members("A-1", "A-2", "A-3")
	b := Batch{Results: []MemberResult{
		{Member: ms[1], Outcome: Ejected, Reason: "conflicts with the batch: model.py"},
		{Member: ms[2], Outcome: Deferred},
	}}
	got := keysOf(mergedMembers(b, ms))
	if strings.Join(got, ",") != "A-1" {
		t.Fatalf("merged = %v, want only A-1", got)
	}
}

// While CI runs, an ejected member is said, with its reason, and is not
// reported pending -- the ejection used to reach only the board.
func TestAnEjectedMemberIsSaidAndNotPending(t *testing.T) {
	ms := members("LTA-2", "LTA-118")
	b := Batch{Ref: "orion/batch", Results: []MemberResult{
		{Member: ms[0], Outcome: Ejected, Reason: "conflicts with the batch: src/log_triage/model.py"},
	}}
	var buf bytes.Buffer
	res := pendingWithEjected(b, ms[1:], ms, &buf)
	if res[0].Verdict != VerdictStale || res[1].Verdict != VerdictPending {
		t.Fatalf("results = %+v", res)
	}
	if !strings.Contains(buf.String(), "LTA-2") || !strings.Contains(buf.String(), "model.py") {
		t.Fatalf("the ejection was not said with its reason:\n%s", buf.String())
	}
}

// The record is recognised by what was OFFERED, so the next pass waits on
// the build instead of reassembling; an old record without Offered still
// matches on its members.
func TestTheRecordIsRecognisedByWhatWasOffered(t *testing.T) {
	st := batchState{Members: []string{"LTA-118"}, Offered: []string{"LTA-118", "LTA-2"}}
	if !sameMembers(st.offered(), members("LTA-2", "LTA-118")) {
		t.Fatal("the offered set did not match the same candidates")
	}
	old := batchState{Members: []string{"LTA-118"}}
	if !sameMembers(old.offered(), members("LTA-118")) {
		t.Fatal("a pre-OR-554 record no longer matches its own members")
	}
}
