package web

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
