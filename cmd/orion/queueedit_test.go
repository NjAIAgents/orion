package main

// Unit coverage of the queue plan: what `orion queue add|remove` DECIDES,
// separately from the Jira wiring that carries it out (OR-223). Every refusal
// in this file is one an operator hit by hand on the night the command was
// written.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

const qLabel = "ORION"

// issueIn builds a ticket carrying the given labels, with a milestone unless
// one is explicitly withheld.
func issueIn(key string, labels []string, fixVersions ...string) tracker.Issue {
	return tracker.Issue{Key: key, Labels: labels, FixVersions: fixVersions}
}

// The four outcomes of an add, in the order the operator typed them.
func TestPlanQueueAddSortsEveryOutcome(t *testing.T) {
	keys := []string{"OR-100", "OR-133", "OR-999"}
	current := map[string]tracker.Issue{
		"OR-100": issueIn("OR-100", nil, "v0.8.6"),              // to queue
		"OR-133": issueIn("OR-133", []string{qLabel}, "v0.8.6"), // already queued
		// OR-999 is absent: a key naming no ticket.
	}

	p := planQueueAdd(keys, current, qLabel, false)

	if strings.Join(p.Add, ",") != "OR-100" {
		t.Errorf("Add = %v, want only OR-100", p.Add)
	}
	if strings.Join(p.Already, ",") != "OR-133" {
		t.Errorf("Already = %v, want only OR-133", p.Already)
	}
	if strings.Join(p.Missing, ",") != "OR-999" {
		t.Errorf("Missing = %v, want only OR-999", p.Missing)
	}
	if p.writes() != 1 {
		t.Errorf("writes() = %d, want 1: only the unqueued ticket needs writing", p.writes())
	}
}

// A ticket with no fixVersion cannot be claimed once OR-221 lands, so
// labelling it would create the silent never-runs state that gate exists to
// prevent. Refused, and the reason names the missing version.
func TestPlanQueueAddRefusesATicketWithNoFixVersion(t *testing.T) {
	current := map[string]tracker.Issue{"OR-100": issueIn("OR-100", nil)}

	p := planQueueAdd([]string{"OR-100"}, current, qLabel, false)

	if len(p.Add) != 0 || p.writes() != 0 {
		t.Fatalf("a ticket with no milestone was queued anyway: %+v", p)
	}
	if len(p.Blocked) != 1 || p.Blocked[0].Key != "OR-100" {
		t.Fatalf("Blocked = %+v, want OR-100", p.Blocked)
	}
	if !strings.Contains(strings.Join(p.Blocked[0].Reasons, " "), "fixVersion") {
		t.Errorf("the refusal does not name the missing version: %v", p.Blocked[0].Reasons)
	}
}

// orion-working and orion-ci-wait are the queue's lock. Neither an add nor a
// remove may touch a ticket holding one, and the refusal names WHICH lock --
// waiting for an agent and waiting for CI are different waits.
func TestPlanQueueRefusesAClaimedTicketBothWays(t *testing.T) {
	for _, tc := range []struct {
		label string
		want  string
	}{
		{tracker.LabelWorking, tracker.LabelWorking},
		{tracker.LabelCIWait, tracker.LabelCIWait},
	} {
		t.Run(tc.label, func(t *testing.T) {
			current := map[string]tracker.Issue{
				"OR-100": issueIn("OR-100", []string{tc.label, qLabel}, "v0.8.6"),
			}

			for _, p := range []queuePlan{
				planQueueAdd([]string{"OR-100"}, current, qLabel, false),
				planQueueAdd([]string{"OR-100"}, current, qLabel, true), // --reset is no override
				planQueueRemove([]string{"OR-100"}, current, qLabel),
			} {
				if p.writes() != 0 {
					t.Fatalf("a %s ticket would be written to: %+v", tc.label, p)
				}
				if len(p.Blocked) != 1 ||
					!strings.Contains(strings.Join(p.Blocked[0].Reasons, " "), tc.want) {
					t.Errorf("the refusal does not name %s: %+v", tc.want, p.Blocked)
				}
			}
		})
	}
}

