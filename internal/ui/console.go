package ui

// How MUCH is printed, and how often the same thing is printed twice.
//
// THE CONSOLE IS FOR ATTENTION, THE LOG FILE IS FOR EVIDENCE. That is the
// whole of this file. Counted from one real screen at concurrency 4 with two
// tickets in flight: about 50 visible lines, of which ~30 were the agent's
// tool-call transcript -- "ran git status" three times in eight seconds,
// "ran echo waiting", a sandbox temp path so long the only useful token in
// it was the part clipped away. Every one of those lines was ALREADY in the
// event log and the run log. The console was paying for a transcript nobody
// reads in real time with the ten lines that decide whether a person has to
// act (OR-217).
//
// So this is a filter at the PRINT SITE, not a second renderer and not a new
// colour system. OR-163 settled the palette and the five status verbs and
// OR-189 settled the stage axis; a prettier render of 200 useless lines is
// still 200 useless lines. Four rules, in order of how much they remove:
//
//  1. TWO LEVELS, QUIET BY DEFAULT. A line marked Trace -- the tool-call
//     transcript -- is printed only under --verbose. Nothing else is
//     withheld: stage boundaries, outcomes, escalations, failures and
//     anything awaiting a person print at both levels.
//  2. IDENTITY ONLY WHEN IT CHANGES. Twenty-five consecutive lines from one
//     actor restated thirty characters of name and model on every one of
//     them. The columns stay -- alignment is the point of them -- they are
//     simply not re-stated, so the eye tracks the key and the verb, which
//     are what vary.
//  3. CONSECUTIVE IDENTICAL ACTIONS COLLAPSE. Four identical lines become
//     the line plus a count.
//  4. A LONG ABSOLUTE PATH SHORTENS TO ITS BASE NAME. The full path is in
//     the event log, which is where a path is evidence rather than noise.
//
// WHAT THIS DOES NOT TOUCH is the record. Nothing here runs before an
// events.Log emit; the JSONL, the run log and `orion logs` are complete
// whatever the verbosity, because OR-168's triage reads the event log and
// OR-199's history depends on it. `orion logs` renders through Print too and
// marks nothing as Trace, so reading history back still shows every tool
// call.

