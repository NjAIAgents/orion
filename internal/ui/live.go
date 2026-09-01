package ui

// The live run display: what a watcher shows WHILE its agents work.
//
// OR-217 stopped printing every tool call, which was right -- 60% of the
// lines carried no information. Nothing replaced them, so the default view
// became a stage boundary and then eleven minutes of silence while two agents
// worked hard on two tickets. The only thing that printed was a poll saying
// nothing was happening. An operator reasonably concludes it has hung.
//
// So: a pinned region below the scrollback, redrawn in place four times a
// second, with one row per run. Three elements, and the third is the one that
// earns its place.
//
//	SPINNER    braille per active run. Liveness at a glance, and the cheapest
//	           possible signal that the process has not wedged.
//	BAR        against the MEDIAN for that actor, from this project's own
//	           completed runs. A reference, not a prediction.
//	SPARKLINE  tool calls per 10s over the last two minutes. This is what a
//	           progress bar cannot do: it shows MOMENTUM. A working run
//	           bounces; a stalled one flatlines and the row says "quiet 3m12s".
//	           On 2026-08-30 a run sat in a Read loop for twenty minutes before
//	           its breaker caught it; a flat sparkline shows that in three
//	           seconds.
//
// THE HONESTY RULES ARE THE WHOLE DESIGN, and they are what the code below is
// shaped by rather than decorated with:
//
//   - The bar measures against the MEDIAN. It is never a prediction of
//     completion, because nothing here knows how many turns remain.
//   - Past the median it does NOT creep toward 100%. It fills, stops, and the
//     row says "running long" with the elapsed and the median. p90 for the
//     implementer is 21 minutes against an 11-minute median, so this is the
//     common case and not an edge case.
//   - A flat sparkline is reported as "quiet Xm", because a stalled run and a
//     busy one must never look the same.
//   - No median for an actor means NO BAR. A bar with nothing to measure
//     against would read as "0% done"; blank reads as "not applicable".
//
// THE SCROLLBACK IS ALSO A ZONE (OR-248). A pinned region that is never lost
// is only half the problem: a talkative tick used to push everything the
// operator was reading off the top of the terminal, and a quiet one left the
// screen looking dead. So the terminal is three zones and EXACTLY ONE OF THEM
// SCROLLS -- a frozen window of recent lines, the region, and the header the
// region already carries. The window has a FLOOR of five lines rather than a
// fixed height: it takes whatever rows are left once the region has its own,
// so a taller terminal shows more history and a short one still shows five.
//
// A line that scrolls out of the window is gone from the SCREEN, not from the
// record: events.jsonl has every one of them and `orion logs` prints them.
// Live.Full drops the cap when somebody actually wants to read the log as it
// happens, and off a terminal the cap does not exist at all.
//
// DEGRADATION IS NOT OPTIONAL. A redirected log must stay a log: off a
// terminal there is no cursor control at all, just one plain line per run per
// tick. NO_COLOR keeps the layout and drops the escapes; TERM=dumb is a
// terminal saying it cannot do anything clever, so it gets the plain form
// too. This reuses color.go's existing opt-outs rather than inventing a
// second set -- one place decides what a terminal can do.
//
// THE STATE IS PACKAGE-LEVEL, like console and the ticket colours above it,
// and for the same reason: the facts arrive from three packages that do not
// know about each other. internal/watch dispatches and reaps a run, ui.Stage
// knows which stage and actor now hold it, internal/work sees every tool
// call, and internal/cost knows what each run spent. Threading one handle
// through all four would be a parameter on every function between them.

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/orion-sdlc/orion/internal/actors"
)

// Region geometry. Sized so four concurrent runs plus the header fit an
// 80-column terminal without wrapping, which is the acceptance condition:
// a row that wraps is two rows, and two rows per run is not a table.
const (
	liveKeyWidth     = 8
	liveActorWidth   = 8
	liveStageWidth   = 12
	liveBarWidth     = 14
	liveElapsedWidth = 6
	// liveRuleWidth is the rule that separates the scrollback above from the
	// region below. Narrower than the terminal on purpose: it is a seam, not
	// a banner.
	liveRuleWidth = 76
)

// liveWindowFloor is how many recent lines the frozen window shows.
//
// A HEIGHT, not a floor. OR-248 shipped it as a floor that grew into whatever
// the terminal had spare, on the reasoning that a taller screen may as well
// show more history. That reasoning was never tested against a real terminal:
// terminalRows read LINES, which no shell exports, so the height was always
// unknown and the window was pinned to five by accident. It looked like the
// mockup because it could not do anything else.
//
// Fixing the height detection made it grow for the first time -- to
// twenty-four lines on a full-screen terminal -- and the answer to what it
// SHOULD be is the mockup: "five lines of scrollback, then a wall". A window
// that expands to fill the screen is the unbounded log this feature exists to
// bound; the space belongs to the region, which is the part being watched
// (OR-264).
//
// Five is also still the smallest number that reads as a log rather than as a
// status line: with one you cannot see that a second thing happened, and with
// none the watcher looks hung, which is the failure OR-217 shipped and OR-240
// was written to undo. It shrinks below five only when the region itself
// would not otherwise fit.
const liveWindowFloor = 5

// liveWindowCap is the most lines the window will show, set from the
// watcher's max_concurrent_tickets.
//
// The window's job is to show what the agents are saying, so the volume it
// has to keep up with is proportional to how many are talking. Five lines is
// right for one or two agents and starves at eight, where a single ticket's
// output can push the other seven off the window between glances.
//
// Zero means "not set", which falls back to the floor. Package-level for the
// same reason the rest of this file's state is: the number lives in the
// watcher's config and the renderer is three packages away from it.
var liveWindowCap int

// LiveWindowCap sets the visible window's maximum. Called once by the
// watcher, which is the only thing that knows the concurrency limit.
func LiveWindowCap(n int) {
	live.mu.Lock()
	defer live.mu.Unlock()
	liveWindowCap = n
}

// windowHeight is how many lines the window may show: at least the floor, at
// most the concurrency cap.
func windowHeight() int {
	live.mu.Lock()
	n := liveWindowCap
	live.mu.Unlock()
	if n < liveWindowFloor {
		return liveWindowFloor
	}
	return n
}

// liveWindowBuffer is how many recent lines are RETAINED, as opposed to shown.
//
// Larger than the visible cap so a window that shrank while the region was
// tall can fill again when it is not, and bounded so a watcher left running
// overnight does not accumulate a line per tool call forever. The complete
// record is events.jsonl either way.
const liveWindowBuffer = 200

// The sparkline's resolution: tool calls per 10s over the last two minutes.
//
// Ten seconds because that is short enough for a burst of edits to show as a
// spike and long enough that one slow Bash call does not read as a stall;
// twelve of them because two minutes is about how long a person will watch a
// row before deciding it is stuck.
const (
	sparkBuckets = 12
	sparkBucket  = 10 * time.Second
)

