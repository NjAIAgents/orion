package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// rosterOf indexes a roster by actor, so a test asserts on WHO is on the run
// and WHY rather than on a position in a slice.
func rosterOf(t *testing.T, idea string) map[string]planActor {
	t.Helper()
	t.Cleanup(actors.Reset)
	out := map[string]planActor{}
	for _, a := range planRoster(idea) {
		if _, dup := out[a.ID]; dup {
			t.Fatalf("%s is on the roster twice", a.ID)
		}
		out[a.ID] = a
	}
	return out
}

func TestPlanRosterCarriesEveryStageActorWhateverTheIdeaSays(t *testing.T) {
	got := rosterOf(t, "")
	for _, s := range planStages {
		a, ok := got[s.Actor]
		if !ok {
			t.Fatalf("%s runs the %s stage but is not on the roster", s.Actor, s.Stage)
		}
		if a.FromIdea {
			t.Errorf("%s is attributed to the idea, but it runs the %s stage on every run", s.Actor, s.Stage)
		}
		if !strings.Contains(a.Signal, s.Stage) {
			t.Errorf("%s signal %q does not name the %s stage it is on the run for", s.Actor, a.Signal, s.Stage)
		}
	}
}

// The ticket's own worked example: registering the database architect has to
// put it into planning, and the run has to say which word did it.
func TestPlanRosterSelectsTheDatabaseArchitectWhenTheIdeaSaysDatabase(t *testing.T) {
	got := rosterOf(t, "Take card payments from the web app, with a database behind it.")
	a, ok := got[events.ActorDBA]
	if !ok {
		t.Fatalf("the idea says database and the database architect is not on the roster: %v", got)
	}
	if !a.FromIdea {
		t.Errorf("the database architect is on the roster but not attributed to the idea")
	}
	if !strings.Contains(a.Signal, `"database"`) {
		t.Errorf("signal %q does not name the word that selected the actor", a.Signal)
	}
}

func TestPlanRosterLeavesOutAnActorTheIdeaDoesNotName(t *testing.T) {
	got := rosterOf(t, "Take card payments from the web app.")
	if a, ok := got[events.ActorDBA]; ok {
		t.Errorf("nothing in this idea is about data, but the database architect is on it: %q", a.Signal)
	}
	if a, ok := got[events.ActorFrontend]; ok {
		t.Errorf("nothing in this idea says frontend, but it is on the roster: %q", a.Signal)
	}
}

// The property the ticket names: the candidates are ConfigurableIDs(), so an
// actor registered later is reachable without editing the planning command.
// This fails the moment that glob is replaced with a literal slice.
func TestPlanRosterReachesEveryConfigurableActor(t *testing.T) {
	for _, id := range actors.ConfigurableIDs() {
		if actors.Model(id) == "" {
			continue // not an agent; see the narrator below
		}
		got := rosterOf(t, "we need the "+id+" on this one")
		if _, ok := got[id]; !ok {
			t.Errorf("%s is a configurable actor that no idea can put on a planning run", id)
		}
	}
}

func TestPlanRosterSelectsNobodyItCouldNotName(t *testing.T) {
	ids := map[string]bool{}
	for _, id := range actors.ConfigurableIDs() {
		ids[id] = true
	}
	for _, a := range planRoster("a database-backed UI for the docs team") {
		if !ids[a.ID] {
			t.Errorf("%s is on the roster but is not a configurable actor", a.ID)
		}
		if a.Signal == "" {
			t.Errorf("%s is on the roster with no signal saying why", a.ID)
		}
	}
}

// Orion is Go and is the narrator of this output, not an agent that can be
// dispatched -- so an idea that happens to say its name does not roster it.
func TestPlanRosterExcludesTheNarrator(t *testing.T) {
	if a, ok := rosterOf(t, "an orion plugin")[events.ActorOrion]; ok {
		t.Errorf("orion is on the roster as a participant: %q", a.Signal)
	}
}

// A word two actors answer to selects neither, so the roster never turns on a
// coin toss between them.
func TestPlanRosterIgnoresAWordTwoActorsShare(t *testing.T) {
	got := rosterOf(t, "we want a developer and an engineer on this")
	for _, id := range []string{events.ActorImplementer, events.ActorFrontend, events.ActorDevOps, events.ActorQA} {
		if a, ok := got[id]; ok && a.FromIdea {
			t.Errorf("%s was selected by a word it shares with another actor: %q", id, a.Signal)
		}
	}
}

// Equality, not containment -- internal/work/route.go's rule. "reindexing" is
// not "index", and reading it as one is how a signal becomes unpredictable.
func TestPlanRosterMatchesWholeWordsOnly(t *testing.T) {
	if a, ok := rosterOf(t, "reindexing the querying subsystem")[events.ActorDBA]; ok {
		t.Errorf("a substring selected the database architect: %q", a.Signal)
	}
}

func TestPlanRosterIsTheSameEveryRunForTheSameIdea(t *testing.T) {
	const idea = "a database-backed UI with docs, and a dba to review the schema"
	first := planRoster(idea)
	for i := 0; i < 20; i++ {
		got := planRoster(idea)
		if len(got) != len(first) {
			t.Fatalf("run %d rostered %d actors, the first rostered %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d differs at %d: %+v, first %+v", i, j, got[j], first[j])
			}
		}
	}
}

// Selection reads the CONFIGURED designation, so an operator who renamed one
// is selected on the words they chose rather than the ones this build shipped.
func TestPlanRosterReadsTheConfiguredDesignation(t *testing.T) {
	t.Cleanup(actors.Reset)
	if err := actors.Configure(map[string]config.Agent{
		events.ActorDBA: {Designation: "warehouse keeper"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rosterOf(t, "a warehouse of card payments")[events.ActorDBA]; !ok {
		t.Error("the renamed designation does not select its actor")
	}
}

func TestPlanRosterAnnouncementNamesEachActorAndItsSignal(t *testing.T) {
	t.Cleanup(actors.Reset)
	var buf bytes.Buffer
	printPlanRoster(&buf, "Take card payments, with a database behind it.")
	got := buf.String()

	if !strings.Contains(got, actors.Display(events.ActorDBA)) {
		t.Errorf("the roster does not name the selected actor:\n%s", got)
	}
	if !strings.Contains(got, `the idea says "database"`) {
		t.Errorf("the roster does not say which signal selected it:\n%s", got)
	}
}

// Never silent: an idea that selects nobody is a normal outcome, and printing
// nothing makes it indistinguishable from selection not having run.
func TestPlanRosterAnnouncementSaysWhenTheIdeaSelectedNobody(t *testing.T) {
	t.Cleanup(actors.Reset)
	var buf bytes.Buffer
	printPlanRoster(&buf, "Take card payments from the web app.")
	if !strings.Contains(buf.String(), "names no other actor") {
		t.Errorf("the roster is silent about having selected nobody:\n%s", buf.String())
	}
}
