package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

// One renderer, for `orion watch`, `orion work` and `orion logs`.
//
// They all describe the same thing -- what Orion and its agents did, in
// order -- and they used to print it three different ways. Two renderers
// over one event stream drift, and the drift shows up as the same run
// reading differently depending on which command you happened to use.
//
// The line is columnar because the reader is following a THREAD, not a
// stream:
//
//	FCIA-8   ok       <developer>  · backend developer   opus     3 commits on orion/fcia-8
//	FCIA-8   ok       <pr writer>  · PR writer           sonnet   wrote the description
//	FCIA-8   ok       orion                              -        opened PR #3, awaiting CI
//	FCIA-10  working  orion                              -        claimed (1 queued)
//
// The names are left as placeholders even in this comment: they are
// configurable, so writing one here would date the moment somebody changed
// it. internal/actors holds the only copy.
//
// Ticket key first, because that is the thread. Then the status word, then
// who acted and on what model, then their message.
//
// THREE COLOUR AXES, THREE COLUMNS, deliberately. The key is coloured by
// ticket so two interleaved tickets separate by eye; the status word is
// coloured by outcome, which is the existing palette and the reason colour
// was added here at all; the actor is coloured by actor. A single column
// cannot carry two of those, and merging any two of them means one is lost.
// The message is never coloured: its text belongs to the agent that said it.

// The status vocabulary. One word per CATEGORY, not one per sentence.
//
// It used to be one per sentence: "created" meant commits, a pull request
// description and a pull request itself, while created/ok/bound/asked/
// resolved/claimed all looked identical and said nothing a reader could
// filter on. What a scanning reader needs from this column is the answer to
// "do I have to do something", which has five values.
const (
	VerbOK      = "ok"      // it worked
	VerbWorking = "working" // in flight, money is being spent
	VerbWaiting = "waiting" // in flight, waiting on a machine or a person
	VerbWarn    = "warning" // worth a look, nothing is broken
	VerbFail    = "failed"  // broken, needs a person
)

// VerbFor maps an event kind to its category word.
//
// Deliberately many-to-few. The kinds are a closed vocabulary for FILTERING
// -- a reader querying the JSONL wants "pr" to mean a pull request -- and
// this column is for SCANNING, where twenty distinct words are twenty things
// to read rather than one thing to notice.
func VerbFor(kind string) string {
	switch kind {
	case events.KindFailed, events.KindBlocked:
		return VerbFail
	case events.KindEscalate, events.KindRefuse, events.KindBudget:
		return VerbWarn
	case events.KindCI:
		return VerbWaiting
	case events.KindClaimed, events.KindBranch, events.KindRunStart,
		events.KindAsk, events.KindTool, events.KindSay:
		return VerbWorking
	case events.KindNote:
		return VerbOK
	}
	// answer, decision, commit, push, pr, merge, refresh, run-end, usage:
	// something happened and it worked.
	//
	// KindStage is here too, and on purpose. A handoff asks nothing of the
	// operator, so its category is `ok` -- spending a sixth verb on it would
	// re-open the decision this comment records. What a boundary needs is a
	// different LAYOUT, not a different word: see stage.go.
	return VerbOK
}

// The icon column: a glyph saying the same thing as the status word.
//
// The word is what the line MEANS and the glyph is what a reader scanning
// hundreds of lines actually sees, which is why both are here and why the
// glyph is never the only carrier -- under an ASCII terminal it degrades to
// a punctuation mark and the word is unchanged.
//
// Categories, matching the five verbs plus the pending state a queued but
// unclaimed ticket sits in.
const (
	iconPending = "○" // queued, or a verb this build does not recognise
	iconWorking = "◐" // in flight, money is being spent
	iconOK      = "✓"
	iconFail    = "✗"
	iconWaiting = "⏳" // a machine or a person is deciding
	iconBlocked = "⚠"
)

type icon struct{ glyph, ascii string }

var icons = map[string]icon{
	VerbOK:      {iconOK, "+"},
	VerbWorking: {iconWorking, ">"},
	VerbWaiting: {iconWaiting, "~"},
	VerbWarn:    {iconBlocked, "!"},
	VerbFail:    {iconFail, "x"},
	// Not verbs of this renderer, but the words a reader uses for the same
	// states -- and the vocabulary `orion queue` prints.
	"queued":  {iconPending, "."},
	"pending": {iconPending, "."},
	"blocked": {iconBlocked, "!"},
	"running": {iconWorking, ">"},
	"ci-wait": {iconWaiting, "~"},
}

