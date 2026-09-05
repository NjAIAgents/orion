package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// A long, multi-sentence idea is still just words to ideaWords/planRoster --
// nothing here truncates or errors on length or on multiple sentences.
func TestPlanRosterHandlesLongMultiSentenceIdeas(t *testing.T) {
	idea := strings.Repeat("This is a long paragraph about the project. ", 50) +
		"It ends by asking for a database behind the web app, and a frontend for it too."
	got := rosterOf(t, idea)

	dba, ok := got[events.ActorDBA]
	if !ok || !dba.FromIdea {
		t.Fatalf("a long idea did not select the database architect on its own word: %v", got)
	}
	fe, ok := got[events.ActorFrontend]
	if !ok || !fe.FromIdea {
		t.Fatalf("a long idea did not select the frontend developer on its own word: %v", got)
	}
}

// An idea made entirely of numbers has no word any actor answers to, so it
// selects only the stage actors -- and must not error or panic doing it.
func TestPlanRosterAllNumbersIdeaSelectsOnlyStageActors(t *testing.T) {
	got := rosterOf(t, "1234 5678 90")

	stageActors := map[string]bool{}
	for _, s := range planStages {
		stageActors[s.Actor] = true
	}
	for id, a := range got {
		if a.FromIdea {
			t.Errorf("%s was selected by an all-numeric idea: %q", id, a.Signal)
		}
		if !stageActors[id] {
			t.Errorf("%s is on the roster for an all-numeric idea but runs no stage", id)
		}
	}
}

// An idea made entirely of punctuation reduces to no words at all, so -- like
// an empty idea -- only the stage actors are on the roster.
func TestPlanRosterAllPunctuationIdeaSelectsOnlyStageActors(t *testing.T) {
	got := rosterOf(t, "!!! ... ??? --- ,,, ;;;")

	stageActors := map[string]bool{}
	for _, s := range planStages {
		stageActors[s.Actor] = true
	}
	if len(got) != len(stageActors) {
		t.Fatalf("punctuation-only idea rostered %d actors, want exactly the %d stage actors: %v",
			len(got), len(stageActors), got)
	}
	for id, a := range got {
		if a.FromIdea {
			t.Errorf("%s was selected by a punctuation-only idea: %q", id, a.Signal)
		}
	}
}

// Word matching is case-insensitive: an idea shouting "DATABASE" selects the
// same actor as one saying "database".
func TestPlanRosterMatchesWordsCaseInsensitively(t *testing.T) {
	got := rosterOf(t, "We need a DataBase and a FRONTEND for this")

	dba, ok := got[events.ActorDBA]
	if !ok || !dba.FromIdea {
		t.Fatalf("mixed-case %q did not select the database architect: %v", "DataBase", got)
	}
	fe, ok := got[events.ActorFrontend]
	if !ok || !fe.FromIdea {
		t.Fatalf("all-caps %q did not select the frontend developer: %v", "FRONTEND", got)
	}
}

// planRoster's order is ConfigurableIDs' own -- sorted by identifier -- not
// merely stable across repeated calls (TestPlanRosterIsTheSameEveryRunForTheSameIdea
// already covers stability). A caller relying on this order to print a
// legible roster needs it to track the registry's own sort, not an
// incidental map- or insertion-order that happens to be repeatable.
func TestPlanRosterOrderMatchesConfigurableIDsOrder(t *testing.T) {
	t.Cleanup(actors.Reset)
	idea := "database frontend qa"
	got := planRoster(idea)

	want := map[string]bool{}
	for _, a := range got {
		want[a.ID] = true
	}

	var wantOrder []string
	for _, id := range actors.ConfigurableIDs() {
		if want[id] {
			wantOrder = append(wantOrder, id)
		}
	}

	if len(got) != len(wantOrder) {
		t.Fatalf("roster has %d actors, expected %d in ConfigurableIDs order", len(got), len(wantOrder))
	}
	for i, a := range got {
		if a.ID != wantOrder[i] {
			t.Errorf("roster position %d is %s, want %s (ConfigurableIDs order)", i, a.ID, wantOrder[i])
		}
	}
}

// planIdea prefers the description over the name, and falls back to the name
// only when the description is empty (OR-150).
func TestPlanIdeaPrefersDescriptionOverName(t *testing.T) {
	p := tracker.Project{Name: "ORPAY project", Description: "a database-backed payments API"}
	if got := planIdea(p); got != p.Description {
		t.Errorf("planIdea() = %q, want the description %q", got, p.Description)
	}
}

func TestPlanIdeaFallsBackToNameWhenDescriptionIsEmpty(t *testing.T) {
	p := tracker.Project{Name: "ORPAY project", Description: ""}
	if got := planIdea(p); got != p.Name {
		t.Errorf("planIdea() = %q, want the fallback name %q", got, p.Name)
	}
}

// printPlanRoster is driven by whatever idea text it is given -- here, one
// derived from planIdea itself -- and produces output naming the actor the
// idea's word selected.
func TestPrintPlanRosterUsesTheGivenIdeaValue(t *testing.T) {
	t.Cleanup(actors.Reset)
	p := tracker.Project{Name: "ORPAY project", Description: "Take card payments, with a database behind it."}

	var buf bytes.Buffer
	printPlanRoster(&buf, "payments-api", planIdea(p))
	got := buf.String()

	if got == "" {
		t.Fatal("printPlanRoster produced no output")
	}
	if !strings.Contains(got, actors.Display(events.ActorDBA)) {
		t.Errorf("printPlanRoster output does not name the actor selected by the idea it was given:\n%s", got)
	}
	if !strings.Contains(got, `the idea says "database"`) {
		t.Errorf("printPlanRoster output does not carry the signal for the idea it was given:\n%s", got)
	}
}
