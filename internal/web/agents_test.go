package web

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// A machine that has never run the wizard has no agents.json, and the page
// still has to answer the question -- with the shipped roster, whole. The
// listing has to be identical to what internal/actors resolves, field for
// field, because the moment the two differ the page is showing a run that
// will not happen.
func TestTheRosterIsTheResolvedRosterNotACopy(t *testing.T) {
	got, err := Roster(t.TempDir())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	want := actors.Roster(nil)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the page's roster is not the resolved roster:\n got %+v\nwant %+v", got, want)
	}
	if len(got) == 0 {
		t.Fatal("the roster is empty; a page that lists nothing cannot be compared to anything")
	}
}

// The override file is the other half of the answer, and the half a hardcoded
// page cannot have. An operator who changed one actor's model has to see that
// value AND see that it was theirs, while every actor absent from the file
// keeps its shipped default and is marked as shipped.
func TestAnOverrideWrittenBySettingsReachesThePageWithItsProvenance(t *testing.T) {
	home := t.TempDir()
	// Not a real model name on purpose: this asserts that the value travels
	// from the settings file to the page, not which models happen to ship.
	const override = "test-model-override"
	if err := config.SaveAgents(home, map[string]config.Agent{
		events.ActorImplementer: {Model: override},
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
		t.Fatalf("the overridden actor is missing from the roster")
	}
	if impl.Model != override {
		t.Errorf("overridden model = %q, want %q: the page is not reading agents.json",
			impl.Model, override)
	}
	if !impl.Overridden.Model {
		t.Errorf("the overridden model is not marked as overridden; a page that cannot "+
			"tell a shipped default from an operator's decision is the page this one replaces "+
			"(fields reported: %v)", impl.Overridden.Fields())
	}

	// An actor the file never mentions: shipped value, marked shipped.
	for _, e := range actors.Roster(nil) {
		if e.ID == events.ActorImplementer {
			continue
		}
		row := rows[e.ID]
		if row.Model != e.Model {
			t.Errorf("%s model = %q, want its shipped %q: overriding one actor must not "+
				"disturb another", e.ID, row.Model, e.Model)
		}
		if len(row.Overridden.Fields()) != 0 {
			t.Errorf("%s reports %v as overridden, but the file never mentions it",
				e.ID, row.Overridden.Fields())
		}
	}
}

// A role's model is declared in exactly one place. This package resolves it
// and never states it.
//
// The failure this refuses is not a wrong value today -- a copy is correct on
// the day it is written. It is the copy that stays behind when the roster
// moves, so the page reports a model the run does not use, at the moment
// somebody is reading it to find out what the run will cost. Test files are
// exempt: the models they name come out of an event stream (what actually
// ran) or set up an override, neither of which is a declaration of what a
// role runs on.
func TestNoRoleModelIsDeclaredInTheWebPackage(t *testing.T) {
	// Derived from the shipped roster, so a model this build has never heard
	// of is covered the day it is added.
	var pats []*regexp.Regexp
	var names []string
	seen := map[string]bool{}
	for _, e := range actors.Roster(nil) {
		// Deduplicated: most models are shared by several actors, and one
		// report per actor would bury the file that needs fixing.
		if e.Model == "" || seen[e.Model] {
			continue
		}
		seen[e.Model] = true
		pats = append(pats, regexp.MustCompile(`\b`+regexp.QuoteMeta(e.Model)+`\b`))
		names = append(names, e.Model)
	}
	if len(pats) == 0 {
		t.Fatal("no shipped model to look for; this test would pass on an empty roster")
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, p := range pats {
			if p.Match(b) {
				t.Errorf("%s names the model %q. A role's model belongs to internal/actors "+
					"alone; declaring one here declares it twice, and the copy is what the "+
					"page will keep showing after the roster changes.", f, names[i])
			}
		}
	}
}

// Every field the override file sets has to say so, not just the one field
// the other override test happens to touch. A page that marks Model but
// misses Name/Designation/Effort would tell an operator their own choice was
// the build's.
func TestEveryOverriddenFieldIsMarkedOverridden(t *testing.T) {
	home := t.TempDir()
	name := "test-name-override"
	if err := config.SaveAgents(home, map[string]config.Agent{
		events.ActorImplementer: {
			Name:        &name,
			Designation: "test-designation-override",
			Model:       "test-model-override",
			Effort:      "high",
		},
	}); err != nil {
		t.Fatalf("SaveAgents: %v", err)
	}

	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	for _, e := range got {
		if e.ID != events.ActorImplementer {
			continue
		}
		fields := e.Overridden.Fields()
		want := []string{"name", "designation", "model", "effort"}
		if !reflect.DeepEqual(fields, want) {
			t.Fatalf("overridden fields = %v, want %v: every field the settings file set "+
				"has to be marked as the operator's decision", fields, want)
		}
		return
	}
	t.Fatalf("the overridden actor %q is missing from the roster", events.ActorImplementer)
}

// The fields a shipped default filled in -- because the override file never
// mentioned them -- have to report as shipped, not silently pass as if
// nobody could tell. This asserts the positive across the whole roster with
// no agents.json at all, the case
// TestAnOverrideWrittenBySettingsReachesThePageWithItsProvenance only checks
// by absence for the actors it doesn't override.
func TestShippedDefaultFieldsAreMarkedShipped(t *testing.T) {
	got, err := Roster(t.TempDir())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the roster is empty; a page that lists nothing cannot be compared to anything")
	}
	for _, e := range got {
		if fields := e.Overridden.Fields(); len(fields) != 0 {
			t.Errorf("%s reports %v as overridden with no agents.json on disk; a shipped "+
				"default must not be mistaken for an operator's decision", e.ID, fields)
		}
	}
}

