package reconcile

import "testing"

func landedSet(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// The OR-211 case, and the reason this package exists. On 2026-08-30 that
// ticket read In Progress for hours while its finished work sat committed but
// never pushed: develop never received it, the milestone counted it, and a
// release cut on the tracker's word would have claimed a fix that was not in
// the binary.
func TestATicketReportedDoneWithNothingOnTheBranchIsAFinding(t *testing.T) {
	rep := Compare("develop", []Ticket{
		{Key: "OR-211", Status: "Done", Done: true, FixVersions: []string{"v0.8.3"}},
	}, landedSet())

	if rep.Clean() {
		t.Fatal("a ticket marked done with no commit on develop must be reported")
	}
	f := rep.Findings[0]
	if f.Kind != MissingFromBranch {
		t.Errorf("Kind = %q, want %q", f.Kind, MissingFromBranch)
	}
	if f.Evidence == "" {
		t.Error("a finding must carry the evidence that produced it, not just a verdict")
	}
}

// The mirror. Harmless to the binary, corrosive to the tracker.
func TestWorkOnTheBranchWithAnOpenTicketIsAFinding(t *testing.T) {
	rep := Compare("develop", []Ticket{
		{Key: "OR-240", Status: "In Progress", Done: false, FixVersions: []string{"v0.8.6"}},
	}, landedSet("OR-240"))

	if got := rep.Of(OpenButMerged); len(got) != 1 {
		t.Fatalf("findings = %v, want the merged-but-open case", rep.Findings)
	}
}

// Agreement is silence. A reconciler that always finds something is one
// nobody reads -- the OR-238 lesson, applied to its own output.
func TestATrackerThatAgreesWithTheBranchReportsNothing(t *testing.T) {
	rep := Compare("develop", []Ticket{
		{Key: "OR-1", Status: "Done", Done: true, FixVersions: []string{"v1"}},
		{Key: "OR-2", Status: "To Do", Done: false, FixVersions: []string{"v1"}},
	}, landedSet("OR-1"))

	if !rep.Clean() {
		t.Fatalf("expected no findings, got %v", rep.Findings)
	}
	if rep.Checked != 2 {
		t.Errorf("Checked = %d, want 2: a clean report must be distinguishable "+
			"from one where nothing was examined", rep.Checked)
	}
}

// Finished work on no milestone appears in no release's notes.
func TestFinishedWorkOnNoMilestoneIsAFinding(t *testing.T) {
	rep := Compare("develop", []Ticket{
		{Key: "OR-9", Status: "Done", Done: true},
	}, landedSet("OR-9"))

	if got := rep.Of(Unversioned); len(got) != 1 {
		t.Fatalf("findings = %v, want the no-milestone case", rep.Findings)
	}
	// It landed, so it must NOT also be reported as missing from the branch.
	if got := rep.Of(MissingFromBranch); len(got) != 0 {
		t.Errorf("landed work must not be reported as missing: %v", got)
	}
}

// Two independent faults on one ticket are two findings, because they need
// two different responses.
func TestOneTicketCanCarryTwoIndependentFindings(t *testing.T) {
	rep := Compare("develop", []Ticket{
		{Key: "OR-5", Status: "Done", Done: true}, // done, not landed, no milestone
	}, landedSet())

	if len(rep.Findings) != 2 {
		t.Fatalf("findings = %v, want both the missing-work and no-milestone faults", rep.Findings)
	}
}

// The comparison must be stable: an operator diffing two runs should see what
// changed, not a reshuffle.
func TestFindingsAreOrderedDeterministically(t *testing.T) {
	in := []Ticket{
		{Key: "OR-30", Status: "Done", Done: true, FixVersions: []string{"v1"}},
		{Key: "OR-10", Status: "Done", Done: true, FixVersions: []string{"v1"}},
		{Key: "OR-20", Status: "Done", Done: true, FixVersions: []string{"v1"}},
	}
	first := Compare("develop", in, landedSet()).Findings
	second := Compare("develop", in, landedSet()).Findings
	for i := range first {
		if first[i].Key != second[i].Key {
			t.Fatalf("ordering is not stable: %v vs %v", first, second)
		}
	}
	if first[0].Key != "OR-10" {
		t.Errorf("first = %s, want OR-10: findings sort by key", first[0].Key)
	}
}

// Keys are compared case-insensitively and whitespace-tolerantly, because
// they arrive from a branch name (orion/or-211) as often as from a commit.
func TestKeysMatchWhateverCaseTheyArriveIn(t *testing.T) {
	rep := Compare("develop", []Ticket{
		{Key: " or-211 ", Status: "Done", Done: true, FixVersions: []string{"v1"}},
	}, landedSet("OR-211"))

	if !rep.Clean() {
		t.Fatalf("a lower-case key from a branch name must match: %v", rep.Findings)
	}
}
