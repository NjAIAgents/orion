package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

func render(l Line) string {
	var b bytes.Buffer
	Print(&b, l)
	return b.String()
}

// Every line says who acted, by name AND job title. Naming on first mention
// only breaks the moment a line is scrolled back to, grepped, or forwarded
// to somebody who did not see the start of the run.
func TestEveryLineCarriesKeyActorModelAndMessage(t *testing.T) {
	got := render(Line{Key: "FCIA-8", Actor: events.ActorImplementer,
		Model: "claude-opus-4-1-20250805", Verb: VerbOK, Msg: "3 commits on orion/fcia-8"})

	for _, want := range []string{
		"FCIA-8", actors.Display(events.ActorImplementer), "opus", "3 commits on orion/fcia-8",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("line is missing %q:\n%s", want, got)
		}
	}
	// The ticket key first: that is the thread a reader follows.
	if i := strings.Index(got, "FCIA-8"); i != 0 {
		t.Errorf("the ticket key must lead the line, got %q", got)
	}
}

// Orion and CI have no name, and a dangling separator would read as a
// missing column rather than as an actor without one.
func TestANamelessActorRendersWithoutATrailingSeparator(t *testing.T) {
	got := render(Line{Key: "FCIA-8", Actor: events.ActorOrion, Verb: VerbOK, Msg: "opened PR #3"})
	if strings.Contains(got, actors.Separator) {
		t.Errorf("orion has no name and must render without a separator:\n%s", got)
	}
	if !strings.Contains(got, noModel) {
		t.Errorf("an actor that runs no model must say so rather than leave a hole:\n%s", got)
	}
}

// The model column exists because a ticket is worked by three models and the
// output used to present them as one voice. An event that did not record one
// falls back to the actor's own.
func TestTheModelFallsBackToTheActorsOwn(t *testing.T) {
	got := render(Line{Key: "FCIA-8", Actor: events.ActorRouter, Verb: VerbOK, Msg: "routed"})
	if !strings.Contains(got, actors.Model(events.ActorRouter)) {
		t.Errorf("want the router's own model in:\n%s", got)
	}
}

// A line whose actor has been cut has lost the thing this layout exists to
// add. A message usually survives clipping because its first words carry the
// sense, so the message is what gives way.
func TestTheMessageTruncatesAndTheMetadataNever(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	long := strings.Repeat("word ", 200)
	got := render(Line{Key: "FCIA-8", Actor: events.ActorImplementer, Verb: VerbOK, Msg: long})

	if len([]rune(strings.TrimRight(got, "\n"))) > 80 {
		t.Errorf("line was not clipped to the terminal width:\n%s", got)
	}
	for _, want := range []string{"FCIA-8", actors.Display(events.ActorImplementer), "opus"} {
		if !strings.Contains(got, want) {
			t.Errorf("metadata %q was truncated; only the message may be:\n%s", want, got)
		}
	}
}

// Colour is an accelerator, never the identifier. Under NO_COLOR or a pipe
// every line has to remain unambiguous on its words alone.
func TestUnderNoColorEveryLineIsStillUnambiguous(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var b bytes.Buffer // not a terminal either
	Print(&b, Line{Key: "FCIA-8", Actor: events.ActorImplementer, Verb: VerbFail, Msg: "boom"})
	Banner(&b, "FCIA-8", "Build the thing", events.ActorImplementer, "opus", "orion/fcia-8")
	got := b.String()

	if strings.Contains(got, "\x1b[") {
		t.Fatalf("escape codes survived NO_COLOR:\n%q", got)
	}
	for _, want := range []string{"FCIA-8", VerbFail, "boom", "Build the thing", "orion/fcia-8"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q must still be readable without colour:\n%s", want, got)
		}
	}
}

// Red, green and yellow mean failure, success and warning. A ticket assigned
// one of them would read as broken -- or as finished -- on every line it
// emits, which is worse than no colour at all.
func TestTicketColoursNeverCollideWithMeaning(t *testing.T) {
	for _, c := range ticketPalette {
		if c == red || c == green || c == yellow {
			t.Fatalf("the ticket rotation contains a semantic colour: %q", c)
		}
	}
}

