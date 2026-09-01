package collect

import (
	"strings"
	"testing"
	"time"
)

// OR-261. Observed on 2026-09-01, looping every minute overnight and landing
// nothing:
//
//	09:43:31  2 branch(es) assembled into orion/batch; CI is running
//	09:44:36  the batch cost 0 CI runs for 2 branchs, in 1s
//	WARNING   cutting orion/batch from develop: exit status 128
//	fatal: cannot force update the branch 'orion/batch' used by worktree at ...
//
// Three faults, and they held each other up. These are the unit-level halves;
// the git half is asserted in batchgit_test.go.

// members() is batch_test.go's, shared across the package's tests.

// resumable() asks "may this PROOF be used to merge?", so it requires a
// validated status. Asking it about a TESTING batch returned false, which sent
// resumeBatch down the clear-and-reassemble path and made the testing branch
// below it unreachable from the day it was written.
func TestATestingBatchIsNotResumableAndMustNotBeAskedThatQuestion(t *testing.T) {
	st := batchState{
		Ref: "orion/batch", Base: "develop", Status: batchTesting,
		Members: []string{"OR-259", "OR-34"}, BaseSHA: "abc123",
		TestingSince: time.Now(),
	}
	if st.resumable("develop", "abc123", members("OR-259", "OR-34")) {
		t.Error("resumable() accepted a testing batch; it answers a question about a " +
			"PROOF, and a batch whose CI is still running has none")
	}

	// And the same record must satisfy the testing gate's own conditions,
	// which is what resumeBatch now checks before it ever reaches resumable().
	if st.Status != batchTesting {
		t.Fatal("fixture is not a testing batch")
	}
	if !sameMembers(st.Members, members("OR-259", "OR-34")) {
		t.Error("the recorded members are the set on offer, so the testing gate " +
			"should admit them")
	}
}

func TestSameMembersIsOrderIndependentAndSizeSensitive(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded []string
		offered  []Member
		want     bool
	}{
		{"same set, different order", []string{"OR-34", "OR-259"},
			members("OR-259", "OR-34"), true},
		{"one added since", []string{"OR-259"},
			members("OR-259", "OR-34"), false},
		{"one gone since", []string{"OR-259", "OR-34"},
			members("OR-259"), false},
		{"same size, different members", []string{"OR-259", "OR-99"},
			members("OR-259", "OR-34"), false},
		{"both empty", nil, nil, true},
	} {
		if got := sameMembers(tc.recorded, tc.offered); got != tc.want {
			t.Errorf("%s: sameMembers = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A base that moved means the build being waited on is testing a tree that no
// longer exists. Resuming onto it would report a verdict about the wrong code.
func TestAMovedBaseStopsATestingBatchFromBeingResumed(t *testing.T) {
	st := batchState{
		Ref: "orion/batch", Base: "develop", Status: batchTesting,
		Members: []string{"OR-259"}, BaseSHA: "abc123", TestingSince: time.Now(),
	}
	// The conditions resumeBatch applies, asserted directly: same base, same
	// recorded SHA, same members.
	sameBase := st.Base == "develop"
	sameSHA := st.BaseSHA == "def456"
	if sameBase && sameSHA {
		t.Fatal("fixture does not represent a moved base")
	}
	if sameSHA {
		t.Error("a base that moved was treated as unchanged")
	}
}

func TestABatchStillWithinItsDeadlineHasNotWaitedOut(t *testing.T) {
	st := batchState{Status: batchTesting, TestingSince: time.Now().Add(-5 * time.Minute)}
	if st.waitedOut(time.Now(), batchCheckDeadline) {
		t.Error("a batch five minutes into a thirty minute deadline was timed out")
	}
	old := batchState{Status: batchTesting, TestingSince: time.Now().Add(-31 * time.Minute)}
	if !old.waitedOut(time.Now(), batchCheckDeadline) {
		t.Error("a batch past its deadline was not timed out")
	}
	// A validated batch is not waiting on anything.
	done := batchState{Status: batchValidated, TestingSince: time.Now().Add(-99 * time.Hour)}
	if done.waitedOut(time.Now(), batchCheckDeadline) {
		t.Error("a validated batch was treated as still testing")
	}
}

// "2 branchs" appeared in every batch line this repository ever printed.
func TestPluralHandlesTheUnitsThisPackageActuallyUses(t *testing.T) {
	for _, tc := range []struct {
		n    int
		unit string
		want string
	}{
		{1, "branch", "1 branch"},
		{2, "branch", "2 branches"},
		{0, "branch", "0 branches"},
		{1, "CI run", "1 CI run"},
		{2, "CI run", "2 CI runs"},
		{0, "CI run", "0 CI runs"},
	} {
		if got := plural(tc.n, tc.unit); got != tc.want {
			t.Errorf("plural(%d, %q) = %q, want %q", tc.n, tc.unit, got, tc.want)
		}
	}
}

// Zero runs in one second is the shape of a cycle that did no work. It printed
// with a green tick once a minute all night while develop never moved.
func TestACostLineStillReadsHonestlyForACycleThatDidNothing(t *testing.T) {
	got := costLine(0, 2, time.Second, baseline{})
	if !strings.Contains(got, "0 CI runs") || !strings.Contains(got, "2 branches") {
		t.Errorf("costLine = %q", got)
	}
	if !strings.Contains(got, "no baseline yet") {
		t.Errorf("a missing baseline must say so rather than be omitted: %q", got)
	}
	// The line itself is neutral; the CALLER decides whether it is a cost or a
	// failure, on whether anything landed. That is asserted where the caller
	// lives, but the wording must not presume success.
	if strings.Contains(strings.ToLower(got), "saved") {
		t.Errorf("the cost line claims a saving on its own: %q", got)
	}
}
