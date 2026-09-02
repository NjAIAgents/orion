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
	// MemberPending is offered but not yet acted on -- grey, because nothing
	// has happened to it and a colour would imply something had.
	MemberPending MemberState = "pending"
	// MemberWorking is being merged or tested right now -- yellow. Without
	// it a batch mid-assembly looks identical to one that has not started,
	// which is the "is this thing moving" question the region exists for.
	MemberWorking MemberState = "working"
	MemberMerged  MemberState = "merged"
	MemberEjected MemberState = "ejected"
	MemberLanded  MemberState = "landed"
	MemberCulprit MemberState = "culprit"
)

// batchMember is one branch's membership, in the order it was offered.
type batchMember struct {
	key   string
	state MemberState
	// detail is what this member's outcome needs a person to know and the
	// state word cannot carry: the file a branch conflicted on, the check
	// that failed. The mockup puts it at the end of the row, and it is the
	// difference between "go and look" and "go and look at THIS".
	detail string
	// elapsed and cost are the member's own, for the done summary. Zero means
	// unknown, which renders as an em dash rather than as 0m/$0.00 -- an
	// ejected branch never ran, and reporting it as free work is a different
	// claim from reporting it as unmeasured.
	elapsed time.Duration
	cost    float64
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
	// checks is the current run's individual checks. On the batch rather than
	// beside it because they ARE the batch's run: a check outliving the batch
	// it belongs to would be a check with nothing to describe.
	checks []Check
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
		// Everything in the run is now in flight. Merged means "in the ref",
		// which was true a moment ago and is no longer the interesting fact:
		// what matters during CI is that these are the ones being tested.
		for i := range live.batch.members {
			if live.batch.members[i].state == MemberMerged {
				live.batch.members[i].state = MemberWorking
			}
		}
	}
	live.batch.phase = p
}

// LiveBatchMember records what became of one branch.
func LiveBatchMember(key string, s MemberState) { LiveBatchMemberDetail(key, s, "") }

// LiveBatchMemberDetail is LiveBatchMember plus the one fact the state word
// cannot carry -- the conflicting file, the failing check.
func LiveBatchMemberDetail(key string, s MemberState, detail string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.batch == nil {
		return
	}
	for i := range live.batch.members {
		if live.batch.members[i].key == key {
			live.batch.members[i].state = s
			if detail != "" {
				live.batch.members[i].detail = detail
			}
			return
		}
	}
	// A key the batch was not opened with still gets a row rather than being
	// dropped: a member appearing late is a bug worth SEEING, and silently
	// discarding it is how the display would come to disagree with the merge.
	live.batch.members = append(live.batch.members, batchMember{key: key, state: s})
}

// LiveBatchMemberCost records what one member's ticket cost and how long it
// took, for the done summary. Pushed from internal/cost, which already sees
// every run's spend.
func LiveBatchMemberCost(key string, elapsed time.Duration, usd float64) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.batch == nil {
		return
	}
	for i := range live.batch.members {
		if live.batch.members[i].key == key {
			// Accumulated: a ticket that went round a fix loop spent what all
			// of its runs spent, and the LAST run's figure would understate
			// exactly the branch that cost the most.
			live.batch.members[i].elapsed += elapsed
			live.batch.members[i].cost += usd
			return
		}
	}
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
	c.checks = append([]Check(nil), b.checks...)
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
	case MemberWorking:
		g := "◐"
		if ascii {
			g = ">"
		}
		return paint(w, yellow, g), ""
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
	// The CI track, empty: no run has started, and the line keeps the shape
	// it will have when one does rather than growing a bar mid-phase.
	head := fmt.Sprintf("  %s  %s  %d members  %s  CI %s",
		paint(w, bold, "batch"), b.memberBar(w), len(b.members), b.phase, b.bar(w, 0))
	head += "   " + Dim(w, fmt.Sprintf("%s ← %s", b.ref, b.base))
	out := []string{clip(head, cols), ""}
	// A ROW EACH, as the mockup has it. Membership is the information here,
	// and a row carries what a cell cannot: whether the branch got in, and
	// for the one that did not, the file it conflicted on. Cramming four
	// members onto one line saved three rows and cost the reason.
	for _, m := range b.members {
		g, note := memberGlyph(w, m.state)
		what := "merged into the ref"
		switch m.state {
		case MemberEjected:
			what = "ejected"
		case MemberPending:
			what = ""
		}
		line := fmt.Sprintf("    %s %s  %s",
			g, paint(w, ticketColor(m.key), pad(m.key, liveKeyWidth)), Dim(w, what))
		if note != "" {
			line += "  " + note
		}
		out = append(out, clip(strings.TrimRight(line, " "), cols))
		// The conflicting file on its own line under the row it belongs to,
		// indented past the key so it reads as a continuation rather than as
		// another member.
		if m.detail != "" {
			out = append(out, clip("               "+Dim(w, m.detail), cols))
		}
	}
	return out
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
	bar := b.bar(w, elapsed)
	// The CI bar is the CHECKS when there are any -- one labelled segment
	// each, so the line says which platform is still going rather than only
	// how long the run has taken. The time bar stays beside it: composition
	// and duration are two questions, and neither answers the other.
	ciBar := checkBar(w, b.checks)
	if ciBar == "" {
		ciBar = bar
	} else {
		ciBar += "  " + bar
	}
	ci := fmt.Sprintf("  %s  %s  %d members  CI %s  %s",
		paint(w, bold, "batch"), b.memberBar(w), len(b.members), ciBar, elapsedString(elapsed))
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
	// The keys are already named, with their state, in the segmented bar on
	// the line above. Repeating them as a bare list said the same thing twice
	// and cost a row doing it, so what stays is the bar.
	out := []string{clip(ci, cols)}
	// An ejected branch is out of THIS run but not out of the picture, and it
	// stays on screen saying so. Dropping it silently was the one thing the
	// operator could not otherwise account for: a batch of four that names
	// three members, with nothing anywhere explaining the fourth.
	//
	// Its own row rather than a name in the list above, because that list is
	// "who is in this CI run" and it is not.
	if len(ejected) > 0 {
		out = append(out, "")
	}
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
// batchBarWidth is the CI bar's width.
//
// Wider than a ticket's, because it measures the thing the whole batch is
// waiting on: one run covering every member. A headline measure drawn at the
// same width as the per-ticket bars beneath it reads as one more row rather
// than as the row they are all blocked behind.
const batchBarWidth = 22