// A failed ticket needs its label cleared AND its status returned to To Do.
// Doing one and not the other leaves it unclaimable, so the plain add refuses
// and points at --reset rather than doing half of it.
func TestPlanQueueAddOnAFailedTicketNeedsReset(t *testing.T) {
	current := map[string]tracker.Issue{
		"OR-217": issueIn("OR-217", []string{tracker.LabelFailed}, "v0.8.6"),
	}

	p := planQueueAdd([]string{"OR-217"}, current, qLabel, false)
	if p.writes() != 0 {
		t.Fatalf("a failed ticket was requeued without --reset: %+v", p)
	}
	if len(p.Blocked) != 1 || !strings.Contains(strings.Join(p.Blocked[0].Reasons, " "), "--reset") {
		t.Errorf("the refusal does not point at --reset: %+v", p.Blocked)
	}

	withReset := planQueueAdd([]string{"OR-217"}, current, qLabel, true)
	if strings.Join(withReset.Reset, ",") != "OR-217" {
		t.Errorf("--reset did not plan a requeue: %+v", withReset)
	}
	if len(withReset.Add) != 0 {
		t.Errorf("a failed ticket was planned as a plain add, which would leave "+
			"%s on it: %+v", tracker.LabelFailed, withReset)
	}
}

// A failed ticket that ALSO carries no milestone has two problems, and hearing
// about one of them sends the operator back for a second run to be told about
// the other.
func TestPlanQueueAddReportsEveryReasonAtOnce(t *testing.T) {
	current := map[string]tracker.Issue{
		"OR-217": issueIn("OR-217", []string{tracker.LabelFailed}),
	}

	p := planQueueAdd([]string{"OR-217"}, current, qLabel, false)

	if len(p.Blocked) != 1 {
		t.Fatalf("Blocked = %+v, want one entry", p.Blocked)
	}
	joined := strings.Join(p.Blocked[0].Reasons, " ")
	for _, want := range []string{"fixVersion", "--reset"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusal does not mention %q: %q", want, joined)
		}
	}
}

// Remove unqueues and nothing else: a ticket that is not on the queue -- including
// one at orion-failed, which no longer carries the label -- is a no-op, not an error.
func TestPlanQueueRemove(t *testing.T) {
	keys := []string{"OR-140", "OR-141", "OR-142", "OR-999"}
	current := map[string]tracker.Issue{
		"OR-140": issueIn("OR-140", []string{qLabel}, "v0.8.6"),
		"OR-141": issueIn("OR-141", nil, "v0.8.6"),
		"OR-142": issueIn("OR-142", []string{tracker.LabelFailed}, "v0.8.6"),
	}

	p := planQueueRemove(keys, current, qLabel)

	if strings.Join(p.Remove, ",") != "OR-140" {
		t.Errorf("Remove = %v, want only the queued ticket OR-140", p.Remove)
	}
	if strings.Join(p.Already, ",") != "OR-141,OR-142" {
		t.Errorf("Already = %v, want the two tickets that were not queued", p.Already)
	}
	if strings.Join(p.Missing, ",") != "OR-999" {
		t.Errorf("Missing = %v, want OR-999", p.Missing)
	}
	// A ticket with no milestone is removable: the version gate is about
	// putting work IN the queue, and refusing to take a ticket out because of
	// it would trap exactly the tickets most likely to need removing.
	if len(p.Blocked) != 0 {
		t.Errorf("Blocked = %+v, want nothing; remove has no version gate", p.Blocked)
	}
}

// Re-running either verb over a set already in the wanted state writes nothing.
func TestPlanQueueIsIdempotent(t *testing.T) {
	queued := map[string]tracker.Issue{
		"OR-100": issueIn("OR-100", []string{qLabel}, "v0.8.6"),
	}
	if p := planQueueAdd([]string{"OR-100"}, queued, qLabel, false); p.writes() != 0 {
		t.Errorf("re-adding a queued ticket writes: %+v", p)
	}
	unqueued := map[string]tracker.Issue{"OR-100": issueIn("OR-100", nil, "v0.8.6")}
	if p := planQueueRemove([]string{"OR-100"}, unqueued, qLabel); p.writes() != 0 {
		t.Errorf("re-removing an unqueued ticket writes: %+v", p)
	}
}

// The queue label is per-project config, so the plan has to read the state
// through the configured label rather than a hardcoded ORION.
func TestPlanQueueHonoursAConfiguredLabel(t *testing.T) {
	current := map[string]tracker.Issue{
		"OR-100": issueIn("OR-100", []string{"AUTOPILOT"}, "v0.8.6"),
	}

	if p := planQueueAdd([]string{"OR-100"}, current, "AUTOPILOT", false); len(p.Already) != 1 {
		t.Errorf("a ticket carrying the configured label was not seen as queued: %+v", p)
	}
	if p := planQueueRemove([]string{"OR-100"}, current, "AUTOPILOT"); len(p.Remove) != 1 {
		t.Errorf("remove did not see the configured label: %+v", p)
	}
}
