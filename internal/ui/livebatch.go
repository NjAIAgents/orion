package ui

// The batch render mode (OR-246).
//
// A new MODE inside the live region, not a second display: it reuses the
// pinned block, the bar, the glyph fallbacks and the plain off-TTY form that
// live.go already owns, and adds the one thing that file cannot express.
//
// What it cannot express is that during a batch the unit of work changes. A
// row-per-ticket is the right shape while agents run -- each ticket has its
// own agent, its own tool calls, its own elapsed. It is the wrong shape during
// CI, because there is exactly ONE run: four bars filled from one shared
// deadline would all read the same value at the same instant, which says four
// independent things are progressing when one is.
//
// So the two quantities are drawn as two different things:
//
//	the BATCH line carries the shared run -- one bar, one elapsed, one median
//	a MEMBER carries its own state, which is what actually differs between them
//
// live.go's per-ticket rows are untouched and keep rendering underneath, so a
// ticket still shows its own life against its own actor's median. Membership
// is what this file adds.

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// BatchPhase is which of the three things a batch is doing.
type BatchPhase string

const (
	BatchAssembling BatchPhase = "assembling"
	BatchTesting    BatchPhase = "testing"
	BatchIsolating  BatchPhase = "isolating"
	BatchDone       BatchPhase = "done"
)

// MemberState is what became of one branch.
//
// Ejected and Culprit are deliberately far apart in every rendering dimension
// -- glyph, colour and words. An ejected branch is SOUND: it did not merge
// cleanly into this set and comes back in the next one. A culprit is the
// branch that broke CI. Drawing them alike sends an operator to debug a branch
// that has nothing wrong with it, which is the single most expensive
// misreading this display can cause.
type MemberState string

const (
	MemberPending MemberState = "pending"
	MemberMerged  MemberState = "merged"
	MemberEjected MemberState = "ejected"
	MemberLanded  MemberState = "landed"
	MemberCulprit MemberState = "culprit"
)

// batchMember is one branch's membership, in the order it was offered.
type batchMember struct {
	key   string
	state MemberState
}

// batchSplit is one node of the bisection, in visit order.
//
// Recorded as a flat list with a depth rather than as a tree of pointers: the
// search is depth-first and never revisits, so visit order IS the drawing
// order, and a flat slice is a snapshot without a deep copy.
type batchSplit struct {
	keys  []string
	green bool
	depth int
	// culprit marks the leaf the search settled on, which is the only node an
	// operator has to act upon.
	culprit bool
}

// liveBatch is the batch's live state. One at a time: a batch is assembled in
// one repository's sandbox from one pass, so there is never a second.
type liveBatch struct {
	ref     string
	base    string
	phase   BatchPhase
	members []batchMember
	splits  []batchSplit
	// ciStarted is when the CURRENT run began, reset per run. The bar measures
	// this run, not the batch's whole life, because the median it is compared
	// against is a median of runs.
	ciStarted time.Time
	median    time.Duration
	runs      int
}

// LiveBatchStart opens a batch. Members are the branches offered to it, in
// order; each starts pending and is resolved by LiveBatchMember.
func LiveBatchStart(ref, base string, keys []string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	b := &liveBatch{ref: ref, base: base, phase: BatchAssembling}
	for _, k := range keys {
		b.members = append(b.members, batchMember{key: k, state: MemberPending})
	}
	live.batch = b
}

// LiveBatchPhase moves the batch on. Entering Testing starts a run: the run
// counter and the bar's clock are the same event, so they are advanced
// together rather than by two calls a caller could get out of step.
func LiveBatchPhase(p BatchPhase) { liveBatchPhase(p, time.Now()) }

func liveBatchPhase(p BatchPhase, at time.Time) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.batch == nil {
		return
	}
	if p == BatchTesting {
		live.batch.ciStarted = at
		live.batch.runs++
	}
	live.batch.phase = p
}

// LiveBatchMember records what became of one branch.
func LiveBatchMember(key string, s MemberState) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.batch == nil {
		return
	}
	for i := range live.batch.members {
		if live.batch.members[i].key == key {
			live.batch.members[i].state = s
			return
		}
	}
	// A key the batch was not opened with still gets a row rather than being
	// dropped: a member appearing late is a bug worth SEEING, and silently
	// discarding it is how the display would come to disagree with the merge.
	live.batch.members = append(live.batch.members, batchMember{key: key, state: s})
}

// LiveBatchSplit records one node of the bisection.
func LiveBatchSplit(keys []string, green bool, depth, runs int, culprit bool) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.batch == nil {
		return
	}
	live.batch.runs = runs
	live.batch.splits = append(live.batch.splits, batchSplit{
		keys: append([]string(nil), keys...), green: green, depth: depth, culprit: culprit})
}

