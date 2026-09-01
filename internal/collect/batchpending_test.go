package collect

import (
	"errors"
	"testing"
	"time"
)

// pendingTester never has an answer, which is what a build in progress looks
// like. It also records how long it was called for, so a test can assert the
// thing that actually matters: that nobody waited.
type pendingTester struct{ calls int }

func (p *pendingTester) Test(string) (bool, error) {
	p.calls++
	return false, ErrCheckPending
}

// The whole of OR-251 in one assertion: a batch whose CI has not reported
// returns immediately, so the tick that called it can carry on reporting
// every other ticket.
//
// The tick's own doc comment is the contract being kept here: "a tick that
// blocked on the agent could never start a second".
func TestABuildInProgressReturnsAtOnceRatherThanWaiting(t *testing.T) {
	g := newFakeGit()
	tr := &pendingTester{}

	start := time.Now()
	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2"), nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a build in progress is not an error: %v", err)
	}
	if !b.Pending {
		t.Error("Pending is false; the caller cannot tell a build in progress " +
			"from a finished batch, which is the whole distinction")
	}
	if elapsed > time.Second {
		t.Errorf("Land took %s waiting for CI; it must return at once so the tick "+
			"keeps reporting the other tickets", elapsed)
	}
	if tr.calls != 1 {
		t.Errorf("the checks were read %d times; one read per tick, never a poll loop",
			tr.calls)
	}
}

// A pending build is NOT a run spent. Counting one per tick would inflate the
// number the whole design is justified by, for as long as the build takes.
func TestAPendingBuildDoesNotCountAsACIRun(t *testing.T) {
	b, err := Land(newFakeGit(), &pendingTester{}, "batch", "develop",
		members("OR-1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Runs != 0 {
		t.Errorf("Runs = %d, want 0: nothing has reported, so nothing has been spent",
			b.Runs)
	}
}

// Nothing merges on a pending build, and nothing is blamed for one.
func TestAPendingBuildNeitherMergesNorBlames(t *testing.T) {
	g := newFakeGit()
	b, _ := Land(g, &pendingTester{}, "batch", "develop", members("OR-1", "OR-2"), nil)

	if len(g.landed) != 0 {
		t.Errorf("merged %v before the checks reported", g.landed)
	}
	if n := len(b.Members(Culprit)); n != 0 {
		t.Errorf("%d members blamed for a build that has not finished", n)
	}
	if n := len(b.Members(Landed)); n != 0 {
		t.Errorf("%d members landed on no evidence", n)
	}
}

// The deadline moved to the record, and it still refuses silence.
func TestTheDeadlineLivesInTheRecordAndStillRefusesSilence(t *testing.T) {
	since := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	st := batchState{Status: batchTesting, TestingSince: since}

	if st.waitedOut(since.Add(29*time.Minute), 30*time.Minute) {
		t.Error("gave up at 29 minutes; the deadline is 30")
	}
	if !st.waitedOut(since.Add(31*time.Minute), 30*time.Minute) {
		t.Error("did not give up at 31 minutes; silence must never be read as green")
	}
	// A record that is not testing has no deadline to exceed.
	valid := batchState{Status: batchValidated, TestingSince: since}
	if valid.waitedOut(since.Add(time.Hour), 30*time.Minute) {
		t.Error("a validated batch waited out a testing deadline")
	}
}

// A caller that does not know about ErrCheckPending must not read it as
// success. Errors.Is is the seam; the safe default is the point.
func TestAnUnawareCallerTreatsPendingAsAnError(t *testing.T) {
	_, err := (&pendingTester{}).Test("batch")
	if err == nil {
		t.Fatal("a pending build reported no error; an unaware caller would " +
			"read the false bool and treat it as a red build, or worse")
	}
	if !errors.Is(err, ErrCheckPending) {
		t.Errorf("err = %v, want it to match ErrCheckPending", err)
	}
}