// bar is the shared CI run's progress, drawn wide.
//
// It draws the TRACK in every phase, not only while testing. A batch that is
// assembling has no run to measure, and an empty track says exactly that --
// the line keeps its shape, and the fill arriving is what marks CI starting.
// The alternative is a batch line that changes width as it moves between
// phases, which reads as the display redrawing rather than as work
// progressing.
func (b *liveBatch) bar(w io.Writer, elapsed time.Duration) string {
	full, head, empty := barFullGlyph, barHeadGlyph, barEmptyGlyph
	if !glyphs() {
		full, head, empty = barFullASCII, barHeadASCII, barEmptyASCII
	}
	if b.median <= 0 || elapsed <= 0 {
		return Dim(w, strings.Repeat(empty, batchBarWidth))
	}
	n := int(float64(batchBarWidth) * float64(elapsed) / float64(b.median))
	if n < 0 {
		n = 0
	}
	if n >= batchBarWidth {
		return paint(w, green, strings.Repeat(full, batchBarWidth))
	}
	return paint(w, green, strings.Repeat(full, n)+head) +
		Dim(w, strings.Repeat(empty, batchBarWidth-n-1))
}

// memberBarWidth is the segmented membership bar's width.
//
// Divided evenly between members, so the bar IS the batch: how many are in
// it, and what became of each. Twenty-two so that a batch of up to about
// eight members still gets a segment wide enough to read a colour from.
const memberBarWidth = 22

// checkBarWidth is the CI bar's width.
//
// Wider than the member bar because its labels are words rather than
// three-digit numbers: at 22 a three-platform run could not fit "windows"
// with a cell of colour either side, so every segment fell back to bare fill
// and the bar said nothing the time bar was not already saying.
const checkBarWidth = 30