// One actor's override must not leak into another actor's row within the
// same Roster call. A shared base map, mutated in place while overlaying one
// entry, would make every actor after it in iteration order show the wrong
// value.
func TestOverridingOneActorLeavesOtherActorsUnchanged(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAgents(home, map[string]config.Agent{
		events.ActorImplementer: {Model: "test-model-override"},
	}); err != nil {
		t.Fatalf("SaveAgents: %v", err)
	}

	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	want := actors.Roster(nil) // the unoverridden roster: what every other row should still equal
	if len(got) != len(want) {
		t.Fatalf("roster length changed from %d to %d after one override", len(want), len(got))
	}
	for i, e := range got {
		if e.ID == events.ActorImplementer {
			continue
		}
		if !reflect.DeepEqual(e, want[i]) {
			t.Errorf("%s changed after overriding %s's model:\n got %+v\nwant %+v",
				e.ID, events.ActorImplementer, e, want[i])
		}
	}
}

// The page lists roles in a stable, predictable order rather than in map
// iteration order, which Go randomizes on every run. Identifier order is
// what makes two loads of the same page show the same layout.
func TestRosterIsOrderedByIdentifier(t *testing.T) {
	got, err := Roster(t.TempDir())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the roster is empty; ordering cannot be asserted over nothing")
	}
	ids := make([]string, len(got))
	for i, e := range got {
		ids[i] = e.ID
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("roster identifiers are not sorted: %v", ids)
	}
}

// No agents.json on disk at all -- not an empty file, not a missing
// directory handled specially, just a home nothing has ever written to --
// still has to answer with the whole shipped roster rather than an error or
// an empty page.
func TestRosterReturnsShippedDefaultsWhenNoAgentsJSONExists(t *testing.T) {
	home := t.TempDir() // never touched by config.SaveAgents: agents.json does not exist here
	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	want := actors.Roster(nil)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roster with no agents.json = %+v, want the shipped roster %+v", got, want)
	}
}

// The page has to list every configurable actor, not a subset that happens
// to be interesting. A roster missing an actor is indistinguishable, from
// the page, from an actor that was removed.
func TestRosterReturnsCompleteRosterWithAllShippedActors(t *testing.T) {
	got, err := Roster(t.TempDir())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	wantIDs := actors.ConfigurableIDs()
	if len(got) != len(wantIDs) {
		t.Fatalf("roster has %d entries, want %d: %v", len(got), len(wantIDs), wantIDs)
	}
	gotIDs := map[string]bool{}
	for _, e := range got {
		gotIDs[e.ID] = true
	}
	for _, id := range wantIDs {
		if !gotIDs[id] {
			t.Errorf("shipped actor %q is missing from the roster", id)
		}
	}
}

// Every row the page renders has to carry an id, a name field, a
// designation, a model and an effort -- the five columns the mockup shows --
// so the template never has to special-case a row shaped differently from
// the rest. ID and Designation are always non-empty; Model and Effort are
// legitimately empty for an actor that runs on the CLI's own default (see
// the Actor.Model/Effort doc comments), so this only asserts the fields
// exist and round-trip, not that every value is filled in.
func TestEachRosterEntryHasAllRequiredFields(t *testing.T) {
	got, err := Roster(t.TempDir())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the roster is empty; required fields cannot be asserted over nothing")
	}
	typ := reflect.TypeOf(actors.RosterEntry{})
	for _, name := range []string{"ID", "Name", "Designation", "Model", "Effort"} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Fatalf("actors.RosterEntry has no %s field", name)
		}
	}
	for _, e := range got {
		if e.ID == "" {
			t.Errorf("roster entry %+v has no ID", e)
		}
		if e.Designation == "" {
			t.Errorf("roster entry %q has no designation", e.ID)
		}
	}
}

// The override test elsewhere in this file checks that a changed field is
// marked as changed; this checks the value itself is the one the settings
// file wrote, across all four overridable fields at once, not just Model.
func TestOverriddenFieldValuesAppearInTheReturnedEntry(t *testing.T) {
	home := t.TempDir()
	name := "test-name-override"
	if err := config.SaveAgents(home, map[string]config.Agent{
		events.ActorImplementer: {
			Name:        &name,
			Designation: "test-designation-override",
			Model:       "test-model-override",
			Effort:      "high",
		},
	}); err != nil {
		t.Fatalf("SaveAgents: %v", err)
	}

	got, err := Roster(home)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	for _, e := range got {
		if e.ID != events.ActorImplementer {
			continue
		}
		if e.Name != name {
			t.Errorf("name = %q, want %q", e.Name, name)
		}
		if e.Designation != "test-designation-override" {
			t.Errorf("designation = %q, want %q", e.Designation, "test-designation-override")
		}
		if e.Model != "test-model-override" {
			t.Errorf("model = %q, want %q", e.Model, "test-model-override")
		}
		if e.Effort != "high" {
			t.Errorf("effort = %q, want %q", e.Effort, "high")
		}
		return
	}
	t.Fatalf("the overridden actor %q is missing from the roster", events.ActorImplementer)
}
