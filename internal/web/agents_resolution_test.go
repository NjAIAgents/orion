package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// agents.json is hand-editable, so it can hold syntactically valid JSON that
// is not the map[string]Agent config.LoadAgents expects -- an array, a
// number, a string. Roster has to surface that as an error rather than
// panicking or silently discarding the file, because a page that swallows a
// malformed override file shows the shipped roster while claiming it is
// authoritative.
func TestRosterErrorsOnOverrideFileWithInvalidStructure(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A JSON array, not the object LoadAgents unmarshals into.
	if err := os.WriteFile(filepath.Join(home, "agents.json"), []byte(`["not", "an", "object"]`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := Roster(home)
	if err == nil {
		t.Fatalf("Roster returned no error for a malformed agents.json; got roster %+v", got)
	}
	if !strings.Contains(err.Error(), "agents.json") {
		t.Errorf("error %q does not name agents.json; an operator cannot find the file to fix", err.Error())
	}
}

// Effort is one of the five columns the mockup shows, and like Model it can
// come from either side: the shipped default when agents.json never mentions
// the actor, or the operator's override when it does. Both directions have
// to resolve to the value actually in force.
func TestEffortFieldResolvesFromShippedOrOverrideConfiguration(t *testing.T) {
	shipped := actors.Roster(nil)
	var shippedEntry actors.RosterEntry
	for _, e := range shipped {
		if e.ID == events.ActorImplementer {
			shippedEntry = e
			break
		}
	}

	home := t.TempDir()
	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	for _, e := range got {
		if e.ID != events.ActorImplementer {
			continue
		}
		if e.Effort != shippedEntry.Effort {
			t.Errorf("effort with no agents.json = %q, want shipped %q", e.Effort, shippedEntry.Effort)
		}
	}

	overrideHome := t.TempDir()
	const overrideEffort = "high"
	if err := config.SaveAgents(overrideHome, map[string]config.Agent{
		events.ActorImplementer: {Effort: overrideEffort},
	}); err != nil {
		t.Fatalf("SaveAgents: %v", err)
	}
	got, err = Roster(overrideHome)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	for _, e := range got {
		if e.ID != events.ActorImplementer {
			continue
		}
		if e.Effort != overrideEffort {
			t.Errorf("effort with agents.json override = %q, want %q", e.Effort, overrideEffort)
		}
		return
	}
	t.Fatalf("the overridden actor %q is missing from the roster", events.ActorImplementer)
}

// Name and Designation are the two identity columns -- what the row is
// called and what role it plays. Both have to resolve the same way Model and
// Effort do: shipped by default, the operator's value once agents.json sets
// one.
func TestNameAndDesignationFieldsResolveFromShippedOrOverrideConfiguration(t *testing.T) {
	shipped := actors.Roster(nil)
	var shippedEntry actors.RosterEntry
	for _, e := range shipped {
		if e.ID == events.ActorImplementer {
			shippedEntry = e
			break
		}
	}

	home := t.TempDir()
	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	for _, e := range got {
		if e.ID != events.ActorImplementer {
			continue
		}
		if e.Name != shippedEntry.Name {
			t.Errorf("name with no agents.json = %q, want shipped %q", e.Name, shippedEntry.Name)
		}
		if e.Designation != shippedEntry.Designation {
			t.Errorf("designation with no agents.json = %q, want shipped %q", e.Designation, shippedEntry.Designation)
		}
	}

	overrideHome := t.TempDir()
	const overrideDesignation = "test-designation-override"
	overrideName := "test-name-override"
	if err := config.SaveAgents(overrideHome, map[string]config.Agent{
		events.ActorImplementer: {
			Name:        &overrideName,
			Designation: overrideDesignation,
		},
	}); err != nil {
		t.Fatalf("SaveAgents: %v", err)
	}
	got, err = Roster(overrideHome)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	for _, e := range got {
		if e.ID != events.ActorImplementer {
			continue
		}
		if e.Name != overrideName {
			t.Errorf("name with agents.json override = %q, want %q", e.Name, overrideName)
		}
		if e.Designation != overrideDesignation {
			t.Errorf("designation with agents.json override = %q, want %q", e.Designation, overrideDesignation)
		}
		return
	}
	t.Fatalf("the overridden actor %q is missing from the roster", events.ActorImplementer)
}

// agents.json can name an identifier this build does not configure -- a
// typo, or an actor a NEWER build defines that this one predates. Roster
// must not error on it and must not manufacture a phantom row for it: the
// entry is silently ignored, exactly as actors.Configure ignores it.
func TestRosterIgnoresUnknownActorIDInOverrideFile(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAgents(home, map[string]config.Agent{
		"not-a-real-actor": {Model: "test-model-override"},
	}); err != nil {
		t.Fatalf("SaveAgents: %v", err)
	}

	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	want := actors.Roster(nil)
	if len(got) != len(want) {
		t.Fatalf("roster has %d entries with an unknown override present, want %d: an unknown "+
			"actor id must not add a row", len(got), len(want))
	}
	for _, e := range got {
		if e.ID == "not-a-real-actor" {
			t.Fatalf("roster contains a phantom row for an unconfigured actor id")
		}
	}
}