// memberBar draws the batch as a row of segments, one per member.
//
// A second bar beside the CI one because they answer different questions and
// neither can answer the other's. The CI bar says how long this run has taken
// against the median -- the question during a thirty-minute wait. This says
// WHO IS IN and what became of them, which is the question when a batch of
// four lands two, ejects one and convicts another.
//
// Colour carries the state, and the words beside the member rows carry it
// again: a bar whose meaning lives only in colour says nothing in NO_COLOR or
// read aloud (OR-163). This is the glance; the rows are the record.
func (b *liveBatch) memberBar(w io.Writer) string {
	if len(b.members) == 0 {
		return ""
	}
	full, empty := barFullGlyph, barEmptyGlyph
	if !glyphs() {
		full, empty = barFullASCII, barEmptyASCII
	}
	// Evenly, with the remainder spread over the leftmost segments rather
	// than left as a ragged gap at one end.
	seg := memberBarWidth / len(b.members)
	if seg < 1 {
		seg = 1
	}
	extra := memberBarWidth - seg*len(b.members)

	// ALL the segments get a label or none do. A bar where the first three
	// members are named and the rest are bare reads as a rendering fault
	// rather than as a deliberate limit -- and the unlabelled ones are
	// exactly the members an operator would then assume are missing.
	label := true
	for i, m := range b.members {
		n := seg
		if i < extra {
			n++
		}
		if n < len(shortKey(m.key))+2 {
			label = false
			break
		}
	}

	var out strings.Builder
	for i, m := range b.members {
		n := seg
		if i < extra {
			n++
		}
		// GREEN done, YELLOW in flight, GREY not started, RED broke it.
		//
		// The ejection is the one that cannot follow that scheme: it is
		// neither done nor broken, and painting it red would say a sound
		// branch needs debugging -- the single most expensive misreading
		// this display can make. It gets the HOLLOW glyph instead, so it is
		// visibly accounted for without claiming a verdict it has not
		// earned, and the row beside it says "returns to the queue".
		glyph, colour := empty, ""
		switch m.state {
		case MemberLanded, MemberMerged:
			glyph, colour = full, green
		case MemberWorking:
			glyph, colour = full, yellow
		case MemberCulprit:
			glyph, colour = full, red
		case MemberEjected:
			glyph, colour = ejectGlyph(), dim
		}
		// The ticket's number INSIDE its segment, so the bar says which
		// branch each block is rather than only how many there are. The
		// project prefix is dropped: it is the same on every member of a
		// batch, so it would spend a third of the segment saying nothing.
		//
		// Only when it fits with a cell of colour either side, or the label
		// swallows the segment and the bar stops reading as a bar.
		if colour == "" {
			colour = dim
		}
		out.WriteString(labelledSegment(w, glyph, shortKey(m.key), n, colour, label))
	}
	return out.String()
}

// checkBar draws the CI run as one segment per check, named.
//
// The same shape as memberBar and for the same reason: a single bar filling
// against a median says how long the run has taken, and says nothing about
// WHICH of three platforms is still going. During a batch that is the
// question -- ubuntu green and windows nine minutes in is a different
// situation from all three still starting, and a time bar renders them
// identically.
//
// Both bars stay. This one is composition; the time bar beside it is
// duration. Neither can answer the other's question.
func checkBar(w io.Writer, checks []Check) string {
	if len(checks) == 0 {
		return ""
	}
	full, empty := barFullGlyph, barEmptyGlyph
	if !glyphs() {
		full, empty = barFullASCII, barEmptyASCII
	}
	seg := checkBarWidth / len(checks)
	if seg < 1 {
		seg = 1
	}
	extra := checkBarWidth - seg*len(checks)

	// All labelled or none, exactly as memberBar: a bar naming two of three
	// checks reads as a fault rather than as a limit.
	label := true
	for i, c := range checks {
		n := seg
		if i < extra {
			n++
		}
		if n < len(shortCheck(c.Name))+2 {
			label = false
			break
		}
	}

	var out strings.Builder
	for i, c := range checks {
		n := seg
		if i < extra {
			n++
		}
		// GLYPH as well as colour, so the three states are still distinct
		// under NO_COLOR: a bar whose meaning lives only in colour says
		// nothing in a screenshot or read aloud (OR-163). Passed is solid,
		// running is the half-filled head the time bar already uses for
		// "in progress", failed is the hollow glyph.
		glyph, colour := empty, ""
		switch c.State {
		case CheckPassed:
			glyph, colour = full, green
		case CheckFailed:
			glyph, colour = ejectGlyph(), red
		case CheckRunning:
			glyph, colour = barHeadGlyph, yellow
			if !glyphs() {
				glyph = barHeadASCII
			}
		}
		if colour == "" {
			colour = dim
		}
		out.WriteString(labelledSegment(w, glyph, shortCheck(c.Name), n, colour, label))
	}
	return out.String()
}

// shortCheck is the part of a check name that varies.
//
// "go (ubuntu-latest)" and "go (macos-latest)" differ only inside the
// brackets, so the bracketed platform is the whole of what a label has to
// carry -- and "-latest" is on every one of them, which makes it noise too.
func shortCheck(name string) string {
	if i := strings.IndexByte(name, '('); i >= 0 {
		if j := strings.IndexByte(name[i:], ')'); j > 0 {
			name = name[i+1 : i+j]
		}
	}
	name = strings.TrimSuffix(name, "-latest")
	// The platforms are the common case, and these are what a person calls
	// them. "ubuntu" is the runner's name for it; Linux is the thing.
	switch name {
	case "ubuntu", "linux":
		return "Linx"
	case "macos":
		return "Mac"
	case "windows":
		return "Win"
	}
	if len(name) > 6 {
		name = name[:6]
	}
	return name
}