// iconWidth is the icon column in terminal CELLS rather than runes.
//
// The hourglass is double-width. Padding it by rune count would push every
// waiting line one column right of every other line, which is precisely the
// ragged wall the column widths exist to prevent.
const iconWidth = 3

// iconFor returns the icon column for a status word, already padded.
func iconFor(verb string) string {
	g, ok := icons[strings.ToLower(strings.TrimSpace(verb))]
	if !ok {
		g = icons["pending"]
	}
	s := g.ascii
	if glyphs() {
		s = g.glyph
	}
	if d := iconWidth - cells(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// cells is the terminal width of an icon. A table rather than a rune-range
// guess: the set is six glyphs and only one of them is wide.
func cells(s string) int {
	n := 0
	for _, r := range s {
		if string(r) == iconWaiting {
			n += 2
			continue
		}
		n++
	}
	return n
}

// Column widths. Metadata NEVER truncates and the message does: a line whose
// actor has been cut has lost the thing this column layout exists to add,
// and a message usually survives clipping because its first words carry the
// sense. A name longer than the column therefore pushes the message right
// rather than being shortened.
const (
	keyWidth   = 8
	verbColumn = 8
	actorWidth = 26
	modelWidth = 8
)

// noModel is what an actor that runs no model shows. A blank would read as
// missing data; this reads as "not applicable", which is the truth for Orion
// itself and for CI.
const noModel = "-"

// Line is one rendered event.
type Line struct {
	// At, when non-zero, prefixes the line with a local time. `orion logs`
	// is reading history and needs it; a live watch is the present tense.
	At    time.Time
	Key   string // FCIA-8
	Actor string // the STABLE identifier; the display name comes from the registry
	Model string // as recorded; empty falls back to the actor's default
	Verb  string // one of the five above
	Msg   string
	// Trace marks a line as part of the agent's tool-call transcript --
	// which file it read, which command it ran. It is printed only under
	// --verbose, because it is already complete in the event log and the run
	// log, and on the console it buries the lines a person has to act on
	// (OR-217, and console.go for the whole argument).
	//
	// A FIELD rather than a verb or a kind, deliberately. The verb column
	// answers "do I have to do something" and every one of its five values
	// can be either signal or transcript; "is this worth a person's screen"
	// is a second axis, and folding it into the first would cost the column
	// the meaning OR-163 gave it.
	Trace bool
}

// Render returns one formatted line, without a newline, identity columns and
// all. What `orion logs` renders a single event with, and what the printer
// calls for the first line of any new identity.
func Render(w io.Writer, l Line) string { return renderLine(w, l, true) }

// renderLine is Render with the identity columns optional. Blanked, never
// dropped: the columns still occupy their width, so a run of lines from one
// actor stays a column layout rather than becoming a ragged left edge.
func renderLine(w io.Writer, l Line, identity bool) string {
	var b strings.Builder
	at := l.At
	if at.IsZero() {
		// A live line carries no stamp of its own, and still needs one on
		// the page: read back afterwards -- a scrollback, a nohup capture --
		// "when did this happen" and "how long was that gap" are otherwise
		// unanswerable without correlating the event log by hand (OR-125).
		//
		// Time only. The date is in the event log's RFC3339 stamps for the
		// record, and a console being read the same day does not need it.
		at = time.Now()
	}
	b.WriteString(Dim(w, at.Local().Format("15:04:05")) + " ")
	// Coloured by ticket, so two tickets in flight separate at a glance --
	// and the key itself is still in the line, so nothing depends on colour.
	b.WriteString(paint(w, ticketColor(l.Key), pad(l.Key, keyWidth)) + " ")
	// Icon and word share the status colour: they are one column saying one
	// thing twice, and colouring them apart would read as two facts.
	b.WriteString(paint(w, statusColor(l.Verb), iconFor(l.Verb)+pad(l.Verb, verbColumn)) + " ")

	if identity {
		who := ""
		if l.Actor != "" {
			who = actors.Display(l.Actor)
		}
		b.WriteString(paint(w, actorColor(l.Actor), pad(who, actorWidth)) + " ")

		model := l.Model
		if model == "" {
			model = actors.Model(l.Actor)
		}
		if model == "" {
			model = noModel
		}
		b.WriteString(Dim(w, pad(shortModel(model), modelWidth)) + " ")
	} else {
		// Unchanged from the line above, so the columns are held open and
		// left blank. Never coloured: an escape sequence around whitespace
		// is invisible on a terminal and line noise in a piped log.
		b.WriteString(strings.Repeat(" ", actorWidth+1+modelWidth+1))
	}

	// The message is clipped, and only the message. Width comes from
	// COLUMNS when the environment sets it; unknown width means no clipping,
	// because guessing a width and cutting a line to it is worse than a line
	// the terminal wraps itself.
	msg := strings.TrimRight(l.Msg, "\n")
	msg = strings.ReplaceAll(msg, "\n", " ")
	if cols := columns(); cols > 0 {
		if room := cols - metaWidth(l, identity); room > 12 && utf8.RuneCountInString(msg) > room {
			msg = string([]rune(msg)[:room-1]) + "…"
		}
	}
	b.WriteString(msg)
	return b.String()
}

// Print writes one line, subject to the console volume rules in console.go:
// a Trace line only under --verbose, identity columns only when they change,
// and a run of identical lines collapsed to one plus a count.
func Print(w io.Writer, l Line) { printLine(w, l) }

// Say is the terminal shorthand: who did what, on which ticket.
func Say(w io.Writer, key, actor, verb, format string, a ...any) {
	Print(w, Line{Key: key, Actor: actor, Verb: verb, Msg: fmt.Sprintf(format, a...)})
}

// SayModel is Say for a line produced by a model other than the actor's
// default -- a resumed run on a different model, say.
func SayModel(w io.Writer, key, actor, model, verb, format string, a ...any) {
	Print(w, Line{Key: key, Actor: actor, Model: model, Verb: verb,
		Msg: fmt.Sprintf(format, a...)})
}

// Trace is Say for a line of the agent's tool-call transcript: printed only
// under --verbose, recorded in the event log regardless. See console.go.
func Trace(w io.Writer, key, actor, model, verb, format string, a ...any) {
	Print(w, Line{Key: key, Actor: actor, Model: model, Verb: verb, Trace: true,
		Msg: fmt.Sprintf(format, a...)})
}

// Banner marks the start of a ticket.
//
// Printed on CLAIM only -- not on resume, not per tick -- or it stops
// meaning "something new started". It carries the key, the summary, who is
// working it, on what, and the branch, so that scrolling back to the top of
// a ticket answers every question at once instead of sending the reader
// hunting through the lines that follow.
func Banner(w io.Writer, key, summary, actor, model, branch string) {
	// A banner ends whatever run of lines preceded it: the first line under
	// it states its identity in full, and a repeat count belongs above the
	// rule rather than after it.
	Reset(w)
	if model == "" {
		model = actors.Model(actor)
	}
	if model == "" {
		model = noModel
	}
	who := actors.Display(actor)
	rule := strings.Repeat("=", 60)
	c := ticketColor(key)
	fmt.Fprintln(w)
	fmt.Fprintln(w, paint(w, c, rule))
	fmt.Fprintf(w, "  %s   %s\n", paint(w, c, key), summary)
	fmt.Fprintf(w, "  %s\n", Dim(w, strings.Join(nonEmpty(who, shortModel(model), branch), " - ")))
	fmt.Fprintln(w, paint(w, c, rule))
}

func nonEmpty(in ...string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// shortModel reduces an API model id to the name people use.
//
// The stream reports claude-opus-4-1-20250805; a log column has room for
// "opus". The full id stays in the JSONL for anyone reconciling a bill, and
// an unrecognised id is passed through rather than mangled, so a model this
// build has never heard of still appears.
func shortModel(m string) string {
	for _, name := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(m, name) {
			return name
		}
	}
	return m
}

func statusColor(verb string) string {
	switch verb {
	case VerbOK:
		return green
	case VerbFail:
		return red
	case VerbWarn:
		return yellow
	case VerbWaiting:
		// Distinct from working: nothing is being spent here, a machine or a
		// person is deciding. Sharing a colour would hide which runs cost
		// money.
		return blue
	case VerbWorking:
		return cyan
	}
	return dim
}

// Ticket colours come from a rotation that EXCLUDES the semantic three.
//
// This is the constraint that decides the palette. Red, green and yellow
// mean failure, success and warning everywhere else in this output. A ticket
// assigned red would read as broken on every line it emits, which is worse
// than no colour at all.
//
// A terminal offers few enough colours that a long queue eventually repeats
// one. That is acceptable: the key is in every line, and colour is an
// accelerator rather than the identifier.
var ticketPalette = []string{cyan, magenta, blue, brightCyan, brightMagenta, brightBlue}

var (
	ticketColors = map[string]string{}
	ticketNext   int
)

// ticketColor assigns on first sight and keeps it for the life of the
// ticket, including across ticks -- a colour that changed between ticks
// would be worse than none, because it would say "different ticket".
func ticketColor(key string) string {
	if key == "" {
		return ""
	}
	colorMu.Lock()
	defer colorMu.Unlock()
	if c, ok := ticketColors[key]; ok {
		return c
	}
	// Skip colours another ticket already holds, so two tickets in flight
	// are never the same hue while both are being followed.
	held := map[string]bool{}
	for _, c := range ticketColors {
		held[c] = true
	}
	for range ticketPalette {
		c := ticketPalette[ticketNext%len(ticketPalette)]
		ticketNext++
		if !held[c] {
			ticketColors[key] = c
			return c
		}
	}
	c := ticketPalette[ticketNext%len(ticketPalette)]
	ticketNext++
	ticketColors[key] = c
	return c
}

// Identity paints a string in the colour this actor already carries in the
// run log, so a roster listing and a log agree about which agent is which.
//
// Exported because identities are now rendered on more than one surface:
// the event stream renders them inline, `orion config agents --list`
// renders them in a table. Both draw from the non-semantic palette, so an
// agent name can never read as an outcome -- a name in red says "broken"
// to anyone scanning the output, whatever the column header claims.
//
// Colour only ever accelerates here. The caller must render text that is
// complete without it, because enabled reports false the moment output is
// piped, which is how anybody captures a roster to share it.
func Identity(w io.Writer, id, s string) string { return paint(w, actorColor(id), s) }

// actorColor is deterministic per actor, and drawn from the same
// non-semantic set for the same reason: an agent is not a status.
func actorColor(id string) string {
	if id == "" {
		return ""
	}
	var h uint32 = 2166136261
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= 16777619
	}
	return ticketPalette[int(h)%len(ticketPalette)]
}

// pad widens to n columns, counting RUNES rather than bytes: the separator
// between a name and a job title is a multibyte character, and byte padding
// puts every line carrying one a column to the left of the rest.
//
// Never truncates. An over-long actor pushes the message right instead.
func pad(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func metaWidth(l Line, identity bool) int {
	// Time and icon are on every line now, so both are unconditional here.
	n := 9 + keyWidth + 1 + iconWidth + verbColumn + 1 + actorWidth + 1 + modelWidth + 1
	// An actor or key wider than its column widens the metadata rather than
	// being cut, so the message must be clipped that much harder. A blanked
	// identity column is exactly its own width and can never overflow.
	if over := utf8.RuneCountInString(actors.Display(l.Actor)) - actorWidth; over > 0 && identity {
		n += over
	}
	if over := utf8.RuneCountInString(l.Key) - keyWidth; over > 0 {
		n += over
	}
	return n
}

// columns reads the terminal width from COLUMNS, the only source available
// without a dependency. Absent means "do not clip".
func columns() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS")))
	if err != nil || n < 40 {
		return 0
	}
	return n
}

// terminalRows reads the terminal height from LINES, the counterpart to
// columns() and limited the same way: it is the only source available without
// a dependency, and a shell that does not export it leaves the height unknown.
//
// Absent means "do not grow". The frozen window (live.go) falls back to its
// floor, which is the safe answer in a way that a guessed height is not: guess
// too tall and the region goes off the top of the screen, which is the thing
// the window exists to prevent.
func terminalRows() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("LINES")))
	if err != nil || n < 10 {
		return 0
	}
	return n
}