// LiveBatchMedian installs the reference duration for a CI run.
func LiveBatchMedian(d time.Duration) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.batch != nil {
		live.batch.median = d
	}
}

// LiveBatchEnd closes the batch, leaving the per-ticket rows to live.go.
func LiveBatchEnd() {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.batch = nil
}

// snapshotLocked copies the batch for rendering. Called with live.mu held.
func (b *liveBatch) snapshotLocked() *liveBatch {
	c := *b
	c.members = append([]batchMember(nil), b.members...)
	c.splits = append([]batchSplit(nil), b.splits...)
	return &c
}

// memberGlyph is the mark and the colour for one state.
//
// Colour follows the project's semantic rule exactly: green is a pass, red is
// a failure, and an ejection is NEITHER -- it is neutral, because nothing went
// wrong with that branch. Yellow rather than red is the whole reason an
// operator does not go and debug it.
func memberGlyph(w io.Writer, s MemberState) (string, string) {
	ascii := !glyphs()
	switch s {
	case MemberMerged, MemberLanded:
		g := "✓"
		if ascii {
			g = "+"
		}
		return paint(w, green, g), ""
	case MemberEjected:
		g := "↩"
		if ascii {
			g = "~"
		}
		return paint(w, yellow, g), paint(w, yellow, "returns to the queue")
	case MemberCulprit:
		g := "✗"
		if ascii {
			g = "x"
		}
		return paint(w, red, g), paint(w, red, "broke the batch")
	default:
		g := "·"
		if ascii {
			g = "."
		}
		return Dim(w, g), ""
	}
}

// countStates tallies the members by state.
func (b *liveBatch) countStates() map[MemberState]int {
	n := map[MemberState]int{}
	for _, m := range b.members {
		n[m.state]++
	}
	return n
}

// landed is how many branches this batch will actually merge.
func (b *liveBatch) landed() int {
	return b.countStates()[MemberMerged] + b.countStates()[MemberLanded]
}

// costLine is the argument for the whole feature, so it is a headline.
//
// "1 CI run for 4 branches" is the number batching exists to produce, and it
// printed LAST in the first cut -- after the per-member report, where it read
// as a footnote about bookkeeping rather than as the result.
func (b *liveBatch) costLine(w io.Writer) string {
	n := b.landed()
	if b.runs == 0 || n == 0 {
		return ""
	}
	runs := "runs"
	if b.runs == 1 {
		runs = "run"
	}
	branches := "branches"
	if n == 1 {
		branches = "branch"
	}
	return paint(w, bold, fmt.Sprintf("%d CI %s for %d %s", b.runs, runs, n, branches))
}

// renderBatch is the batch block: one headline, then whatever the phase makes
// worth showing underneath.
func renderBatch(w io.Writer, b *liveBatch, now time.Time, cols int) []string {
	if b == nil {
		return nil
	}
	switch b.phase {
	case BatchAssembling:
		return renderAssembling(w, b, cols)
	case BatchTesting:
		return renderTesting(w, b, now, cols)
	case BatchIsolating:
		return renderIsolating(w, b, cols)
	default:
		return renderBatchDone(w, b, cols)
	}
}

func batchHead(w io.Writer, b *liveBatch, right string) string {
	left := fmt.Sprintf("  %s  %d members  %s",
		paint(w, bold, "batch"), len(b.members), b.phase)
	if right == "" {
		return left
	}
	return left + "  " + Dim(w, right)
}

// renderAssembling shows MEMBERSHIP, because that is the only thing that
// differs between branches while the ref is being built. Nothing is measured
// here, so nothing is drawn as though it were.
func renderAssembling(w io.Writer, b *liveBatch, cols int) []string {
	out := []string{batchHead(w, b, fmt.Sprintf("%s ← %s", b.ref, b.base))}
	var cells []string
	for _, m := range b.members {
		g, note := memberGlyph(w, m.state)
		cell := fmt.Sprintf("%s %s", m.key, g)
		if note != "" {
			cell += " " + note
		}
		cells = append(cells, cell)
	}
	return append(out, clip("    "+strings.Join(cells, "   "), cols))
}