// quietAfter is how long without a tool call before a run is reported quiet.
//
// A minute, matching the acceptance criterion. Short enough to catch a wedged
// run while somebody is still watching, long enough that a single long test
// run does not cry wolf.
const quietAfter = time.Minute

// liveRedraw is how often the region is redrawn in place.
//
// The watcher's own tick is 60s and cannot serve this: a spinner that moves
// once a minute is not a liveness signal, it is a clock. So the region owns
// its own timer, which is the whole reason this is not bolted onto the poll
// loop.
const liveRedraw = 250 * time.Millisecond

// liveRun is one dispatched ticket's live state.
//
// Everything here is derived from facts that already exist somewhere in
// Orion. Nothing in this file starts, stops, or measures work; it only
// renders what the packages doing the work already report.
type liveRun struct {
	key   string
	actor string // actor id, resolved to a name at render time
	stage string
	// started is when this watcher dispatched the ticket, not when the
	// tracker first saw it. Elapsed on this row means "how long has THIS
	// process been working it", which is the question a person watching the
	// terminal is asking.
	started time.Time
	// last is the most recent tool call. Zero means the run has not made one
	// yet, in which case quiet is measured from started -- a run that has
	// never done anything is the most suspicious kind of quiet, not the least.
	last  time.Time
	calls int
	// buckets is a ring of tool-call counts, newest at index newest%len.
	// A ring rather than a slice of timestamps: a busy run makes hundreds of
	// calls a minute and this must cost the same at any rate.
	newest  int64
	buckets [sparkBuckets]int
	// median is the actor's median completed-run duration in this project,
	// resolved when the actor becomes known. Zero means unknown, which is
	// rendered as no bar at all rather than as an empty one.
	median time.Duration
	// barColor overrides the bar's colour. Empty means green -- progress. A
	// culprit sets red, so the branch that broke the batch reads as broken in
	// the bar as well as in the word beside it.
	barColor string
}

// live is the registry. Guarded by its own mutex because a watcher writes to
// it from every job goroutine and reads from the redraw timer.
var live struct {
	mu    sync.Mutex
	runs  map[string]*liveRun
	spend float64
	ci    int
	// median answers "how long does this actor's run usually take in this
	// project". A seam rather than a call into internal/cost, because cost
	// already imports this package to render its report: the dependency can
	// only point one way, and it points this way.
	//
	// Nil means no medians are available, which every row then reports by
	// showing no bar. That is also what a fresh project looks like, and it is
	// the honest rendering of both.
	median func(actor string) time.Duration
	// batch is the batch in flight, or nil. One at a time: a batch is
	// assembled from one pass in one repository's sandbox, so a second would
	// mean two passes racing on one clone -- which collect does not do.
	batch *liveBatch
	// since is when this watcher started, set on the first LiveStart.
	since time.Time
	// checks is the current CI run's individual checks, newest reading wins.
	//
	// ci above is the COUNT of tickets waiting; this is what the one shared
	// run is actually doing. During a batch that distinction is the whole
	// point: three tickets share one run, and "2 in CI" says nothing about
	// which of the three platforms is still going (OR-264).
	checks []Check
}

// Check is one CI check and where it got to, as the display needs it.
//
// Declared here rather than imported from internal/collect because that
// package already depends on this one for rendering; the dependency can only
// point one way. Three states, because that is what a reader acts on.
type Check struct {
	Name  string
	State string
}

// The check states, matching internal/collect's vocabulary.
const (
	CheckPassed  = "passed"
	CheckFailed  = "failed"
	CheckRunning = "running"
)

// LiveChecks records what the current CI run's checks are doing. Replaces
// the previous reading rather than merging: a rollup is a complete picture
// of one moment, and merging would leave a finished check on screen after
// a re-run dropped it.
func LiveChecks(c []Check) {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.checks = append([]Check(nil), c...)
}

// LiveMedians installs the median lookup. Called once by the watcher, which
// is the only thing that knows which projects are in scope.
func LiveMedians(f func(actor string) time.Duration) {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.median = f
}

// LiveStart registers a dispatched ticket.
//
// Called at DISPATCH rather than at the first stage boundary, so the row
// appears the moment the slot is taken. A ticket that is claimed and then
// spends forty seconds provisioning a worktree is exactly the window this
// display exists to fill.
func LiveStart(key string) { liveStart(key, time.Now()) }

func liveStart(key string, at time.Time) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.runs == nil {
		live.runs = map[string]*liveRun{}
	}
	// The session clock starts at the first dispatch rather than at process
	// start: what the header reports is how long this watcher has been
	// WORKING, and the seconds spent reading config are not that.
	if live.since.IsZero() {
		live.since = at
	}
	live.runs[key] = &liveRun{
		key: key, stage: "starting", started: at,
		newest: at.UnixNano() / int64(sparkBucket),
	}
}

// LiveEnd removes a run. Called when the watcher reaps it.
func LiveEnd(key string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	delete(live.runs, key)
}

// LiveActivity records one tool call.
//
// The actor rides along because supervisor.Activity is per-run and the run
// knows who is making the call; a row whose actor only updated at stage
// boundaries would attribute a fix-loop re-entry to whoever held the previous
// stage.
func LiveActivity(key, actor string) { liveActivity(key, actor, time.Now()) }

func liveActivity(key, actor string, at time.Time) {
	live.mu.Lock()
	defer live.mu.Unlock()
	r := live.runs[key]
	if r == nil {
		return
	}
	r.setActorLocked(actor)
	r.advance(at)
	r.buckets[bucketIndex(r.newest)]++
	r.calls++
	r.last = at
}

// liveStage records that a run crossed into a new stage. Called by Stage, so
// the region cannot show a stage the boundary line did not also print.
func liveStage(key, stage, actor string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	r := live.runs[key]
	if r == nil {
		return
	}
	r.stage = stage
	r.setActorLocked(actor)
}

// setActorLocked changes the acting actor and re-resolves its median.
//
// Re-resolved on CHANGE rather than on every redraw: the lookup reads the
// usage history off disk, a run changes actor a handful of times, and the
// region redraws four times a second.
func (r *liveRun) setActorLocked(actor string) {
	if actor == "" || actor == r.actor {
		return
	}
	r.actor = actor
	r.median = 0
	if live.median != nil {
		r.median = live.median(actor)
	}
}

// LiveSpend adds one finished run's cost to the session total.
//
// Pushed by internal/cost as each run is recorded rather than pulled by
// re-reading the usage history: the history is append-only and unbounded, and
// re-summing it four times a second to print a dollar figure would be the
// most expensive thing on the screen.
func LiveSpend(usd float64) {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.spend += usd
}

// LiveCI records how many tickets are waiting on CI. A count, not rows: the
// header can state it exactly, and a row would have to invent an elapsed for
// work this process did not start.
func LiveCI(n int) {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.ci = n
}

