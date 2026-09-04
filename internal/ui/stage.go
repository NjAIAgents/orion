package ui

// Stage boundaries: the line that says the run just crossed from one stage
// into the next, and who holds each side.
//
// Why this is not a sixth verb. The verb column answers ONE question -- "do I
// have to do something" -- and that question has the five values event.go
// lists. A handoff needs nothing from the operator, so its verb would be `ok`
// and it would look like every other `ok` line. What was missing was never a
// word; it was an AXIS. The end of implementation used to read:
//
//	13:34:52 OR-183   ✓  ok       <developer> · backend developer  opus   2 commit(s) on orion/or-183
//	13:34:52 OR-183   ◐  working  <qa> · QA engineer               sonnet verifying with ...
//
// Two lines, the same second, and nothing saying the run had crossed out of
// implementation. A reader had to know which actor holds which role and infer
// the handoff from the names changing (OR-189).
//
// So a boundary is distinguished by LAYOUT: it drops the icon/verb/actor/
// model columns entirely and puts one continuous sentence where they were.
// Nothing else in this renderer looks like that, which is what makes it
// findable by eye without reading it.
//
// AND THE WORDS CARRY THE MEANING. The rule from OR-163 binds harder here
// than anywhere: a boundary recognisable only by colour is invisible in a
// piped log, in CI output, and to grep. Every boundary therefore contains the
// literal word "stage", both stage names, and both parties, in text. Colour
// and the box-drawing rule only reinforce, and both degrade -- the rule and
// the arrow fall back to ASCII the same way iconFor does.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

// Handoff is one boundary: the stage being left, the stage being entered, and
// the party holding each side.
//
// By and Next are ACTOR IDENTIFIERS, resolved to display names at render
// time. They are not required to be agents: the roster already models the two
// parties that are not -- ci is a machine and human is the person reading
// this -- and naming who is next is worthless if it cannot say "a machine" or
// "you" when that is the truth.
type Handoff struct {
	// At, when non-zero, prefixes the line with a local time, exactly as
	// Line.At does. Zero means now.
	At   time.Time
	Key  string // FCIA-8
	From string // the stage being left, e.g. "implementing"
	To   string // the stage being entered, e.g. "qa"
	By   string // actor id holding the stage being left
	Next string // actor id holding the stage being entered
	// Detail is what this particular crossing is worth saying -- "2 commit(s)
	// on orion/or-183", "round 1 of 2". It rides ON the boundary rather than
	// on a line of its own, because a line reporting an artifact count in
	// place of a handoff is what OR-189 was filed about.
	Detail string
}

// The boundary's own punctuation. Both degrade, for the reason glyphs()
// exists: a box-drawing character on a non-UTF-8 terminal is mojibake, and
// the transition still has to be legible when it is.
const (
	stageRuleGlyph  = "══"
	stageRuleASCII  = "=="
	stageArrowGlyph = "→"
	stageArrowASCII = "->"
	// stageWord is in the line as a WORD so `grep stage` finds every
	// boundary in a piped log. It is the whole point of this file.
	stageWord = "stage"
)

func stageRule() string {
	if glyphs() {
		return stageRuleGlyph
	}
	return stageRuleASCII
}

func stageArrow() string {
	if glyphs() {
		return stageArrowGlyph
	}
	return stageArrowASCII
}

// Stage prints one boundary and records it in the event log.
//
// ONE call, doing both, deliberately. internal/work and cmd/orion's fix loop
// are different packages crossing the same boundaries, and the last time a
// second copy of a shared logger was hand-rolled in the other package it
// printed unattributed lines and emitted nothing to the event log at all
// (OR-176). A helper that cannot print without emitting is how that stays
// impossible rather than merely discouraged.
//
// log may be nil: Emit tolerates it, and a boundary must never be the reason
// a run fails.
func Stage(w io.Writer, log *events.Log, h Handoff) {
	// Stamped ONCE, here, so the printed line and the recorded event agree on
	// when the crossing happened. Letting each side default to its own
	// time.Now() makes them disagree by a hair, which is invisible until the
	// two straddle a second and a reader correlating a console capture with
	// the JSONL finds the same boundary at two different times.
	if h.At.IsZero() {
		h.At = time.Now()
	}
	// A boundary is where a run of lines ends: any pending repeat count is
	// printed before it rather than after, and the first line of the new
	// stage states its actor in full (OR-217).
	Reset(w)
	fmt.Fprintln(w, RenderStage(w, h))
	log.Emit(events.Event{
		Kind: events.KindStage, Key: h.Key,
		// Whoever now holds the run. An event log filtered to one actor
		// should show the boundary that handed the work TO them.
		Actor: h.Next,
		At:    h.At.UTC(),
		Msg:   plainTransition(h),
		// Identifiers, never display names -- see the KindStage comment in
		// internal/events. This is also what lets `orion logs` re-render the
		// boundary in full rather than dumping the map as loose key: value
		// lines under it.
		Detail: map[string]any{
			detailFrom: h.From, detailTo: h.To,
			detailBy: h.By, detailNext: h.Next, detailNote: h.Detail,
		},
	})
}

