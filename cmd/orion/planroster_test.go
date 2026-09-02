package main

import (
	"bytes"
	"regexp"
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

// An empty idea has no words to match, so the only actors on the run are the
// stage actors -- nothing is attributed to the idea, and nothing else is on
// the roster at all.
func TestPlanRosterOnAnEmptyIdeaHasOnlyTheStageActors(t *testing.T) {
	got := rosterOf(t, "")

	stageActors := map[string]bool{}
	for _, s := range planStages {
		stageActors[s.Actor] = true
	}
	if len(got) != len(stageActors) {
		t.Fatalf("empty idea rostered %d actors, want exactly the %d stage actors: %v", len(got), len(stageActors), got)
	}
	for id, a := range got {
		if a.FromIdea {
			t.Errorf("%s is attributed to the idea, but the idea was empty: %q", id, a.Signal)
		}
		if !stageActors[id] {
			t.Errorf("%s is on the roster for an empty idea but runs no stage", id)
		}
	}
}

// A single word is enough to match the actor it belongs to, with no other
// words in the idea needed to disambiguate it.
func TestPlanRosterSingleWordIdeaMatchesItsActor(t *testing.T) {
	got := rosterOf(t, "database")
	a, ok := got[events.ActorDBA]
	if !ok {
		t.Fatalf("the single word %q did not select the database architect: %v", "database", got)
	}
	if !a.FromIdea {
		t.Errorf("the database architect is on the roster but not attributed to the idea")
	}
	if !strings.Contains(a.Signal, `"database"`) {
		t.Errorf("signal %q does not name the word that selected the actor", a.Signal)
	}
}

// A multi-word idea can name several actors at once, each on its own word,
// with neither word bleeding into the other actor's match.
func TestPlanRosterMultiWordIdeaSelectsEachActorItsWordNames(t *testing.T) {
	got := rosterOf(t, "database frontend")

	dba, ok := got[events.ActorDBA]
	if !ok || !dba.FromIdea {
		t.Fatalf(`"database" did not select the database architect: %v`, got)
	}
	if !strings.Contains(dba.Signal, `"database"`) {
		t.Errorf("database architect signal %q does not name its word", dba.Signal)
	}

	fe, ok := got[events.ActorFrontend]
	if !ok || !fe.FromIdea {
		t.Fatalf(`"frontend" did not select the frontend developer: %v`, got)
	}
	if !strings.Contains(fe.Signal, `"frontend"`) {
		t.Errorf("frontend developer signal %q does not name its word", fe.Signal)
	}
}

// log-triage is a hyphenated actor identifier. The idea can name it as a
// standalone word, or as one component of a longer hyphenated word -- both
// forms have to reach the same actor.
func TestPlanRosterMatchesHyphenatedIdentifierWholeAndAsComponent(t *testing.T) {
	whole, ok := rosterOf(t, "run log-triage on this")[events.ActorLogTriage]
	if !ok || !whole.FromIdea {
		t.Fatalf("the identifier log-triage, named whole, did not select its actor: %v", whole)
	}

	component, ok := rosterOf(t, "a log-triage-followup for the ci failure")[events.ActorLogTriage]
	if !ok || !component.FromIdea {
		t.Fatalf("log-triage, named as a component of a longer hyphenated word, did not select its actor: %v", component)
	}
}

// The architect runs two stages (spec and plan), so its signal has to name
// both -- not just whichever one a test happens to check.
func TestPlanRosterStageActorSignalNamesEveryStageItRuns(t *testing.T) {
	got := rosterOf(t, "")
	a, ok := got[events.ActorArchitect]
	if !ok {
		t.Fatalf("the architect runs stages but is not on the roster: %v", got)
	}
	if !strings.Contains(a.Signal, "spec") {
		t.Errorf("signal %q does not name the spec stage the architect runs", a.Signal)
	}
	if !strings.Contains(a.Signal, "plan") {
		t.Errorf("signal %q does not name the plan stage the architect runs", a.Signal)
	}
}

// Each idea-selected actor's own signal names the specific word that put it
// on the run -- not some other selected actor's word.
func TestPlanRosterEachIdeaSelectedActorSignalNamesItsOwnWord(t *testing.T) {
	got := rosterOf(t, "we need a database architect and a frontend developer for this")

	dba, ok := got[events.ActorDBA]
	if !ok || !dba.FromIdea || !strings.Contains(dba.Signal, `"database"`) {
		t.Fatalf("the database architect's signal does not name \"database\": %+v", dba)
	}
	fe, ok := got[events.ActorFrontend]
	if !ok || !fe.FromIdea || !strings.Contains(fe.Signal, `"frontend"`) {
		t.Fatalf("the frontend developer's signal does not name \"frontend\": %+v", fe)
	}
	if strings.Contains(dba.Signal, `"frontend"`) || strings.Contains(fe.Signal, `"database"`) {
		t.Errorf("a signal names the other actor's word: dba=%q frontend=%q", dba.Signal, fe.Signal)
	}
}

// When the idea names nobody beyond the stage actors, the announcement has to
// say so explicitly rather than just printing a roster that is silently
// shorter than it could have been.
func TestPlanRosterAnnouncementExplicitlyStatesTheIdeaSelectedNoOne(t *testing.T) {
	t.Cleanup(actors.Reset)
	var buf bytes.Buffer
	printPlanRoster(&buf, "a plain idea with nothing any actor answers to")
	got := buf.String()
	if !strings.Contains(got, "names no other actor") {
		t.Errorf("announcement does not explicitly state the idea selected nobody:\n%s", got)
	}
	for _, id := range []string{events.ActorDBA, events.ActorFrontend, events.ActorLogTriage} {
		if strings.Contains(got, `the idea says`) && strings.Contains(got, actors.Display(id)) {
			t.Errorf("%s appears as idea-selected even though nothing should have matched:\n%s", id, got)
		}
	}
}

// The roster's own §R legibility rule: actor names and signals line up in
// columns, so a reader scans down one column rather than re-parsing every
// line -- checked here on two actors with different-length display names.
func TestPlanRosterAnnouncementColumnsAreAligned(t *testing.T) {
	t.Cleanup(actors.Reset)
	var buf bytes.Buffer
	printPlanRoster(&buf, "we need a database architect and a qa engineer for this")

	var chosen []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "     ") {
			chosen = append(chosen, line)
		}
	}
	if len(chosen) < 2 {
		t.Fatalf("need at least two selected-actor lines to check column alignment, got %d:\n%s", len(chosen), buf.String())
	}

	gap := regexp.MustCompile(`\s{2,}`)
	col := -1
	for _, line := range chosen {
		loc := gap.FindStringIndex(line)
		if loc == nil {
			t.Fatalf("line has no column gap to align on: %q", line)
		}
		if col == -1 {
			col = loc[0]
			continue
		}
		if loc[0] != col {
			t.Errorf("columns are not aligned: %q starts its next column at %d, want %d", line, loc[0], col)
		}
	}
}