// LiveReset clears the registry. For tests, and for a second watcher in one
// process, which nothing does today and which would otherwise inherit the
// first one's rows.
func LiveReset() {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.runs, live.spend, live.ci, live.median = nil, 0, 0, nil
	live.batch, live.checks, live.since = nil, nil, time.Time{}
}

// liveState is one consistent read of the registry: rows in key order, plus
// the header's two counters.
//
// A snapshot, deliberately. Rendering holds no lock, so a job goroutine
// reporting a tool call can never block on the terminal, and a row can never
// change under the renderer halfway down the region.
type liveState struct {
	rows  []liveRun
	spend float64
	ci    int
	// batch is a COPY, taken under the same lock as the rows, so the block and
	// the rows below it describe the same instant. A pointer into the registry
	// would let a member resolve between drawing the batch and drawing the
	// ticket it belongs to.
	batch *liveBatch
	// checks is copied for the same reason the batch is.
	checks []Check
	// since is when this watcher started, for the header's elapsed.
	since time.Time
}

func liveSnapshot() liveState {
	live.mu.Lock()
	defer live.mu.Unlock()
	st := liveState{spend: live.spend, ci: live.ci, since: live.since}
	st.checks = append([]Check(nil), live.checks...)
	if live.batch != nil {
		st.batch = live.batch.snapshotLocked()
	}
	for _, r := range live.runs {
		st.rows = append(st.rows, *r)
	}
	// By key, so a row does not move between redraws. Map order would make
	// four rows shuffle four times a second, which is unreadable however
	// correct each row is.
	sort.Slice(st.rows, func(i, j int) bool { return st.rows[i].key < st.rows[j].key })
	return st
}

// advance rolls the sparkline ring forward to the bucket containing at,
// zeroing every bucket that was skipped.
//
// Zeroing the gap is what makes a stall VISIBLE. Without it a run that
// stopped calling tools would keep showing whatever its last burst wrote,
// which is precisely the "busy and stalled look identical" failure the
// sparkline exists to prevent.
func (r *liveRun) advance(at time.Time) {
	idx := at.UnixNano() / int64(sparkBucket)
	if idx <= r.newest {
		return
	}
	if idx-r.newest >= sparkBuckets {
		r.buckets = [sparkBuckets]int{}
		r.newest = idx
		return
	}
	for i := r.newest + 1; i <= idx; i++ {
		r.buckets[bucketIndex(i)] = 0
	}
	r.newest = idx
}

// bucketIndex maps an absolute bucket number onto the ring. Go's % keeps the
// sign of its left operand, so a negative index -- a zero time, a clock
// before 1970 -- would panic on the array. Normalised rather than assumed.
func bucketIndex(i int64) int {
	n := int64(sparkBuckets)
	return int(((i % n) + n) % n)
}