// Detail keys. Named constants because Stage writes them and HandoffOf reads
// them back, in different packages' service, and a typo in either would fail
// silently as an empty column.
const (
	detailFrom = "from"
	detailTo   = "to"
	detailBy   = "by"
	detailNext = "next"
	detailNote = "detail"
)

// HandoffOf reconstructs a boundary from a recorded event, so history renders
// through the same function the live run used. Two formatters over one event
// stream drift, and the drift shows up as the same run reading differently
// depending on which command you typed.
func HandoffOf(e events.Event) Handoff {
	return Handoff{
		At:   e.At,
		Key:  e.Key,
		From: detailString(e, detailFrom), To: detailString(e, detailTo),
		By: detailString(e, detailBy), Next: detailString(e, detailNext),
		Detail: detailString(e, detailNote),
	}
}

func detailString(e events.Event, key string) string {
	s, _ := e.Detail[key].(string)
	return s
}

// RenderStage returns one formatted boundary, without a newline.
//
// Deliberately NOT clipped to the terminal width, unlike Render. What Render
// clips is an agent's prose, whose first words carry the sense; what would be
// clipped here is who holds the run next, which is the entire reason the line
// exists. A boundary that wraps is readable; a boundary cut at "hands to …"
// is not.
func RenderStage(w io.Writer, h Handoff) string {
	at := h.At
	if at.IsZero() {
		at = time.Now()
	}
	var b strings.Builder
	// The same two leading columns as every other line, so the timestamp and
	// the ticket thread stay in one place down the page. Everything after
	// them is where a boundary stops looking like a status line.
	b.WriteString(Dim(w, at.Local().Format("15:04:05")) + " ")
	b.WriteString(paint(w, ticketColor(h.Key), pad(h.Key, keyWidth)) + " ")

	rule := stageRule()
	b.WriteString(Dim(w, rule+" "+stageWord+" "+rule) + " ")
	b.WriteString(paint(w, bold, h.From+" "+stageArrow()+" "+h.To) + "  ")
	b.WriteString(handoffClause(h))
	return b.String()
}

// plainTransition is the boundary as the event log records it: stage names
// only, ASCII, no party names. The parties live in Detail as identifiers
// because a name written into the log would be wrong the day it changed.
func plainTransition(h Handoff) string {
	s := h.From + " " + stageArrowASCII + " " + h.To
	if h.Detail != "" {
		s += "; " + h.Detail
	}
	return s
}

// handoffClause names BOTH sides in words.
//
// Two adjacent lines with different names leaves the handoff as an exercise
// for the reader; "X hands to Y" is the fact they wanted. When one party
// holds both sides -- Orion pushing a branch and then opening the pull
// request for it -- "orion hands to orion" is technically true and reads as
// nonsense, so that case says it continues.
func handoffClause(h Handoff) string {
	// "hands" agrees with a third person and not with the reader, who the
	// roster calls "you". A boundary out of an approval is one of the nine,
	// so this is a sentence that gets printed, not a hypothetical.
	hands := " hands to "
	if h.By == events.ActorHuman {
		hands = " hand to "
	}
	clause := party(h.By) + hands + party(h.Next)
	if h.By == h.Next {
		clause = party(h.By) + " continues"
	}
	parts := []string{clause}
	if h.Detail != "" {
		parts = append(parts, h.Detail)
	}
	// THE PART MOST EASILY GOT WRONG. At "pull request opened, awaiting CI"
	// the next party is a machine, and at "checks pass" it is a person. A
	// line that named an agent there would have the operator watching a
	// developer apparently work for the length of a CI run or an overnight
	// approval wait -- the same defect as an unmarked handoff, pointed the
	// other way. Stated by the renderer rather than by each call site, so it
	// cannot be forgotten at one of them.
	if !agentSide(h.Next) {
		parts = append(parts, "no agent is running")
	}
	return strings.Join(parts, "; ")
}

// agentSide reports whether a party is an agent that is running and spending.
//
// The roster already draws this line: ci and human are the two identifiers it
// refuses to give a name to, precisely because one is a machine and the other
// is the reader. Nothing else needs listing here.
func agentSide(id string) bool {
	return id != "" && id != events.ActorCI && id != events.ActorHuman
}

// party is what a side is called. Display resolves ci to "ci" and human to
// "you", which is why a machine and a person need no special case: the
// registry is already the one place that knows what anything is called.
func party(id string) string {
	if id == "" {
		return noModel // "not applicable" reads better than a blank
	}
	return actors.Display(id)
}