// labelledSegment is one bar segment with its name ON it.
//
// The label does not replace the fill and does not sit beside it: the cells
// it occupies keep the segment's colour and the text is reversed out of them,
// so the bar reads as continuous and the name reads as belonging to that
// block rather than as a gap in it.
//
// Under NO_COLOR there is no reverse video to use, so the label falls back to
// standing in the fill -- still legible, still in the right segment, which is
// what the label is for.
func labelledSegment(w io.Writer, glyph, name string, n int, colour string, label bool) string {
	if !label {
		return paint(w, colour, strings.Repeat(glyph, n))
	}
	pad := n - len(name)
	left := pad / 2
	if !enabled(w) {
		return strings.Repeat(glyph, left) + name + strings.Repeat(glyph, pad-left)
	}
	return paint(w, colour, strings.Repeat(glyph, left)) +
		colour + reverse + name + reset +
		paint(w, colour, strings.Repeat(glyph, pad-left))
}

// ejectGlyph is the segment an ejected member draws: filled enough to be
// accounted for, hollow enough not to read as a verdict.
func ejectGlyph() string {
	if !glyphs() {
		return "="
	}
	return "▒"
}

// shortKey is the number a ticket key ends in: OR-223 -> 223.
//
// The prefix identifies the PROJECT, which is the same for every member of a
// batch, so inside a segment it is the one part carrying no information.
func shortKey(key string) string {
	if i := strings.LastIndex(key, "-"); i >= 0 && i+1 < len(key) {
		return key[i+1:]
	}
	return key
}

// isolationRuns estimates how many CI runs the bisection will cost.
//
// About 2*log2(n): the search tests BOTH halves at each level rather than only
// the failing one, because a batch can hold more than one culprit (batch.go).
func isolationRuns(members int) int {
	n := 1
	for i := members; i > 1; i /= 2 {
		n += 2
	}
	return n
}

func renderIsolating(w io.Writer, b *liveBatch, cols int) []string {
	// "red isolating", and how far through the search this is. A binary
	// search over n members costs about 2*log2(n) runs (batch.go), so the
	// estimate is honest arithmetic rather than a guess -- and "run 4 of ~6"
	// is the difference between a search that is progressing and one an
	// operator is about to kill.
	head := fmt.Sprintf("  %s  %s  %s  %s", paint(w, bold, "batch"),
		b.memberBar(w), paint(w, red, "red"), b.phase)
	out := []string{
		head + "   " + Dim(w, fmt.Sprintf("run %d of ~%d", b.runs, isolationRuns(len(b.members)))),
		"",
	}
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
	// A tick on the headline, so the summary reads as a result rather than as
	// one more status line.
	tick := "✓"
	if !glyphs() {
		tick = "+"
	}
	head := fmt.Sprintf("  %s %s  %s",
		paint(w, green, tick), paint(w, bold, "batch"), b.memberBar(w))
	// A batch that landed nothing has no cost line -- zero runs for zero
	// branches is not a saving to report (OR-261). Say what happened instead
	// of trailing off after the bar, which reads as a line that failed to
	// render rather than as a batch that achieved nothing.
	if cost := b.costLine(w); cost != "" {
		head += "  " + cost
	} else {
		head += "  " + Dim(w, "landed nothing")
	}
	// What the isolation actually cost, beside what the batch cost. One run
	// for three branches is the headline; four runs to find the culprit is
	// the price of that headline, and hiding it would be advertising.
	if b.runs > 1 {
		head += "  " + Dim(w, fmt.Sprintf("%s %d runs total with isolation", liveSep, b.runs))
	}
	// A blank line under the headline, in every phase. The headline is a
	// statement about the SET and the rows are its members; run together they
	// read as one list whose first entry happens to be bold.
	out := []string{head, ""}

	for _, m := range b.members {
		g, note := memberGlyph(w, m.state)
		// Elapsed and cost, or an em dash each. An ejected branch never ran,
		// and "0m $0.00" claims it was free work rather than unmeasured.
		el, cost := "—", "—"
		if !glyphs() {
			el, cost = "-", "-"
		}
		if m.elapsed > 0 {
			el = coarse(m.elapsed)
		}
		if m.cost > 0 {
			cost = fmt.Sprintf("$%.2f", m.cost)
		}
		line := fmt.Sprintf("    %s %s  %s  %s  %s",
			g, paint(w, ticketColor(m.key), pad(m.key, liveKeyWidth)),
			pad(string(m.state), 8), pad(el, 5), pad(cost, 6))
		// The detail last: the check that failed, the file that conflicted.
		switch {
		case m.detail != "":
			line += "  " + Dim(w, m.detail)
		case note != "":
			line += "  " + note
		}
		out = append(out, clip(strings.TrimRight(line, " "), cols))
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
