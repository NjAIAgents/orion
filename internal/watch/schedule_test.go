package watch

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// A labelled ticket with no fixVersion must never be claimed, and the
// condition must be in the JQL rather than applied after the fetch.
//
// OR-221. The label means "ready to be worked"; a fixVersion means "scheduled
// to ship in a named release". Neither implies the other, and work that is
// ready but unscheduled is work nobody has decided to pay for. Filtering
// after the fetch would still let an unschedulable ticket enter the candidate
// set and be claimed in a race with a second watcher.
func TestTheQueueRequiresAnOpenMilestoneAsWellAsTheLabel(t *testing.T) {
	sched := tracker.Schedules{"OR": {"v0.8.6"}}

	jql := queuedJQL([]string{"OR"}, "ORION", sched)

	if !strings.Contains(jql, `fixVersion IN ("v0.8.6")`) {
		t.Fatalf("an unscheduled ticket is still claimable: %s", jql)
	}
	// And the rest of the criterion survives alongside it.
	for _, want := range []string{
		`project = "OR"`,
		`labels = "ORION"`,
		`labels NOT IN ("orion-working", "orion-ci-wait", "orion-failed")`,
		`statusCategory != "Done"`,
		" ORDER BY priority DESC, Rank ASC",
	} {
		if !strings.Contains(jql, want) {
			t.Errorf("lost %s from the queue query: %s", want, jql)
		}
	}
}

// A project that has never defined a version keeps exactly the query it had.
//
// Orion adopts arbitrary repositories; enforcing a release convention on a
// project that never opted into one would halt it completely.
func TestAProjectWithoutVersionsKeepsTheOldQuery(t *testing.T) {
	sched := tracker.Schedules{"FCIA": nil}

	jql := queuedJQL([]string{"FCIA"}, "ORION", sched)

	if !strings.Contains(jql, `project IN ("FCIA")`) {
		t.Errorf("an unversioned project was gated anyway: %s", jql)
	}
	if strings.Contains(jql, "fixVersion") {
		t.Errorf("an unversioned project must have no milestone clause: %s", jql)
	}
	// And nothing can be reported held, because nothing is being held.
	if got := heldJQL([]string{"FCIA"}, "ORION", sched); got != "" {
		t.Errorf("held query built for a project that enforces nothing: %s", got)
	}
}

// The held query is the claim query with the milestone requirement inverted:
// same label, same in-flight exclusions, same status filter. Anything else
// would report as held a ticket that was never a candidate to begin with.
func TestTheHeldQueryIsTheClaimQueryInverted(t *testing.T) {
	sched := tracker.Schedules{"OR": {"v0.8.6"}}

	jql := heldJQL([]string{"OR"}, "ORION", sched)

	for _, want := range []string{
		`project = "OR"`,
		`fixVersion NOT IN ("v0.8.6")`,
		"fixVersion IS EMPTY",
		`labels = "ORION"`,
		`labels NOT IN ("orion-working", "orion-ci-wait", "orion-failed")`,
		`statusCategory != "Done"`,
	} {
		if !strings.Contains(jql, want) {
			t.Errorf("lost %s from the held query: %s", want, jql)
		}
	}
}

// An empty label falls back to the default in the held query too, or the
// report names a label nothing carries.
func TestTheHeldQueryDefaultsItsLabel(t *testing.T) {
	jql := heldJQL([]string{"OR"}, "", tracker.Schedules{"OR": {"v0.8.6"}})
	if !strings.Contains(jql, `labels = "`+tracker.QueueLabelDefault+`"`) {
		t.Errorf("got %s", jql)
	}
}

// A ticket held back for having no release must be REPORTED, not silently
// skipped -- and on the tick that starts nothing, which is the tick an
// operator is staring at wondering whether the watcher is broken.
func TestAHeldTicketIsReportedRatherThanSilentlySkipped(t *testing.T) {
	stopping.Store(false)
	s := &spy{maxSleeps: 1}
	s.held = []HeldTicket{
		{Key: "OR-300", Reason: "labelled ORION but not attached to a release, " +
			"so it will not be claimed"},
	}

	var buf bytes.Buffer
	if err := Run(Options{Out: &buf, Home: t.TempDir(), Once: true,
		Interval: time.Millisecond}, s.deps()); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "OR-300") {
		t.Errorf("the held ticket was never named:\n%s", out)
	}
	if !strings.Contains(out, "not attached to a release") {
		t.Errorf("the reason was never given:\n%s", out)
	}
}

// Several tickets held for one reason are ONE line, not one line each.
//
// The console collapses a run of identical lines into a count (OR-217), and
// that only works while the line does not change. One line per ticket would
// alternate and defeat it, putting a growing block on screen every tick all
// night -- which is how a report becomes noise and stops being read.
func TestHeldTicketsSharingAReasonAreReportedOnOneLine(t *testing.T) {
	var buf bytes.Buffer
	reason := "labelled ORION but not attached to a release, so it will not be claimed"
	ui.Reset(&buf)
	reportHeld(&buf, []HeldTicket{
		{Key: "OR-300", Reason: reason},
		{Key: "OR-301", Reason: reason},
		{Key: "OR-302", Reason: "labelled ORION but attached only to a release that " +
			"has already closed, so it will not be claimed"},
	})
	ui.Flush(&buf)

	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("want one line per REASON (2), got %d:\n%s", lines, buf.String())
	}
	if !strings.Contains(buf.String(), "OR-300, OR-301") {
		t.Errorf("tickets sharing a reason were not grouped:\n%s", buf.String())
	}
}