import (
	"fmt"
	"io"
	"path"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// verbose is the level, as a process-wide setting.
//
// Process-wide because the alternative is threading a level through every
// call site in internal/work, internal/collect and internal/watch, and the
// thing being configured is one operator's one terminal. A watcher has one
// console however many jobs write to it.
var verbose atomic.Bool

// SetVerbose selects the level. False -- the default -- is quiet.
func SetVerbose(on bool) { verbose.Store(on) }

// Verbose reports whether the full stream is being printed. Exported so a
// caller can say so in a banner rather than leaving the operator to wonder
// what they are not being shown.
func Verbose() bool { return verbose.Load() }

// console is the state the three suppression rules need: what was printed
// last, and where.
//
// Keyed on the WRITER as well as the line. Two different destinations are
// two different pages, and identity suppressed on one because of what was
// printed to the other would be a line missing its actor for no reason a
// reader could see.
var console struct {
	mu   sync.Mutex
	w    io.Writer
	last Line
	// have distinguishes "nothing printed yet" from "a zero Line printed",
	// which are the same value and very different situations.
	have bool
	// repeat counts identical lines suppressed since last was printed.
	repeat int
	// at is when last was printed, for the staleness rule below.
	at time.Time
}

// identityRefresh is how long a line can be off-screen-adjacent before the
// next one re-states who it belongs to.
//
// Shorter than the heartbeat interval so that every heartbeat carries its
// identity, and long enough that a burst of tool calls in the same second
// still collapses to one identity line -- which is the whole point of the
// suppression it relaxes.
const identityRefresh = 10 * time.Second

// clock is time.Now, replaceable so a test can exercise a rule about
// elapsed time without electing to take that long.
var clock = time.Now

// printLine is the funnel every status line goes through: Say, SayModel,
// Trace, notify's echo and `orion logs`.
func printLine(w io.Writer, l Line) {
	// Rule 1, first, so a suppressed line costs nothing else.
	if l.Trace && !verbose.Load() {
		return
	}
	l.Msg = shortenPaths(l.Msg)

	console.mu.Lock()
	defer console.mu.Unlock()
	if !sameWriter(console.w, w) {
		flushLocked()
		console.w, console.have = w, false
	}
	// Rule 3. Held rather than printed: the first of the run is already on
	// screen, so what a reader needs from the rest is the count.
	if console.have && sameAction(console.last, l) {
		console.repeat++
		return
	}
	flushLocked()
	// Rule 2. The first line after any change -- of ticket, actor or model --
	// states the identity again.
	//
	// Rule 2b: so does the first line after a GAP. Suppression assumes the
	// reader still has the previous line in view, which holds for a burst
	// and stops holding for a heartbeat thirty seconds later (OR-338). A
	// long agent run then rendered as fifteen consecutive lines with an
	// empty actor column and no way to tell whose they were -- measured on
	// a real watch, 23:02 to 23:10 (OR-346).
	identity := !console.have || !sameIdentity(console.last, l) ||
		clock().Sub(console.at) >= identityRefresh
	fmt.Fprintln(w, renderLine(w, l, identity))
	console.last, console.have, console.repeat, console.at = l, true, 0, clock()
}

// Flush prints the count for a run of identical lines that has not been
// broken by a different line.
//
// Called at the end of a run and at every boundary, because the alternative
// is a repeat count that arrives after the thing it counts has stopped being
// relevant -- or, at the end of a watcher, never.
func Flush(w io.Writer) {
	console.mu.Lock()
	defer console.mu.Unlock()
	if !sameWriter(console.w, w) {
		return
	}
	flushLocked()
}

// Reset flushes and forgets the last line, so the next one states its
// identity in full.
//
// What a boundary is FOR: a stage handoff or a banner ends the run of lines
// before it, and the first line of the new stage restating who is acting is
// the point rather than repetition.
func Reset(w io.Writer) {
	console.mu.Lock()
	defer console.mu.Unlock()
	if sameWriter(console.w, w) {
		flushLocked()
	}
	console.have = false
}

// ConsoleReset forgets the destination as well as the last line, so nothing
// about the previous run can suppress the next one's first line.
//
// Reset above is a boundary WITHIN a run and deliberately keeps console.w.
// This is the boundary BETWEEN runs. It exists because the console is
// process-global while sameWriter compares by pointer: once a writer is
// collected, a new one can be allocated at the same address, compare equal to
// the stale console.w, and have its first line swallowed by rule 3 as a
// repeat of a line belonging to a run that has already ended. That is
// invisible when it happens -- the text is counted, never written -- and it
// depends on the allocator, so it appears as a test that passes locally and
// fails perhaps one run in three on a busier machine (OR-262).
//
// For tests, and for a second watcher in one process, exactly as LiveReset.
func ConsoleReset() {
	console.mu.Lock()
	defer console.mu.Unlock()
	flushLocked()
	console.w, console.have, console.repeat, console.last = nil, false, 0, Line{}
}

func flushLocked() {
	if console.repeat == 0 || !console.have || console.w == nil {
		return
	}
	l := console.last
	l.Msg = fmt.Sprintf("%s (x%d)", l.Msg, console.repeat+1)
	// Identity suppressed: it is by definition the identity of the line
	// directly above.
	fmt.Fprintln(console.w, renderLine(console.w, l, false))
	console.repeat = 0
}

// sameAction is "this is the same thing happening again". Everything the
// line says except the timestamp, which is the one field that must not stop
// two identical actions from collapsing.
func sameAction(a, b Line) bool {
	return sameIdentity(a, b) && a.Verb == b.Verb && a.Msg == b.Msg && a.Trace == b.Trace
}

// sameIdentity is the actor/model columns, plus the KEY.
//
// The key is in here because two tickets interleave at concurrency, and a
// developer line on OR-217 followed by a developer line on OR-220 is not a
// continuation of anything -- blanking the second one's identity would
// invite the reader to attribute it to the thread above it.
func sameIdentity(a, b Line) bool {
	return a.Key == b.Key && a.Actor == b.Actor && a.Model == b.Model
}

// sameWriter compares two destinations without risking the panic an
// interface comparison can raise.
//
// == on interfaces panics when the dynamic type is not comparable. Every
// writer here is a pointer, so this never fires in practice -- and a
// renderer that can panic is a renderer that can take down a run at the
// moment somebody is reading it to find out what went wrong. An
// uncomparable writer degrades to "a different page", which prints identity
// more often rather than less.
func sameWriter(a, b io.Writer) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	return ta == tb && ta.Comparable() && a == b
}

// pathMinLen is how long an absolute path has to be before its base name is
// printed instead.
//
// Long enough that the paths a reader can actually use -- /etc/hosts, a
// short repo path -- are left alone, and short enough to catch the sandbox
// temp paths that consume a line and identify nothing. The whole path is in
// the event log either way.
const pathMinLen = 40

// shortenPaths replaces long absolute paths with their base name.
//
// Whitespace-separated fields, and only fields that BEGIN with a slash: a
// URL, a branch name (orion/or-217) and a relative path all survive
// untouched, and those are the three things in these messages that look like
// a path and must not be cut.
func shortenPaths(msg string) string {
	if !strings.Contains(msg, "/") {
		return msg
	}
	fields := strings.Split(msg, " ")
	for i, f := range fields {
		// Trailing punctuation is part of the sentence, not of the path:
		// "left /a/b/c behind:" must keep its colon.
		trimmed := strings.TrimRight(f, ".,;:)")
		tail := f[len(trimmed):]
		if !strings.HasPrefix(trimmed, "/") || len(trimmed) < pathMinLen {
			continue
		}
		if base := path.Base(trimmed); base != "" && base != "/" {
			fields[i] = base + tail
		}
	}
	return strings.Join(fields, " ")
}