// A designation with punctuation around its words -- parentheses, commas, a
// trailing period -- still yields clean words a plain idea can match; the
// punctuation must not fuse into the word and silently break the match.
func TestPlanRosterDesignationWithSpecialCharactersStillExtractsWords(t *testing.T) {
	t.Cleanup(actors.Reset)
	if err := actors.Configure(map[string]config.Agent{
		events.ActorQA: {Designation: "payments, (specialist)."},
	}); err != nil {
		t.Fatal(err)
	}

	a, ok := rosterOf(t, "we need a payments specialist on this")[events.ActorQA]
	if !ok || !a.FromIdea {
		t.Fatalf("a designation with surrounding punctuation did not extract a matchable word: %v", a)
	}
}

// A designation that is only whitespace contributes no words at all -- it
// must not make the actor selectable, and it must not do anything worse
// (panic, a stray empty-string word matching everything).
func TestPlanRosterWhitespaceOnlyDesignationDoesNotMakeActorSelectable(t *testing.T) {
	t.Cleanup(actors.Reset)
	if err := actors.Configure(map[string]config.Agent{
		events.ActorQA: {Designation: "   "},
	}); err != nil {
		t.Fatal(err)
	}

	got := rosterOf(t, "we need help with payments and reviews and everything else")
	if a, ok := got[events.ActorQA]; ok && a.FromIdea {
		t.Errorf("a whitespace-only designation still selected qa via some word: %q", a.Signal)
	}

	// The identifier itself is still a word, whitespace designation or not.
	got2 := rosterOf(t, "run qa on this")
	if a, ok := got2[events.ActorQA]; !ok || !a.FromIdea {
		t.Errorf("qa is not selectable by its own identifier when its designation is whitespace-only: %v", got2)
	}
}
