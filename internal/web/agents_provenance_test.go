package web

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// An operator rarely overrides every field of an actor at once -- setting
// just the model, say, and leaving the name and designation as shipped. The
// page has to mark exactly the field that changed and nothing else, or an
// operator who only touched the model would see their designation reported
// as their own decision too.
func TestPartialOverrideReflectsProvenancePerField(t *testing.T) {
	home := t.TempDir()
	const overrideModel = "test-partial-model-override"
	if err := config.SaveAgents(home, map[string]config.Agent{
		events.ActorImplementer: {Model: overrideModel},
	}); err != nil {
		t.Fatalf("SaveAgents: %v", err)
	}

	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}

	var shipped actors.RosterEntry
	for _, e := range actors.Roster(nil) {
		if e.ID == events.ActorImplementer {
			shipped = e
			break
		}
	}

	for _, e := range got {
		if e.ID != events.ActorImplementer {
			continue
		}
		if e.Model != overrideModel {
			t.Errorf("Model = %q, want overridden %q", e.Model, overrideModel)
		}
		if e.Name != shipped.Name {
			t.Errorf("Name = %q, want shipped %q: an untouched field must keep its default", e.Name, shipped.Name)
		}
		if e.Designation != shipped.Designation {
			t.Errorf("Designation = %q, want shipped %q: an untouched field must keep its default",
				e.Designation, shipped.Designation)
		}
		want := []string{"model"}
		if fields := e.Overridden.Fields(); !reflect.DeepEqual(fields, want) {
			t.Fatalf("overridden fields = %v, want %v: only the field actually set should be "+
				"marked as the operator's decision", fields, want)
		}
		return
	}
	t.Fatalf("actor %q is missing from the roster", events.ActorImplementer)
}

// Two actors overridden in the same agents.json file must not blend their
// provenance: each row reports only the fields ITS OWN entry in the file
// set, independent of what the other actor's entry set.
func TestMultipleActorsOverriddenWithIndependentProvenance(t *testing.T) {
	home := t.TempDir()
	name := "test-frontend-name-override"
	if err := config.SaveAgents(home, map[string]config.Agent{
		events.ActorImplementer: {Model: "test-implementer-model-override"},
		events.ActorFrontend: {
			Name:   &name,
			Effort: "high",
		},
	}); err != nil {
		t.Fatalf("SaveAgents: %v", err)
	}

	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	rows := map[string]actors.RosterEntry{}
	for _, e := range got {
		rows[e.ID] = e
	}

	impl, ok := rows[events.ActorImplementer]
	if !ok {
		t.Fatalf("actor %q is missing from the roster", events.ActorImplementer)
	}
	if want := []string{"model"}; !reflect.DeepEqual(impl.Overridden.Fields(), want) {
		t.Errorf("%s overridden fields = %v, want %v", events.ActorImplementer, impl.Overridden.Fields(), want)
	}

	fe, ok := rows[events.ActorFrontend]
	if !ok {
		t.Fatalf("actor %q is missing from the roster", events.ActorFrontend)
	}
	if fe.Name != name {
		t.Errorf("%s Name = %q, want %q", events.ActorFrontend, fe.Name, name)
	}
	want := []string{"name", "effort"}
	if !reflect.DeepEqual(fe.Overridden.Fields(), want) {
		t.Errorf("%s overridden fields = %v, want %v: the frontend actor's own override must not "+
			"pick up the model field the implementer's entry set", events.ActorFrontend, fe.Overridden.Fields(), want)
	}
	if fe.Overridden.Model {
		t.Errorf("%s reports its model as overridden, but its own entry never set one -- "+
			"the implementer's override leaked across actors", events.ActorFrontend)
	}
}

// Roster is documented as returning actors.RosterEntry rather than a
// web-shaped copy of it. This pins that down as a compile-time and run-time
// fact rather than something only the doc comment claims.
func TestRosterReturnsActorsRosterEntryType(t *testing.T) {
	got, err := Roster(t.TempDir())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the roster is empty; the type cannot be inspected over nothing")
	}

	// The static type is pinned by this assignment: it would fail to compile
	// if Roster returned anything other than []actors.RosterEntry.
	var pinned []actors.RosterEntry = got

	wantType := reflect.TypeOf(actors.RosterEntry{})
	gotType := reflect.TypeOf(pinned[0])
	if gotType != wantType {
		t.Fatalf("Roster element type = %s, want %s: a web-specific copy of the actors type "+
			"would drift the day actors.RosterEntry gains a field", gotType, wantType)
	}
}

// LoadAgents' error -- an agents.json that exists but is not valid JSON --
// has to reach the caller through Roster rather than being swallowed. A
// caller that cannot tell "no overrides" from "the override file is
// corrupt" would render an empty-but-wrong roster instead of failing loudly.
func TestConfigLoadAgentsErrorIsPropagatedAsRosterError(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(config.AgentsPath(home), []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Confirm the premise directly against config.LoadAgents before trusting
	// Roster's pass-through of it.
	if _, err := config.LoadAgents(home); err == nil {
		t.Fatal("config.LoadAgents did not error on invalid JSON; this test's premise is wrong")
	}

	got, err := Roster(home)
	if err == nil {
		t.Fatal("Roster did not return an error for a corrupt agents.json; the failure was swallowed")
	}
	if got != nil {
		t.Errorf("Roster returned a non-nil roster (%+v) alongside an error", got)
	}
}

// Sanity check that AgentsPath actually points at the file this test writes
// to, so the corruption above lands where LoadAgents reads it.
func TestAgentsPathIsInsideHome(t *testing.T) {
	home := t.TempDir()
	if got := config.AgentsPath(home); filepath.Dir(got) != home {
		t.Fatalf("AgentsPath(%q) = %q, want a file directly inside home", home, got)
	}
}

// A valid agents.json that exists but declares no entries -- `{}` -- is a
// different case from the file being absent, and from it being corrupt. It
// still has to answer with the whole shipped roster: an operator who ran the
// wizard and changed nothing has an empty override file on disk, not a
// missing one.
func TestRosterHandlesEmptyAgentsJSONFile(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(config.AgentsPath(home), []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	want := actors.Roster(nil)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roster with an empty agents.json = %+v, want the shipped roster %+v", got, want)
	}
}

// home itself need not exist -- not just agents.json within it. A machine
// where Orion has never written anything under its home directory still has
// to render the shipped roster rather than erroring out of an os.ReadFile
// that fails on a missing parent directory too.
func TestRosterDoesNotCrashOnNonExistentHomeDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "never-created")

	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	want := actors.Roster(nil)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roster with a non-existent home = %+v, want the shipped roster %+v", got, want)
	}
}
