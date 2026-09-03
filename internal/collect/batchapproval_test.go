package collect

import (
	"strings"
	"testing"
)

// A green batch must not merge until somebody says so, and "not yet" must not
// look like a failure: the members are unmerged, unblamed, and offered again.
func TestAGreenBatchWaitsForApprovalRatherThanMerging(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2"), nil,
		WithApproval(func(string, []string) (bool, error) { return false, nil }))
	if err != nil {
		t.Fatalf("waiting on a person is not an error: %v", err)
	}
	if !b.AwaitingApproval {
		t.Error("AwaitingApproval is false; the caller cannot tell this batch apart " +
			"from one that finished")
	}
	if len(g.landed) != 0 {
		t.Errorf("merged %v without approval", g.landed)
	}
	if got := len(b.Members(Landed)); got != 0 {
		t.Errorf("%d members reported as landed while waiting for approval", got)
	}
	if got := len(b.Members(Culprit)); got != 0 {
		t.Errorf("%d members blamed; a batch nobody has approved yet is not a failure", got)
	}
}

// The gate is asked only AFTER the checks pass. Asking first trains an
// approver to say yes because they were asked, which is the rubber stamp the
// gate exists to prevent -- approvalFlow states the same rule for the
// per-branch path.
func TestApprovalIsAskedOnlyAfterTheChecksPass(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-1": true}}

	asked := false
	_, err := Land(g, tr, "batch", "develop", members("OR-1"), nil,
		WithApproval(func(string, []string) (bool, error) { asked = true; return true, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Error("a red batch asked for approval; the gate order is checks first, then ask")
	}
}

// Approval carries the merge through, and what merges is the ref that was
// tested.
func TestAnApprovedBatchLandsTheRefThatWasTested(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}

	var sawRef string
	var sawMembers []string
	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2"), nil,
		WithApproval(func(ref string, m []string) (bool, error) {
			sawRef, sawMembers = ref, m
			return true, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Green() {
		t.Fatalf("batch is not green: %v", b.Describe())
	}
	if len(g.landed) != 1 || g.landed[0] != "batch" {
		t.Errorf("landed %v, want the tested ref", g.landed)
	}
	if sawRef != "batch" {
		t.Errorf("approver was asked about %q, want the ref being landed", sawRef)
	}
	// The approver must be told WHICH tickets, or the question is unanswerable.
	if len(sawMembers) != 2 {
		t.Errorf("approver was given %v; it has to name the tickets about to merge",
			sawMembers)
	}
}

// An approver that fails is not an approval. The distinction matters because
// the safe reading of "I could not ask" is "do not merge".
func TestAnApproverThatErrorsDoesNotMerge(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}

	_, err := Land(g, tr, "batch", "develop", members("OR-1"), nil,
		WithApproval(func(string, []string) (bool, error) {
			return false, errNoSlack
		}))
	if err == nil {
		t.Fatal("an unanswerable approval merged; want a refusal")
	}
	if !strings.Contains(err.Error(), "approval") {
		t.Errorf("error = %q, want it to name approval as what failed", err)
	}
	if len(g.landed) != 0 {
		t.Errorf("merged %v despite being unable to ask", g.landed)
	}
}

// resumable() is the guard that stops a recorded proof being reused for a set
// nobody proved. Each case is a different way of getting that wrong.
func TestAProofIsOnlyReusedForExactlyWhatWasProved(t *testing.T) {
	base := batchState{
		Ref: "orion/batch", Base: "develop", Status: batchValidated,
		BaseSHA: "abc", Members: []string{"OR-1", "OR-2"},
	}
	two := members("OR-1", "OR-2")

	for _, tc := range []struct {
		name  string
		state batchState
		sha   string
		mem   []Member
		want  bool
	}{
		{"the same set on the same base", base, "abc", two, true},
		{"order does not matter", base, "abc", members("OR-2", "OR-1"), true},
		{"the base moved", base, "def", two, false},
		{"a member was added", base, "abc", members("OR-1", "OR-2", "OR-3"), false},
		{"a member is missing", base, "abc", members("OR-1"), false},
		{"a different member", base, "abc", members("OR-1", "OR-9"), false},
		{"a different work branch", base, "abc", two, true}, // overridden below
		{"never validated", batchState{Ref: "r", Base: "develop", BaseSHA: "abc",
			Members: []string{"OR-1", "OR-2"}}, "abc", two, false},
		{"no recorded base sha", batchState{Ref: "r", Base: "develop",
			Status: batchValidated, Members: []string{"OR-1", "OR-2"}}, "abc", two, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := "develop"
			if tc.name == "a different work branch" {
				b, tc.want = "main", false
			}
			if got := tc.state.resumable(b, tc.sha, tc.mem); got != tc.want {
				t.Errorf("resumable = %v, want %v", got, tc.want)
			}
		})
	}
}

// errNoSlack stands in for the real "configured approvers, no way to ask
// them" refusal, which is the only case batchApprover fails rather than
// degrading.
var errNoSlack = errApprovalUnavailable{}

type errApprovalUnavailable struct{}

func (errApprovalUnavailable) Error() string {
	return "Slack is not available to ask the approvers"
}

// TestAnApprovedBatchIsNotAskedAboutTwice is OR-318.
//
// The approval record used to be cleared the moment a human approved. The
// batch ref name is constant -- every batch is orion/batch -- so the next
// pass found no record, decided nobody had been asked, and posted the same
// request to Slack again. Observed as four asks for two batches, each
// already carrying a green tick.
//
// The risk is not the duplicate message. That record holds the message
// timestamp the decision is read from, so forgetting it means a later pass
// can read a reaction from a message other than the one a person answered.
func TestAnApprovedBatchIsNotAskedAboutTwice(t *testing.T) {
	dir := t.TempDir()
	key := batchRequestKey("orion/batch")
	members := []string{"OR-310", "OR-309"}

	// The ask.
	if err := saveRequest(dir, Request{
		Key: key, Channel: "C1", TS: "1.1", Members: members,
	}); err != nil {
		t.Fatal(err)
	}
	// The approval, which used to clear the record.
	if err := saveRequest(dir, Request{
		Key: key, Channel: "C1", TS: "1.1", Members: members, Decided: true,
	}); err != nil {
		t.Fatal(err)
	}

	got, ok := loadRequests(dir).Requests[key]
	if !ok {
		t.Fatal("the approved batch was forgotten, so the next pass will ask again")
	}
	if !got.Decided {
		t.Error("the record does not say the batch was decided")
	}
	if !sameKeys(got.Members, members) {
		t.Errorf("members not recorded: %v", got.Members)
	}
}

// TestADifferentBatchOnTheSameRefIsAskedAfresh. The ref is reused, so the
// record alone cannot say whether this is the same question. Members can.
func TestADifferentBatchOnTheSameRefIsAskedAfresh(t *testing.T) {
	if sameKeys([]string{"OR-310"}, []string{"OR-310", "OR-309"}) {
		t.Error("a batch that gained a member was treated as the same batch")
	}
	// Order is not significant: the queue assembles in whatever order it
	// likes, and a human approving two tickets approved the same pair either
	// way round.
	if !sameKeys([]string{"OR-310", "OR-309"}, []string{"OR-309", "OR-310"}) {
		t.Error("a reshuffle that changed nothing was treated as a new batch")
	}
}