// The glyph sets. Every one of them has an ASCII form, because glyphs() says
// no on a non-UTF-8 locale, under NO_COLOR and on TERM=dumb -- and a row of
// mojibake is worse than no row.
const (
	spinnerGlyphs = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	spinnerASCII  = `|/-\`
	sparkGlyphs   = "▁▂▃▄▅▆▇█"
	sparkASCII    = "_.-=+*#%"
	// A BLOCK bar, not a rule. The mockup draws a filled band against a
	// stippled remainder, which reads as a quantity at a glance where a thin
	// line reads as a divider -- and the region already has two real rules in
	// it, so a bar shaped like one competes with them.
	barFullGlyph  = "█"
	barHeadGlyph  = "▓"
	barEmptyGlyph = "░"
	barFullASCII  = "#"
	barHeadASCII  = ">"
	barEmptyASCII = "-"
	liveRuleGlyph = "─"
	liveRuleASCII = "-"
)

// spinner picks the frame for this instant.
//
// Derived from the CLOCK rather than from a counter, so the frame is a
// function of when the region was drawn and a test can assert on it without
// running a timer. Every row advances together, which reads as one process
// working rather than as several unrelated animations.
func spinner(now time.Time) string {
	frames := []rune(spinnerGlyphs)
	if !glyphs() {
		frames = []rune(spinnerASCII)
	}
	i := now.UnixNano() / int64(liveRedraw)
	return string(frames[int(((i%int64(len(frames)))+int64(len(frames)))%int64(len(frames)))])
}

// sparkline renders the ring oldest-to-newest.
//
// An empty bucket is the LOWEST glyph rather than a space: a flat line is the
// signal, and a gap would read as missing data. Scaled to the busiest bucket
// in the window, because what a reader wants from this is the SHAPE -- the
// absolute rate is already in the call count next to it.
func (r liveRun) sparkline() string {
	ramp := []rune(sparkGlyphs)
	if !glyphs() {
		ramp = []rune(sparkASCII)
	}
	max := 0
	for _, n := range r.buckets {
		if n > max {
			max = n
		}
	}
	var b strings.Builder
	for k := sparkBuckets - 1; k >= 0; k-- {
		n := r.buckets[bucketIndex(r.newest-int64(k))]
		level := 0
		if max > 0 && n > 0 {
			// Any activity at all reaches at least the second glyph, so one
			// call in a quiet window is visibly different from none.
			level = 1 + (n-1)*(len(ramp)-2)/max
		}
		b.WriteRune(ramp[level])
	}
	return b.String()
}

// bar renders elapsed against the actor's median.
//
// The two rules that matter are both here. An unknown median draws NOTHING,
// and a run past its median FILLS AND STOPS rather than creeping toward a
// completion nothing can predict.
func (r liveRun) bar(w io.Writer, elapsed time.Duration) string {
	if r.median <= 0 {
		return strings.Repeat(" ", liveBarWidth)
	}
	full, head, empty := barFullGlyph, barHeadGlyph, barEmptyGlyph
	if !glyphs() {
		full, head, empty = barFullASCII, barHeadASCII, barEmptyASCII
	}
	n := int(float64(liveBarWidth) * float64(elapsed) / float64(r.median))
	if n < 0 {
		n = 0
	}
	// Green for progress, matching the mockup and the ok/failed palette the
	// rest of the display already uses. barColor lets a culprit's row draw
	// its bar red, so the one branch that broke the batch is red in the
	// glyph, the word and the bar rather than only in the word.
	c := r.barColor
	if c == "" {
		c = green
	}
	if n >= liveBarWidth {
		return paint(w, c, strings.Repeat(full, liveBarWidth))
	}
	return paint(w, c, strings.Repeat(full, n)+head) +
		Dim(w, strings.Repeat(empty, liveBarWidth-n-1))
}

// notes are the sentences a bar can never carry.
//
// Both are stated in WORDS, per OR-163: a row whose meaning lives in the
// shape of a bar is a row that says nothing in a screenshot, in a NO_COLOR
// terminal, or to somebody reading it aloud.
func (r liveRun) notes(now time.Time) []string {
	var out []string
	elapsed := now.Sub(r.started)
	// A bar with no median draws nothing, which is right -- inventing a
	// baseline is what OR-250 forbids -- and silent, which is not. Fourteen
	// blank columns where every other row has a bar reads as a display that
	// was never built rather than as a measurement that cannot honestly be
	// made yet, and it was read exactly that way.
	//
	// So the absence is stated, in words, per OR-163. The vocabulary is
	// batchcost.go's, deliberately: the same idea should read the same way
	// wherever a baseline is missing.
	if r.median <= 0 {
		out = append(out, "no baseline yet")
	}
	if r.median > 0 && elapsed > r.median {
		out = append(out, "running long "+liveSep+" median "+coarse(r.median))
	}
	since := r.last
	if since.IsZero() {
		since = r.started
	}
	if d := now.Sub(since); d >= quietAfter {
		out = append(out, "quiet "+elapsedString(d))
	}
	return out
}

// liveSep is the middle dot the roster already uses to join two facts on one
// line. One separator, so the display reads as one system.
const liveSep = "·"

// renderChecks is the individual checks of the ONE shared CI run.
//
// The count in the header answers "how many tickets are waiting"; this
// answers "what is that run actually doing". During a batch the two are very
// different questions -- three tickets share one run, so a count says nothing
// about which platform is still going, and "still running" for nine minutes
// with no way to see that it is only Windows is the thing an operator sits
// and wonders about.
//
// One line, not a row each: they belong to a single run, and stacking them
// would push the ticket rows off a short terminal to say what fits in eighty
// columns.
func renderChecks(w io.Writer, checks []Check, cols int) string {
	if len(checks) == 0 {
		return ""
	}
	ascii := !glyphs()
	var cells []string
	for _, c := range checks {
		var mark string
		switch c.State {
		case CheckFailed:
			g := "✗"
			if ascii {
				g = "x"
			}
			mark = paint(w, red, g)
		case CheckRunning:
			// The same braille the ticket rows use, so "still going" reads
			// identically wherever it appears.
			mark = spinner(time.Now())
		default:
			g := "✓"
			if ascii {
				g = "+"
			}
			mark = paint(w, green, g)
		}
		cells = append(cells, fmt.Sprintf("%s %s", c.Name, mark))
	}
	return clip("    "+strings.Join(cells, "   "), cols)
}

// renderRow is one run's line.
//
// Columns are dropped RIGHT TO LEFT when the terminal is too narrow --
// sparkline, then bar, then actor -- because a row that wraps has stopped
// being a row, and two rows per run is not a table.
//
// THE ONE THING THAT IS CLIPPED is the actor's name, and only when it is
// longer than its column. Everywhere else in this renderer metadata is left
// to push the message right rather than be cut (see event.go), and that is
// right for a log line, which wraps harmlessly and is read afterwards. It is
// wrong here: this region is redrawn in place, and a row that wrapped would
// desynchronise the erase from what is on screen and corrupt the scrollback
// above it. The name is also the one column already stated in full on every
// scrollback line, so clipping it here loses nothing that is not two lines up.
func renderRow(w io.Writer, r liveRun, now time.Time, cols int) string {
	elapsed := now.Sub(r.started)
	who := ""
	if r.actor != "" {
		who = actors.Get(r.actor).Name
		if who == "" {
			who = r.actor
		}
	}

	var b strings.Builder
	used := 0
	add := func(s string, cells int) {
		b.WriteString(s)
		used += cells
	}

	add(" "+paint(w, cyan, spinner(now))+"  ", 4)
	// The key is NOT clipped: it is the row's identifier, and an over-long one
	// pushes the row wide rather than becoming unidentifiable.
	add(paint(w, ticketColor(r.key), pad(r.key, liveKeyWidth))+"  ", fieldCells(r.key, liveKeyWidth)+2)
	if keep(cols, liveActor) {
		add(paint(w, actorColor(r.actor), pad(clip(who, liveActorWidth), liveActorWidth))+"  ",
			liveActorWidth+2)
	}
	add(pad(r.stage, liveStageWidth)+"  ", fieldCells(r.stage, liveStageWidth)+2)
	if keep(cols, liveBar) {
		add(r.bar(w, elapsed)+"  ", liveBarWidth+2)
	}
	// Elapsed AND the median it is measured against, as the mockup has it:
	// "18m / ~24m". The bar shows the ratio and the numbers say what the
	// ratio is of -- a bar alone cannot be read aloud, quoted in a ticket, or
	// seen in a NO_COLOR terminal (OR-163).
	el := elapsedString(elapsed)
	if r.median > 0 && keep(cols, liveMedian) {
		add(pad(el, liveElapsedWidth)+" "+Dim(w, "/ ~"+coarse(r.median))+"  ",
			liveElapsedWidth+1+3+len(coarse(r.median))+2)
	} else {
		add(pad(el, liveElapsedWidth)+"  ", liveElapsedWidth+2)
	}
	if keep(cols, liveSpark) {
		add(r.sparkline()+"  ", sparkBuckets+2)
	}
	add(fmt.Sprintf("%4d", r.calls), 4)

	// The notes go last and are clipped to whatever is left, because they are
	// the only variable-length thing on the row. Clipped as TEXT, before any
	// colour is applied: cutting a painted string can land inside an escape
	// sequence, which is how a renderer corrupts the terminal it was meant to
	// make readable.
	if notes := r.notes(now); len(notes) > 0 {
		s := strings.Join(notes, " "+liveSep+" ")
		if cols > 0 {
			s = clip(s, cols-used-2)
		}
		if s != "" {
			add("  "+paint(w, yellow, s), 2+utf8.RuneCountInString(s))
		}
	}
	return b.String()
}

// fieldCells is how many columns a padded field actually occupies: its width, or
// the string itself when it overflows.
func fieldCells(s string, width int) int {
	if n := utf8.RuneCountInString(s); n > width {
		return n
	}
	return width
}

// clip shortens a string to n columns, marking that it was cut.
//
// The ellipsis degrades with everything else: on a terminal that cannot show
// one, a full stop says the same thing.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	mark := "…"
	if !glyphs() {
		mark = "."
	}
	return string([]rune(s)[:n-1]) + mark
}

// The optional columns.
const (
	// Surrendered first: the bar already carries this ratio, so the numbers
	// are the cheapest thing to lose and the last thing worth wrapping a row
	// for.
	liveMedian = iota
	liveSpark
	liveBar
	liveActor
)

// keep decides whether an optional column still fits.
//
// Right to left: a column is kept only when everything to its RIGHT that is
// still shown also fits, which is what makes the order of surrender
// sparkline, then bar, then actor rather than the reverse.
//
// An unknown width keeps everything, matching what renderLine does with a
// message it cannot measure: guessing a width and cutting to it is worse than
// a line the terminal lays out itself.
func keep(cols, col int) bool {
	if cols <= 0 {
		return true
	}
	// The fixed part: spinner, key, stage, elapsed, call count and the gaps.
	need := 4 + liveKeyWidth + 2 + liveStageWidth + 2 + liveElapsedWidth + 2 + 4
	// Each optional column costs its own width PLUS everything to its left
	// that must still be present for it to make sense.
	need += liveActorWidth + 2
	if col == liveActor {
		return cols >= need
	}
	need += liveBarWidth + 2
	if col == liveBar {
		return cols >= need
	}
	need += sparkBuckets + 2
	if col == liveSpark {
		return cols >= need
	}
	// "/ ~24m" and its leading space.
	return cols >= need+7
}

// renderHeader is the one line above the rows: when, which project, how much
// is in flight, and what the session has spent.
//
// The spend is here because it is the least deniable liveness signal there
// is. A spinner says a goroutine is alive; a number that goes up says work is
// actually being done.
// hintFor names the key that changes what is on screen right now.
//
// ONE hint, and only when there is something to do with it. Listing both keys
// always would spend header width on an instruction that is wrong half the
// time -- "ctrl-o collapses" is untrue while collapsed -- and a status line
// that says something inaccurate is worse than one that says less. Nothing at
// all when there are no rows: there is nothing to collapse.
func hintFor(st liveState, collapsed bool) string {
	if len(st.rows) == 0 {
		return ""
	}
	if collapsed {
		return "ctrl-o expands"
	}
	return "ctrl-o collapses"
}

func renderHeader(w io.Writer, st liveState, now time.Time) string {
	return renderHeaderAt(w, st, now, false)
}

func renderHeaderAt(w io.Writer, st liveState, now time.Time, collapsed bool) string {
	parts := []string{now.Local().Format("15:04:05")}
	if p := projectsOf(st.rows); p != "" {
		parts = append(parts, p)
	}
	// "3 running", or "idle" when nothing is: a bare "0 running" reads as a
	// counter that failed rather than as a watcher with nothing to do, and
	// the mockup's finished state says idle.
	if len(st.rows) == 0 {
		parts = append(parts, "idle")
	} else {
		parts = append(parts, fmt.Sprintf("%d running", len(st.rows)))
	}
	if st.batch != nil {
		parts = append(parts, "1 batch")
	}
	if st.ci > 0 {
		parts = append(parts, fmt.Sprintf("%d in CI", st.ci))
	}
	if st.spend > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f this session", st.spend))
	}
	// How long this watcher has been up. The mockup's last field, and the one
	// that answers "is this the run I started before lunch" without reading
	// back through the log.
	if !st.since.IsZero() {
		parts = append(parts, coarse(now.Sub(st.since)))
	}
	if h := hintFor(st, collapsed); h != "" {
		parts = append(parts, h)
	}
	return Dim(w, strings.Join(parts, "  "+liveSep+"  "))
}

// projectsOf names the projects on screen, from the keys themselves.
//
// The key's own prefix rather than a registry lookup: this runs four times a
// second and the answer is in the string. A key with no prefix contributes
// nothing rather than a blank entry.
func projectsOf(rows []liveRun) string {
	var seen []string
	for _, r := range rows {
		p, _, ok := strings.Cut(r.key, "-")
		if !ok || p == "" {
			continue
		}
		found := false
		for _, s := range seen {
			if s == p {
				found = true
				break
			}
		}
		if !found {
			seen = append(seen, p)
		}
	}
	return strings.Join(seen, "/")
}

// renderRegion is the whole pinned block: a rule, the header, a blank line,
// then one row per run. Empty when nothing is running, which is what makes a
// tick with nothing to do print nothing at all.
func renderRegion(w io.Writer, st liveState, now time.Time, cols int) []string {
	return renderRegionAt(w, st, now, cols, false)
}

// renderRegionAt is renderRegion with the collapsed state made explicit.
//
// COLLAPSED KEEPS THE HEADER AND THE BATCH, and drops only the per-ticket
// rows. The header is the status line, which is pinned and must never
// disappear -- collapsing to nothing would answer "too many rows" with "no
// information", and the operator's next move would be to expand it again. The
// batch line stays because a batch is one thing however many tickets are in
// it, so it is not what made the region tall.
//
// What is dropped is exactly what grows with max_concurrent_tickets, which is
// the thing being made survivable (OR-249).
func renderRegionAt(w io.Writer, st liveState, now time.Time, cols int, collapsed bool) []string {
	// A batch keeps the region alive with no rows of its own. The members'
	// agents have finished by the time a batch assembles, so their rows are
	// gone -- and a batch waiting up to thirty minutes on one CI run is
	// exactly the silence this region exists to fill.
	if len(st.rows) == 0 && st.batch == nil {
		return nil
	}
	r := liveRuleGlyph
	if !glyphs() {
		r = liveRuleASCII
	}
	out := []string{
		Dim(w, strings.Repeat(r, liveRuleWidth)),
		renderHeaderAt(w, st, now, collapsed),
		"",
	}
	// The batch above the rows: it is the thing the rows are members OF, and
	// reading the set after its members inverts the containment.
	out = append(out, renderBatch(w, st.batch, now, cols)...)
	// Directly under the batch, because it describes that one shared run.
	if line := renderChecks(w, st.checks, cols); line != "" {
		out = append(out, line)
	}
	if collapsed {
		// Said, rather than left to be inferred from rows that vanished. An
		// operator who collapsed the region ten minutes ago and came back to
		// a quiet screen needs to know the runs are hidden, not finished.
		if n := len(st.rows); n > 0 {
			out = append(out, Dim(w, fmt.Sprintf("    %d run(s) hidden · ctrl-o expands", n)))
		}
		return out
	}
	// Two to a line while the batch is TESTING, per the mockup. That phase is
	// the one where a full row per ticket says least: one CI run covers all of
	// them, so the stage, the bar and the sparkline are the same story
	// repeated, and the rows are worth compressing to buy back the vertical
	// space the batch block and its checks now take.
	//
	// Every other phase keeps one row per ticket, because then the tickets
	// really are doing different things.
	if st.batch != nil && st.batch.phase == BatchTesting && pairedFits(cols) {
		return append(out, renderPairedRows(w, st.rows, now, cols)...)
	}
	for _, row := range st.rows {
		out = append(out, renderRow(w, row, now, cols))
	}
	return out
}

// pairedColumnWidth is what one half of a paired line gets.
const pairedColumnWidth = 46

// pairedFits reports whether two compact rows fit side by side.
//
// Below this the pair is worse than the thing it replaces: a wrapped row is
// two rows, and two wrapped rows are four, which is more than the one-per-
// ticket layout it was meant to compress.
func pairedFits(cols int) bool {
	return cols == 0 || cols >= pairedColumnWidth*2
}

// renderPairedRows draws two runs to a line.
//
// The compact cell drops the stage and the call count and keeps what differs
// between two tickets in the same CI run: which ticket, whether it is moving,
// how long it has been alive against its own median.
func renderPairedRows(w io.Writer, rows []liveRun, now time.Time, cols int) []string {
	var out []string
	for i := 0; i < len(rows); i += 2 {
		line := " " + pairedCell(w, rows[i], now)
		if i+1 < len(rows) {
			line += "  " + pairedCell(w, rows[i+1], now)
		}
		// Trailing pad on the last cell is invisible but real: it is columns
		// the erase has to account for, and a line that ends in spaces wraps
		// on a narrow terminal for nothing.
		out = append(out, clip(strings.TrimRight(line, " "), cols))
	}
	return out
}

func pairedCell(w io.Writer, r liveRun, now time.Time) string {
	elapsed := now.Sub(r.started)
	cell := fmt.Sprintf("%s %s %s %s",
		spinner(now),
		paint(w, ticketColor(r.key), pad(r.key, liveKeyWidth)),
		r.bar(w, elapsed),
		pad(elapsedString(elapsed), liveElapsedWidth))
	// Padded on DISPLAY cells, not bytes: paint() wraps the key in escapes
	// that occupy no columns, and padding on length would stagger the second
	// column by however much colour the first one used.
	if n := displayCells(cell); n < pairedColumnWidth {
		cell += strings.Repeat(" ", pairedColumnWidth-n)
	}
	return cell
}

// renderPlain is the off-terminal form: one line per run, per tick.
//
//	23:47  OR-237  implementing  6m02s  84 calls
//
// A redirected log must stay a log. No cursor control, no spinner, no bar --
// none of those mean anything in a file -- and one line per minute rather
// than four per second, because the file is read afterwards and a redraw
// stream is not a record of anything.
func renderPlain(st liveState, now time.Time) []string {
	var out []string
	if line := renderBatchPlain(st.batch, now); line != "" {
		out = append(out, line)
	}
	for _, r := range st.rows {
		line := fmt.Sprintf("%s  %s  %s  %s  %d calls",
			now.Local().Format("15:04"), pad(r.key, liveKeyWidth),
			pad(r.stage, liveStageWidth), elapsedString(now.Sub(r.started)), r.calls)
		if notes := r.notes(now); len(notes) > 0 {
			line += "  " + strings.Join(notes, " "+liveSep+" ")
		}
		out = append(out, line)
	}
	return out
}

// displayCells is how many columns a rendered line occupies once the terminal
// has swallowed its escape sequences.
//
// Runes are not enough here. paint() wraps a field in escapes that occupy no
// columns at all, and counting them would have a coloured line look wider than
// it is -- which, in the arithmetic below, means the window thinks a line
// wrapped when it did not and erases a row that was never drawn.
func displayCells(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// CSI is ESC [ params, ended by a byte in @-~. Anything else is a
			// short escape whose second byte ends it. Either way: skipped whole,
			// never counted, and never cut in half.
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < '@' || s[j] > '~') {
					j++
				}
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
		n++
	}
	return n
}

// screenRows is how many terminal rows a line occupies once it has wrapped.
//
// The erase is a RELATIVE cursor move, so this is not cosmetic: a line counted
// as one row and drawn as two strands a row on screen at every redraw, and the
// strandings accumulate until the region is walking down the terminal. An
// unknown width cannot wrap anything, so it counts one.
func screenRows(s string, cols int) int {
	if cols <= 0 {
		return 1
	}
	if n := displayCells(s); n > cols {
		return (n + cols - 1) / cols
	}
	return 1
}

func screenRowsOf(lines []string, cols int) int {
	n := 0
	for _, s := range lines {
		n += screenRows(s, cols)
	}
	return n
}

// windowLines picks the newest recent lines that fit above the region.
//
// The order of the two rules is the whole function. The FLOOR is taken first,
// so five lines are shown whatever the region costs; only past the floor does
// the height of the terminal get a say, and then a line is kept only while the
// rows it needs are rows the region does not. A terminal that has not told us
// its height gets the floor: guessing tall enough would be guessing the region
// off the top of the screen, which is the failure this exists to prevent.
// windowFrameRows is what the frame costs: one rule above, one below.
const windowFrameRows = 2

// windowFrame draws the labelled rules that bound the frozen window.
//
// The frame is not decoration. Three zones share the screen and exactly one
// of them scrolls (OR-248); without a boundary the scrolling zone and the
// pinned rows below it read as one continuous log, and the whole point is
// that they behave differently. The labels say which is which and how much
// history is being kept, so a line vanishing off the top is explained rather
// than merely noticed.
func windowFrame(w io.Writer, n, cols int) (string, string) {
	tl, tr, bl, br, h := "╭", "╮", "╰", "╯", "─"
	if !glyphs() {
		tl, tr, bl, br, h = "+", "+", "+", "+", "-"
	}
	width := liveRuleWidth
	if cols > 0 && cols-1 < width {
		width = cols - 1
	}

	label := func(left, right, text string) string {
		// text sits at the right end, as the mockup has it: the left corner
		// is the anchor the eye follows down the screen.
		body := " " + text + " "
		fill := width - 2 - utf8.RuneCountInString(body)
		if fill < 0 {
			return Dim(w, left+strings.Repeat(h, max0(width-2))+right)
		}
		return Dim(w, left+strings.Repeat(h, fill)+body+right)
	}
	return label(tl, tr, fmt.Sprintf("recent %d line(s)", n)),
		label(bl, br, "scrolls, then gone")
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func windowLines(recent []string, regionRows, termRows, cols int) []string {
	// One row for the cursor itself, which sits below everything drawn.
	avail := termRows - regionRows - 1

	// THE REGION OUTRANKS THE FLOOR. The floor keeps five lines of history so
	// the screen does not look dead, but on a terminal too short for both, an
	// unconditional floor spends rows the region needs and the ticket rows go
	// off the top -- the header still saying "3 running" over the empty space
	// where they were. History is a convenience; the rows are the thing being
	// watched, so the window yields (OR-264).
	floor := windowHeight()
	if termRows > 0 && avail < floor {
		floor = avail
	}
	if floor < 0 {
		floor = 0
	}

	start, rows := len(recent), 0
	for i := len(recent) - 1; i >= 0; i-- {
		r := screenRows(recent[i], cols)
		// The cap is the FIRST condition, so a roomy terminal does not grow
		// the window into the space the region should have. Past it the
		// available-rows test still applies, which is what makes a short
		// terminal shrink below the cap rather than overflow.
		if len(recent)-i > floor {
			break
		}
		if termRows > 0 && rows+r > avail {
			break
		}
		rows += r
		start = i
	}
	return recent[start:]
}

// elapsedString is a duration a person reads at a glance: 42s, 6m02s, 1h04m.
//
// Fixed shape per magnitude so a column of them stays a column. Seconds are
// zero-padded for the same reason -- 6m2s and 6m12s would not line up.
func elapsedString(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// coarse is a duration used as a REFERENCE rather than as a measurement --
// the median in "running long · median 11m". Rounded, because a median
// printed to the second claims a precision a sample of a dozen runs does not
// have.
func coarse(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
}

// Live is the writer that owns the bottom of the terminal.
//
// EVERY line the watcher prints goes through it, which is the only way the
// screen can be three zones at all: a write goes into the frozen window rather
// than into the terminal's own scrollback, and the whole block -- window then
// region -- is erased and redrawn as one. Anything writing to the terminal
// around this would be overdrawn by the next redraw and lost.
//
// Off a terminal it is a pass-through and nothing else, so a redirected log is
// a complete, greppable, uncapped record. That is not a fallback bolted on
// afterwards -- it is the same object with cursor control switched off, so
// there is no second code path to keep honest.
type Live struct {
	// lock guards drawn, pending, the window and every write to w, so a job
	// goroutine's line can never land between the erase and the redraw of a row.
	lock    sync.Mutex
	w       io.Writer
	cursor  bool
	drawn   int  // ROWS drawn, not lines: a wrapped line is two rows to erase
	pending bool // last write did not end a line, so hold the redraw
	// window is the frozen scrollback: the recent lines still on screen, oldest
	// first. Trimmed to what was actually shown at every redraw, so it is
	// bounded by the terminal rather than by the length of the run.
	window []string
	// partial is a line a caller has begun and not yet ended. Held rather than
	// shown, so a half-written line never becomes a window entry that a later
	// write has to amend.
	partial string
	// full means the cap has been dropped and the log prints in full, straight
	// into the terminal's own scrollback -- the behaviour every caller had
	// before the window existed.
	full bool
	// collapsed hides the per-ticket rows, leaving the batch line and the
	// status line (OR-249). A DISPLAY state and nothing more: events.jsonl
	// records identically either way, which is the same contract the window
	// keeps -- what scrolls out is unshown, never unrecorded.
	collapsed bool
	done      chan struct{}
	wg        sync.WaitGroup
}

// NewLive wraps a writer. Cursor control is used only when the destination is
// a terminal that can take it.
func NewLive(w io.Writer) *Live {
	l := &Live{w: w, cursor: cursorControl(w)}
	if l.cursor {
		l.done = make(chan struct{})
		l.wg.Add(1)
		go l.loop()
		// The cap is only worth dropping where there is a person to drop it, so
		// the key reader starts only when both ends are a terminal.
		if isTerminal(os.Stdin) {
			go l.watchInput(os.Stdin)
		}
	}
	return l
}

// Full drops the cap: from here every line prints in full, into the terminal's
// own scrollback, exactly as it did before the window existed.
//
// The window's own lines are committed on the way out, so nothing that was on
// screen disappears at the moment the cap is dropped. What had already scrolled
// out of the window is NOT reprinted -- it was never held. events.jsonl is the
// complete record and `orion logs` prints it; a buffer of every line a watcher
// emitted over an eight-hour run is not something to keep in memory for a
// keystroke that usually never comes.
//
// One way. Re-capping would leave the operator's screen mid-log with no way to
// tell which lines the window had eaten.
func (l *Live) Full() {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.full || !l.cursor {
		return
	}
	l.eraseLocked()
	l.full = true
	l.commitWindowLocked()
	l.drawLocked()
}

// watchInput drops the cap on the first keystroke.
//
// LINE BUFFERED rather than raw, which is why the key is ctrl-r (or anything
// else) FOLLOWED BY ENTER. Putting a terminal into raw mode takes an ioctl per
// platform; Orion cross-compiles to six of them, has no third-party modules,
// and has no build-tagged file anywhere in the tree. One extra keystroke buys
// all of that back.
//
// Reading the terminal from a backgrounded watcher would normally stop the
// process with SIGTTIN. The Go runtime ignores that signal by default, so the
// read fails instead and this goroutine exits -- `orion watch &` keeps running,
// simply with no key to press.
// Keys the region answers to. Control characters rather than letters, so
// nothing typed at a prompt behind the watcher is mistaken for a command.
const (
	keyCollapse = 0x0F // ctrl-o: collapse the region to its summary, and back
	keyFullLog  = 0x12 // ctrl-r: drop the window's cap and print the full log
)

func (l *Live) watchInput(r io.Reader) {
	var b [64]byte
	for {
		n, err := r.Read(b[:])
		// EVERY byte in the read, not just the first. A terminal delivers a
		// paste or a fast double-press as one read, and answering only b[0]
		// would silently swallow the second key.
		for _, c := range b[:n] {
			switch c {
			case keyCollapse:
				l.ToggleCollapsed()
			case keyFullLog:
				l.Full()
			}
		}
		if err != nil {
			return
		}
	}
}

// ToggleCollapsed switches the region between every ticket row and a summary.
//
// THE RELEASE VALVE FOR A LONG QUEUE. OR-246 removed the ceiling on
// max_concurrent_tickets and asks above ten instead; at ten the pinned rows
// are taller than many terminals and there is no way to shrink them. Expanded
// stays the default -- "you should never have to ask to see what is running"
// -- and this is the other half of that rule, which shipped without it.
//
// A TOGGLE, not a one-way door. The key handling this replaces dropped the
// window's cap on ANY keystroke and could not be undone: a stray arrow key
// ended the frozen window for the rest of the run. A control the operator
// cannot reverse is one they learn not to touch.
func (l *Live) ToggleCollapsed() {
	l.lock.Lock()
	defer l.lock.Unlock()
	if !l.cursor {
		return
	}
	l.eraseLocked()
	l.collapsed = !l.collapsed
	l.drawLocked()
}

// cursorControl reports whether the destination can be redrawn in place.
//
// Two conditions, both from color.go's existing rules so there is one answer
// to "what can this terminal do". A non-terminal is a file or a pipe, and
// escape codes in one are corruption a reader has to strip back out.
// TERM=dumb is a terminal SAYING it cannot render anything clever, and taking
// it at its word is the whole point of honouring it.
//
// NO_COLOR is deliberately NOT here. It asks for no colour, not for no
// layout, so the region stays pinned and loses its escapes and its glyphs.
func cursorControl(w io.Writer) bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(w)
}

// Write puts a line into the frozen window without disturbing the region.
//
// Off a terminal, and once the cap has been dropped, it is the pass-through it
// always was and the terminal owns the scrollback. Otherwise the line never
// reaches the terminal directly at all: it is captured, and the window redraws
// with it. That is what bounds it -- a line the writer emitted itself could
// only be taken back by scrolling the screen, which is the thing being stopped.
func (l *Live) Write(p []byte) (int, error) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if !l.cursor {
		return l.w.Write(p)
	}
	l.eraseLocked()
	n, err := len(p), error(nil)
	if l.full {
		n, err = l.w.Write(p)
	} else {
		l.capture(string(p))
	}
	// A write that did not finish its line leaves the cursor mid-row, and
	// drawing the region there would splice the two together. Held until the
	// line is closed, which every caller in this codebase does with Fprintln.
	l.pending = len(p) > 0 && p[len(p)-1] != '\n'
	l.drawLocked()
	return n, err
}

// capture folds a write into the window, one entry per COMPLETED line.
//
// Split here rather than at the caller because a writer is a byte stream: one
// Write can carry three lines or half of one, and a window that counted writes
// instead of lines would cap at five of whichever it happened to get.
func (l *Live) capture(s string) {
	l.partial += s
	for {
		i := strings.IndexByte(l.partial, '\n')
		if i < 0 {
			return
		}
		l.window = append(l.window, strings.TrimSuffix(l.partial[:i], "\r"))
		l.partial = l.partial[i+1:]
	}
}

// commitWindowLocked prints the window into the terminal's real scrollback and
// forgets it, so the lines that were on screen stay on screen.
func (l *Live) commitWindowLocked() {
	for _, line := range l.window {
		fmt.Fprintln(l.w, line)
	}
	l.window = nil
}

// Tick is the off-terminal heartbeat: one plain line per run.
//
// Called once per watcher tick rather than on a timer, because the file this
// lands in is read afterwards and four lines a second would bury the run it
// is describing. On a terminal it does nothing at all -- the region is
// already saying it, continuously.
func (l *Live) Tick() {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.cursor {
		return
	}
	for _, line := range renderPlain(liveSnapshot(), time.Now()) {
		fmt.Fprintln(l.w, line)
	}
}

// Close stops the redraw and clears the region, leaving the scrollback as the
// only thing on screen. Safe to call twice.
//
// The window is COMMITTED rather than erased with the region. Its lines are the
// only ones the terminal has not seen -- erasing them too would end a watch run
// on a blank screen, which is a worse answer to "what just happened" than the
// five lines that were there a moment ago.
func (l *Live) Close() {
	if l.done != nil {
		close(l.done)
		l.wg.Wait()
		l.done = nil
	}
	l.lock.Lock()
	defer l.lock.Unlock()
	l.eraseLocked()
	l.commitWindowLocked()
	l.commitSummaryLocked()
}

// commitSummaryLocked writes the finished batch into real scrollback.
//
// The region is erased on the way out, and everything in it goes with it. For
// the rows that is right -- a run that has ended has nothing left to say, and
// its outcome was already printed as a line. For the BATCH it was wrong: the
// summary is the cost line and what became of each member, which the mockup
// calls "one durable line", and it was being drawn four times a second and
// then wiped. What survived was the scrolling log, which is the one thing that
// does not answer "what did that batch actually do" (OR-264).
//
// Only the done phase, and only once: a batch still assembling or testing has
// no outcome to keep, and committing mid-flight would print a summary that the
// next redraw contradicts.
func (l *Live) commitSummaryLocked() {
	st := liveSnapshot()
	if st.batch == nil || st.batch.phase != BatchDone {
		return
	}
	fmt.Fprintln(l.w)
	for _, line := range renderBatch(l.w, st.batch, time.Now(), columns()) {
		fmt.Fprintln(l.w, line)
	}
}

func (l *Live) loop() {
	defer l.wg.Done()
	t := time.NewTicker(liveRedraw)
	defer t.Stop()
	// The cached terminal size is dropped every so often so a resized window
	// is picked up. A poll rather than SIGWINCH: the signal needs a
	// platform-specific constant on six cross-compile targets, and re-asking
	// stty once a second costs one exec where the alternative costs a
	// build-tagged file per platform. A resize takes at most a second to
	// reflow, which is below what anyone notices while dragging a window.
	const resizeCheck = time.Second
	resize := time.NewTicker(resizeCheck)
	defer resize.Stop()

	for {
		select {
		case <-l.done:
			return
		case <-resize.C:
			invalidateTerminalSize()
		case <-t.C:
			l.lock.Lock()
			l.eraseLocked()
			l.drawLocked()
			l.lock.Unlock()
		}
	}
}

// eraseLocked removes the drawn block, leaving the cursor where it began.
//
// Move up by however many rows were drawn, then clear from there to the end
// of the screen. Clearing to the end rather than line by line is what makes a
// block that SHRANK -- a run finished, the window narrowed -- not leave its
// last row behind. ROWS, not lines: see screenRows.
func (l *Live) eraseLocked() {
	if l.drawn == 0 {
		return
	}
	fmt.Fprintf(l.w, "\x1b[%dA\x1b[0J", l.drawn)
	l.drawn = 0
}

// drawLocked paints the block: the frozen window, then the region under it.
//
// The geometry is read fresh on EVERY redraw rather than cached, which is what
// makes a resize reflow: the next of the four frames a second sizes the window
// to the new terminal, and the erase that precedes it is measured in the rows
// this pass actually drew.
func (l *Live) drawLocked() {
	if l.pending {
		return
	}
	cols := columns()
	region := renderRegionAt(l.w, liveSnapshot(), time.Now(), cols, l.collapsed)
	drawn := 0
	if !l.full {
		// Trimmed to what is shown, so the window is the buffer: a line that
		// scrolled off the screen is dropped here and is thereafter only in
		// events.jsonl, which is exactly what "gone from the screen" means.
		//
		// The frame's own two rows are charged to the region's budget, or the
		// window would claim them and push a ticket row off the bottom -- the
		// one thing the floor exists to prevent.
		// Trimmed for DISPLAY without discarding the buffer. Reassigning
		// l.window here made the trim permanent: a line dropped because the
		// region was tall at that instant could never come back when the
		// region shrank again, so the window only ever lost lines.
		shown := windowLines(l.window, screenRowsOf(region, cols)+windowFrameRows, terminalRows(), cols)
		// The buffer itself is bounded separately, or a long run grows it
		// without limit for lines nothing will ever show again.
		if len(l.window) > liveWindowBuffer {
			l.window = l.window[len(l.window)-liveWindowBuffer:]
		}
		if len(shown) > 0 {
			top, bottom := windowFrame(l.w, len(shown), cols)
			fmt.Fprintln(l.w, top)
			drawn += screenRows(top, cols)
			for _, line := range shown {
				fmt.Fprintln(l.w, line)
				drawn += screenRows(line, cols)
			}
			fmt.Fprintln(l.w, bottom)
			drawn += screenRows(bottom, cols)
		}
	}
	for _, line := range region {
		fmt.Fprintln(l.w, line)
		drawn += screenRows(line, cols)
	}
	l.drawn = drawn
}
