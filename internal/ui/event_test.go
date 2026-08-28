package ui

import (
	"bytes"
	"regexp"
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
	// The ticket key first after the time: that is the thread a reader
	// follows, and the stamp is the only thing that outranks it.
	if i := strings.Index(got, "FCIA-8"); i != len("15:04:05 ") {
		t.Errorf("the ticket key must lead the line after the time, got %q", got)
	}
}

// Reading a log after the fact -- a scrollback, a nohup capture -- "when did
// this happen" and "how long was that gap" cannot be answered by correlating
// the event log by hand. Every line carries the time, including a live one
// that has no stamp of its own.
func TestEveryLineCarriesATimePrefix(t *testing.T) {
	stamped := render(Line{At: time.Date(2026, 8, 27, 15, 4, 5, 0, time.Local),
		Key: "OR-125", Verb: VerbOK, Msg: "done"})
	if !strings.HasPrefix(stamped, "15:04:05 ") {
		t.Errorf("a stamped line lost its time: %q", stamped)
	}
	// The date is deliberately absent: the event log carries full RFC3339
	// stamps for the record, and a same-day console does not need it.
	if strings.Contains(stamped, "2026") {
		t.Errorf("the console line carries a date it does not need: %q", stamped)
	}

	live := render(Line{Key: "OR-125", Verb: VerbWorking, Msg: "claimed"})
	if !regexp.MustCompile(`^\d\d:\d\d:\d\d `).MatchString(live) {
		t.Errorf("a live line was printed without a time: %q", live)
	}
}

// A status WORD is a thing to read; a glyph is a thing to notice, which is
// what scanning hundreds of lines needs. Both, never one: the word is what
// survives a terminal that cannot render the glyph.
func TestEveryStatusRendersItsOwnIcon(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	for verb, want := range map[string]string{
		VerbOK:      iconOK,
		VerbWorking: iconWorking,
		VerbWaiting: iconWaiting,
		VerbWarn:    iconBlocked,
		VerbFail:    iconFail,
		"queued":    iconPending,
		"pending":   iconPending,
		"blocked":   iconBlocked,
	} {
		got := render(Line{Key: "OR-125", Verb: verb, Msg: "x"})
		if !strings.Contains(got, want) {
			t.Errorf("%q rendered without its %q icon: %s", verb, want, got)
		}
		if !strings.Contains(got, verb) {
			t.Errorf("%q lost its status word to the icon: %s", verb, got)
		}
	}
	// A verb this build has never seen still gets a column rather than a
	// hole, so the ones after it stay aligned.
	if got := render(Line{Key: "OR-125", Verb: "something-new", Msg: "x"}); !strings.Contains(got, iconPending) {
		t.Errorf("an unknown status rendered without an icon: %s", got)
	}
}

// The five icon categories are distinct. Two states sharing a glyph would
// make the column decorative -- it exists to make a state JUMP visible.
func TestTheIconsAreDistinctPerCategory(t *testing.T) {
	seen := map[string]string{}
	for _, verb := range []string{VerbOK, VerbWorking, VerbWaiting, VerbWarn, VerbFail, "queued"} {
		g := icons[verb]
		if other, dup := seen[g.glyph]; dup {
			t.Errorf("%q and %q share the icon %q", verb, other, g.glyph)
		}
		seen[g.glyph] = verb
		if a, dup := seen[g.ascii]; dup && a != verb {
			t.Errorf("%q and %q share the ASCII icon %q", verb, a, g.ascii)
		}
		seen[g.ascii] = verb
	}
}

// A glyph on a terminal that cannot render it is mojibake, not a status. The
// same opt-outs as colour, plus a locale that says the terminal is not UTF-8.
func TestTheIconsFallBackToASCII(t *testing.T) {
	for _, env := range []struct{ name, value string }{
		{"NO_COLOR", "1"},
		{"TERM", "dumb"},
		{"LC_ALL", "C"},
	} {
		t.Run(env.name+"="+env.value, func(t *testing.T) {
			t.Setenv("LANG", "en_US.UTF-8")
			t.Setenv(env.name, env.value)
			got := render(Line{Key: "OR-125", Verb: VerbFail, Msg: "boom"})
			if strings.Contains(got, iconFail) {
				t.Errorf("a non-UTF-8 terminal was sent a glyph: %q", got)
			}
			if !strings.Contains(got, icons[VerbFail].ascii) {
				t.Errorf("the ASCII icon is missing: %q", got)
			}
			if !strings.Contains(got, VerbFail) {
				t.Errorf("the status word must survive the fallback: %q", got)
			}
		})
	}
}

// The hourglass is double-width. Padding the column by rune count would push
// every waiting line one column right of every other line -- the ragged wall
// the fixed widths exist to prevent.
func TestTheIconColumnIsPaddedInCellsNotRunes(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	for _, verb := range []string{VerbOK, VerbWorking, VerbWaiting, VerbWarn, VerbFail} {
		if got := cells(iconFor(verb)); got != iconWidth {
			t.Errorf("%q occupies %d cells, want %d", verb, got, iconWidth)
		}
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