// A colour that changed between ticks would be worse than none: it would say
// "a different ticket".
func TestATicketKeepsItsColourAndTwoTicketsDiffer(t *testing.T) {
	resetTicketColors()
	a := ticketColor("FCIA-8")
	b := ticketColor("FCIA-10")
	if a == b {
		t.Errorf("two tickets in flight got the same colour; they cannot be told apart")
	}
	if again := ticketColor("FCIA-8"); again != a {
		t.Errorf("a ticket's colour changed between ticks: %q then %q", a, again)
	}
}

// Ticket colour and actor colour are different axes and cannot share one
// column: a line cannot be both.
func TestTicketColourAndActorColourAreDifferentColumns(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1") // colour without needing a terminal
	resetTicketColors()
	var b bytes.Buffer

	one := Render(&b, Line{Key: "FCIA-8", Actor: events.ActorImplementer, Verb: VerbOK, Msg: "x"})
	two := Render(&b, Line{Key: "FCIA-10", Actor: events.ActorImplementer, Verb: VerbOK, Msg: "x"})

	// Same actor, different ticket: the actor colour is common to both and
	// the ticket colour is not. If one column carried both axes, changing
	// the ticket would change the actor colour with it.
	actor := actorColor(events.ActorImplementer)
	if !strings.Contains(one, actor) || !strings.Contains(two, actor) {
		t.Fatalf("the actor column is not coloured by actor:\n%s%s", one, two)
	}
	if ticketColor("FCIA-8") == ticketColor("FCIA-10") {
		t.Fatal("two tickets in flight share a colour")
	}
	if !strings.Contains(one, ticketColor("FCIA-8")) {
		t.Errorf("the key column is not coloured by ticket:\n%s", one)
	}
}

// The banner is what a reader scrolling back to the start of a ticket lands
// on, so it has to answer every question at once.
func TestTheBannerCarriesEverythingAboutTheTicket(t *testing.T) {
	var b bytes.Buffer
	Banner(&b, "FCIA-8", "Build golden evaluation suite", events.ActorImplementer, "", "orion/fcia-8")
	got := b.String()
	for _, want := range []string{
		"FCIA-8", "Build golden evaluation suite",
		actors.Display(events.ActorImplementer), "opus", "orion/fcia-8",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the banner is missing %q:\n%s", want, got)
		}
	}
}

// One word per category. "created" used to mean commits, a pull request
// description and a pull request itself, which is three things a reader
// cannot filter apart.
func TestTheVerbVocabularyIsOneWordPerCategory(t *testing.T) {
	allowed := map[string]bool{
		VerbOK: true, VerbWorking: true, VerbWaiting: true, VerbWarn: true, VerbFail: true,
	}
	for _, kind := range []string{
		events.KindClaimed, events.KindBranch, events.KindRunStart, events.KindRunEnd,
		events.KindAsk, events.KindAnswer, events.KindRefuse, events.KindEscalate,
		events.KindDecision, events.KindCommit, events.KindPush, events.KindPR,
		events.KindCI, events.KindMerge, events.KindRefresh, events.KindBlocked,
		events.KindFailed, events.KindBudget, events.KindTool, events.KindSay,
		events.KindNote, "something-a-later-build-emits",
	} {
		if v := VerbFor(kind); !allowed[v] {
			t.Errorf("kind %q renders as %q, which is outside the vocabulary", kind, v)
		}
	}
}

// A log written before the roster existed carries only the stored
// identifier, and must render with the names in force today.
func TestAnOldLogRendersWithCurrentNames(t *testing.T) {
	old := events.Event{
		At: time.Now(), Kind: events.KindCommit,
		Actor: events.ActorImplementer, Key: "FCIA-1", Msg: "2 commit(s)",
	}
	got := render(Line{At: old.At, Key: old.Key, Actor: old.Actor,
		Verb: VerbFor(old.Kind), Msg: old.Msg})
	if !strings.Contains(got, actors.Display(events.ActorImplementer)) {
		t.Errorf("history must render with the current roster:\n%s", got)
	}
}

func resetTicketColors() {
	colorMu.Lock()
	defer colorMu.Unlock()
	ticketColors = map[string]string{}
	ticketNext = 0
}