// renderTesting is ONE bar for the shared run.
//
// The members are named on their own line without marks of their own: they
// are all in the same run, so any per-member progress drawn here would be the
// same number repeated, formatted to look like several.
func renderTesting(w io.Writer, b *liveBatch, now time.Time, cols int) []string {
	elapsed := now.Sub(b.ciStarted)
	if b.ciStarted.IsZero() {
		elapsed = 0
	}
	// One line, not batchHead plus a bar: the member count, the bar and the
	// run number are one fact about one run, and splitting them over two rows
	// invites reading the second as a per-member thing.
	bar := liveRun{median: b.median}.bar(w, elapsed)
	ci := fmt.Sprintf("  %s  %d members  CI %s  %s",
		paint(w, bold, "batch"), len(b.members), bar, elapsedString(elapsed))
	// "median" names what the number IS. Without it "/ ~11m" reads as an
	// estimate of when this run will finish, which is the prediction the bar
	// deliberately refuses to make.
	if b.median > 0 {
		ci += Dim(w, fmt.Sprintf(" / ~%s median", coarse(b.median)))
	} else {
		// Same reason as liveRun.notes: a blank bar with nothing beside it
		// reads as unbuilt rather than as unmeasured.
		ci += "  " + Dim(w, "no baseline yet")
	}
	ci += "  " + Dim(w, fmt.Sprintf("run %d", b.runs))

	var keys []string
	var ejected []string
	for _, m := range b.members {
		if m.state == MemberEjected {
			ejected = append(ejected, m.key)
			continue
		}
		keys = append(keys, m.key)
	}
	out := []string{clip(ci, cols), clip("    "+Dim(w, strings.Join(keys, "  ")), cols)}
	// An ejected branch is out of THIS run but not out of the picture, and it
	// stays on screen saying so. Dropping it silently was the one thing the
	// operator could not otherwise account for: a batch of four that names
	// three members, with nothing anywhere explaining the fourth.
	//
	// Its own row rather than a name in the list above, because that list is
	// "who is in this CI run" and it is not.
	for _, key := range ejected {
		g, note := memberGlyph(w, MemberEjected)
		out = append(out, clip(fmt.Sprintf("    %s %s  %s", g, key, Dim(w, note)), cols))
	}
	return out
}

// renderIsolating draws the SEARCH, because the shape of the search is what
// explains the cost.
//
// A row per member could say which branch broke; only the tree says why
// finding it took four runs rather than one, and that number is what the
// binary search is justified by.
func renderIsolating(w io.Writer, b *liveBatch, cols int) []string {
	out := []string{batchHead(w, b, fmt.Sprintf("run %d", b.runs))}
	for i, s := range b.splits {
		mark := paint(w, green, "✓")
		if !s.green {
			mark = paint(w, red, "✗")
		}
		if !glyphs() {
			mark = "+"
			if !s.green {
				mark = "x"
			}
			mark = paint(w, map[bool]string{true: green, false: red}[s.green], mark)
		}
		branch := ""
		if s.depth > 0 {
			stem := "├"
			if i == len(b.splits)-1 {
				stem = "└"
			}
			if !glyphs() {
				stem = "|"
				if i == len(b.splits)-1 {
					stem = "`"
				}
			}
			branch = strings.Repeat(" ", s.depth) + Dim(w, stem) + " "
		}
		line := fmt.Sprintf("    %s[%s]  %s", branch, strings.Join(s.keys, " "), mark)
		if s.culprit {
			arrow := "←"
			if !glyphs() {
				arrow = "<-"
			}
			line += "  " + paint(w, red, arrow+" culprit")
		}
		out = append(out, clip(line, cols))
	}
	if n := b.landed(); n > 0 {
		out = append(out, clip(fmt.Sprintf("    %s", b.costLine(w)), cols))
	}
	return out
}

// renderBatchDone is the durable summary: the cost first, then one line per
// member with what became of it.
func renderBatchDone(w io.Writer, b *liveBatch, cols int) []string {
	out := []string{"  " + b.costLine(w)}
	for _, m := range b.members {
		g, note := memberGlyph(w, m.state)
		line := fmt.Sprintf("    %s %s  %s", g, pad(m.key, liveKeyWidth), m.state)
		if note != "" {
			line += "  " + note
		}
		out = append(out, clip(line, cols))
	}
	return out
}

// renderBatchPlain is the off-TTY form: one line, no cursor control, no bar.
//
// Same contract renderPlain already meets. A batch in a log file is a
// transition record, so it states the phase, the counts and the run count and
// nothing that only means something while animating.
func renderBatchPlain(b *liveBatch, now time.Time) string {
	if b == nil {
		return ""
	}
	n := b.countStates()
	parts := []string{
		now.Local().Format("15:04"),
		"batch",
		string(b.phase),
		fmt.Sprintf("%d members", len(b.members)),
	}
	if b.runs > 0 {
		parts = append(parts, fmt.Sprintf("run %d", b.runs))
	}
	// Named only when non-zero, so a healthy batch does not print three
	// counters of nothing on every tick.
	for _, s := range []MemberState{MemberMerged, MemberLanded, MemberEjected, MemberCulprit} {
		if n[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[s], s))
		}
	}
	return strings.Join(parts, "  ")
}

// batchKeys is the members' keys in a stable order, for tests and for the
// event record.
func (b *liveBatch) batchKeys() []string {
	out := make([]string, 0, len(b.members))
	for _, m := range b.members {
		out = append(out, m.key)
	}
	sort.Strings(out)
	return out
}
