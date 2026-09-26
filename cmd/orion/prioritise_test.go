package main

// What `orion prioritise` would DO, decided before anything is written
// (OR-280). The rules here are the ones that decide whether the queue ends up
// in the order the operator asked for, or in a different one that was
// reported as theirs.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// queued is a ticket sitting in the queue at the given priority.
func queuedAt(priority string) tracker.Issue {
	return tracker.Issue{Labels: []string{"ORION"}, Priority: priority}
}

func TestPrioritisePlanKeepsTheOrderTyped(t *testing.T) {
	current := map[string]tracker.Issue{
		"OR-1": queuedAt("Medium"),
		"OR-2": queuedAt("Medium"),
		"OR-3": queuedAt("Medium"),
	}
	p := planPrioritise([]string{"OR-3", "OR-1", "OR-2"}, current, "ORION")
	if !p.ok() {
		t.Fatalf("a queued, equally-prioritised set was refused: %+v", p)
	}
	if got := strings.Join(p.Order, ","); got != "OR-3,OR-1,OR-2" {
		t.Errorf("order is %q, want OR-3,OR-1,OR-2 -- the order given IS the request", got)
	}
}

// The queue reads priority BEFORE rank, so ranking a Medium ticket above a
// High one writes an order the queue will never show. Reporting that as a
// reorder is the failure worth a whole refusal.
func TestPrioritiseRefusesASetWhosePrioritiesDiffer(t *testing.T) {
	current := map[string]tracker.Issue{
		"OR-1": queuedAt("Medium"),
		"OR-2": queuedAt("High"),
	}
	p := planPrioritise([]string{"OR-1", "OR-2"}, current, "ORION")
	if p.ok() {
		t.Fatal("an ordering that priority would override was accepted")
	}
	for _, want := range []string{"priority", "Medium", "High", "OR-1", "OR-2"} {
		if !strings.Contains(p.Refusal, want) {
			t.Errorf("the refusal never mentions %q, so nobody can act on it: %s", want, p.Refusal)
		}
	}
}

// A project with priority disabled reports "" on every ticket. That is one
// priority, not several: rank decides everything there, which is exactly when
// this command is most useful.
func TestPrioritiseAcceptsAProjectWithNoPrioritiesAtAll(t *testing.T) {
	current := map[string]tracker.Issue{"OR-1": queuedAt(""), "OR-2": queuedAt("")}
	if p := planPrioritise([]string{"OR-2", "OR-1"}, current, "ORION"); !p.ok() {
		t.Fatalf("a project with priority disabled could not reorder its queue: %+v", p)
	}
}

// Ordering a ticket that is not in the queue is an instruction with no
// effect. Accepting it silently is worse than refusing, because it reads as
// an instruction that worked.
func TestPrioritiseRefusesTicketsThatAreNotInTheQueue(t *testing.T) {
	current := map[string]tracker.Issue{
		"OR-1": queuedAt("Medium"),
		"OR-2": {Labels: []string{"orion-working"}, Priority: "Medium"},
		"OR-3": {Labels: []string{"orion-failed"}, Priority: "Medium"},
		"OR-4": {Priority: "Medium"},
	}
	p := planPrioritise([]string{"OR-1", "OR-2", "OR-3", "OR-4"}, current, "ORION")
	if p.ok() {
		t.Fatal("tickets outside the queue were given a place in it")
	}
	if len(p.Blocked) != 3 {
		t.Fatalf("blocked %d ticket(s), want 3 (working, failed, unlabelled): %+v", len(p.Blocked), p.Blocked)
	}
	reasons := ""
	for _, b := range p.Blocked {
		reasons += b.Key + ": " + strings.Join(b.Reasons, " ") + "\n"
	}
	for _, want := range []string{"orion-working", "orion-failed", "orion queue add"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("the reasons never mention %q, so the way out is not named:\n%s", want, reasons)
		}
	}
}

// A key naming no ticket cannot be skipped over: the ordering the operator
// asked for included it, so what would be written is a different ordering.
func TestPrioritiseRefusesTheWholeRunWhenAKeyNamesNoTicket(t *testing.T) {
	current := map[string]tracker.Issue{"OR-1": queuedAt("Medium"), "OR-2": queuedAt("Medium")}
	p := planPrioritise([]string{"OR-1", "OR-999", "OR-2"}, current, "ORION")
	if p.ok() {
		t.Fatal("a partial ordering was accepted; an ordering is one intention, not three")
	}
	if len(p.Missing) != 1 || p.Missing[0] != "OR-999" {
		t.Errorf("missing keys are %v, want [OR-999]", p.Missing)
	}
}
