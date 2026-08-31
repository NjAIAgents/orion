package watch

// THE TESTS THAT GUARD THE LOCK.
//
// OR-225 asked for a claimed ticket to say WHICH actor holds it. The obvious
// implementation -- renaming orion-working to orion-working-dev,
// orion-working-qa and so on -- breaks the claim lock, and these tests exist
// to make that break loud rather than silent.
//
// orion-working is not a status label. It IS the mutual-exclusion lock, and
// it is matched EXACTLY in two places: InFlight's JQLEq, and the queue's
// JQLNotIn. Jira Cloud cannot reliably prefix-match a label, so a claim
// wearing a suffixed lock label would be invisible to both -- every claimed
// ticket would read as free and two watchers would work one branch. That is
// the race OR-138 and OR-144 exist to prevent, and it would appear as
// corrupted work rather than as an error.

import (
	"io"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// The queue's exclusion enumerates the labels it excludes, which is exactly
// why a stage label must never join it. An enumeration is a list somebody
// has to remember to update, and the day a new actor's stage label is
// missing from it is the day that actor's claims stop excluding the ticket
// from the queue.
func TestTheQueueQueryNeverMentionsAStageLabel(t *testing.T) {
	jql := queuedJQL([]string{"OR"}, "ORION", nil)
	if strings.Contains(jql, actors.StageLabelPrefix) {
		t.Fatalf("the queue query enumerates stage labels, so a new actor would "+
			"silently widen the lock's blind spot: %s", jql)
	}
	// And it still excludes the lock itself, exactly.
	if !strings.Contains(jql, tracker.LabelWorking) {
		t.Errorf("the queue no longer excludes claimed tickets: %s", jql)
	}
}

// The in-flight query matches the lock label EXACTLY. A stage label carrying
// the same prefix would be a second thing that has to match, and there is no
// way to ask for both in one exact comparison.
func TestInFlightMatchesTheLockLabelAndNothingElse(t *testing.T) {
	home := lockHome(t)
	j := &fakeLock{}
	if _, err := InFlight(j, home, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(j.searches) != 1 {
		t.Fatalf("expected one search, got %v", j.searches)
	}
	jql := j.searches[0]
	if strings.Contains(jql, actors.StageLabelPrefix) {
		t.Fatalf("the in-flight query reads a stage label; a claim it did not "+
			"enumerate would read as free: %s", jql)
	}
	if !strings.Contains(jql, tracker.JQLEq("labels", tracker.LabelWorking)) {
		t.Errorf("the in-flight query no longer matches %s exactly: %s",
			tracker.LabelWorking, jql)
	}
}

// A CLAIMED TICKET WEARING A STAGE LABEL IS STILL CLAIMED. This is the
// behaviour the whole two-label design buys: the extra label is inert, so
// the lock reads identically with it and without it.
func TestAStageLabelDoesNotChangeWhetherATicketIsClaimed(t *testing.T) {
	home := lockHome(t)
	j := &fakeLock{issues: []tracker.Issue{
		{Key: "OR-130", Status: "In Progress", StatusCategory: "indeterminate",
			Labels: []string{tracker.LabelWorking, actors.StageLabel("qa")}},
	}}
	running, err := InFlight(j, home, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0] != "OR-130" {
		t.Fatalf("InFlight = %v; a claim wearing a stage label must still hold a slot", running)
	}
	if len(j.removed) != 0 {
		t.Errorf("a live claim was cleared: %v", j.removed)
	}
}

// An UNRECOGNISED stage label -- written by a newer build that knows an
// actor this one does not -- must degrade to today's behaviour rather than
// fail anything. Nothing reads the label for control flow, so the only
// consequence of one this build cannot name is a less informative view.
func TestAnUnknownStageLabelChangesNothing(t *testing.T) {
	home := lockHome(t)
	j := &fakeLock{issues: []tracker.Issue{
		{Key: "OR-130", Status: "In Progress", StatusCategory: "indeterminate",
			Labels: []string{tracker.LabelWorking, actors.StageLabelPrefix + "security-reviewer"}},
	}}
	running, err := InFlight(j, home, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0] != "OR-130" {
		t.Fatalf("InFlight = %v; an unknown stage label must not affect the lock", running)
	}
	// And the state a reader is shown is decided by the lock, not the stage.
	if got := tracker.State(j.issues[0].Labels, "ORION"); got != "working" {
		t.Errorf("State = %q, want %q", got, "working")
	}
	// A stage label ALONE is not a state. It cannot make a ticket look
	// claimed, which is the property that keeps it from ever becoming a lock
	// by accident.
	if got := tracker.State([]string{actors.StageLabel("qa")}, "ORION"); got != "" {
		t.Errorf("a stage label alone reported the state %q; it is not a state", got)
	}
}

// tracker.Managed is spliced into a JQL by `orion queue`, so it is the third
// enumeration a stage label must stay out of. Clearing paths use
// actors.StageLabels() alongside it instead.
func TestManagedStaysTheLockLabelsOnly(t *testing.T) {
	for _, l := range tracker.Managed("ORION") {
		if strings.HasPrefix(l, actors.StageLabelPrefix) {
			t.Errorf("Managed() offers %q, and Managed() is read into a query", l)
		}
	}
}
