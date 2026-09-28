package ui

// The status board: what a watcher is doing, as one block, printed when it
// changes (OR-544).
//
// WHY A BLOCK AND NOT A LIVE REGION. OR-334 removed the live region because a
// redrawn panel fights the scrollback, breaks in a piped log and is gone the
// moment the terminal closes; "the log is the interface" stands. But the
// region's information went with it, and what replaced it -- a heartbeat line
// per running ticket every 30 seconds, plus the queue's held reasons reprinted
// every minute -- left the answers a person watching needs ("is it moving,
// does anything need me") scattered across a scroll of lines that mostly did
// not change. This is the region's content as ordinary log lines: printed as
// one block, only when something in it changed or a few minutes have passed,
// so it is in the scrollback like everything else and never redrawn.
//
// FED BY THE LIVE HOOKS. Every ui.Live* call site the region used is still in
// place (they became no-ops in livestub.go); they now record into this model
// instead, so the board needs no new plumbing through work, collect or watch.

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
)

// boardEvery is the longest a watcher goes without restating the board while
// work is in flight, so elapsed times stay honest on a quiet screen.
const boardEvery = 3 * time.Minute

type boardJob struct {
	key, title, stage, actor, note string
	started                        time.Time
	// agents counts subagents the run delegated to through its Task/Agent
	// tool; fan is a Go-dispatched fan-out in progress (OR-544).
	agents int
	fan    *boardFan
}

// boardFan is one fan-out: its children and how many run at once.
type boardFan struct {
	children []*boardChild
	limit    int
}

type boardChild struct {
	label string
	state string // pending, running, done, failed
	took  time.Duration
}

type boardMember struct {
	key    string
	state  MemberState
	detail string
}

type boardBatch struct {
	ref     string
	phase   BatchPhase
	members []*boardMember
	started time.Time
	// at is when the batch entered each phase, so the bottom panel can say
	// how long assembling took and how long CI has run (OR-559). A phase it
	// never entered has no entry, and its step shows no time.
	at map[BatchPhase]time.Time
}

var board struct {
	mu     sync.Mutex
	active bool
	// spinning turns the in-progress icon, at frame spin, while the
	// full-screen view draws the board; the plain log keeps it still (OR-551).
	spinning bool
	spin     int
	jobs     map[string]*boardJob
	queue    map[string]int // stage word -> count, from LiveQueue
	held     int
	heldBy   [][2]string // reason groups: keys, reason
	inCI     int
	checks   []Check
	spend    float64
	landed   int
	// median is the usual CI time of a batch, from LiveBatchMedian: what the
	// CI step's bar measures against (OR-559). Zero means no baseline yet,
	// and the step shows only its elapsed time rather than inventing one.
	median  time.Duration
	batch   *boardBatch
	last    string // the last finished batch, one line
	lastOK  bool
	lastAt  time.Time
	needs   []string
	sig     string
	printed time.Time
}

// BoardEnable turns the board on for this process. A watcher does; a single
// `orion work` run does not, and keeps its per-ticket heartbeat instead.
func BoardEnable() {
	board.mu.Lock()
	defer board.mu.Unlock()
	board.active = true
	actors.EnableTeams()
	if board.jobs == nil {
		board.jobs = map[string]*boardJob{}
	}
}

// BoardActive reports whether a watcher's board is carrying the per-ticket
// progress, so the heartbeat can stay quiet.
func BoardActive() bool {
	board.mu.Lock()
	defer board.mu.Unlock()
	return board.active
}

// BoardHeld records how many queued tickets the queue is holding back.
func BoardHeld(n int) {
	board.mu.Lock()
	board.held = n
	board.mu.Unlock()
}

// BoardHeldBy records why they are held, one group per reason, so the board
// can say "12 on LTA-30" instead of the scroll repeating twelve keys.
func BoardHeldBy(groups [][2]string) {
	board.mu.Lock()
	board.heldBy = append([][2]string(nil), groups...)
	board.mu.Unlock()
}

// BoardNeedsYou sets what requires a person, one short sentence each. Empty
// clears it, and the line disappears.
func BoardNeedsYou(items []string) {
	board.mu.Lock()
	board.needs = append([]string(nil), items...)
	board.mu.Unlock()
}

// BoardLanded counts one ticket landed. Called from the watcher's merged
// verdict, which both the per-branch and the batch path report -- so it is
// counted once, there, and not again from LiveBatchMember.
func BoardLanded() {
	board.mu.Lock()
	board.landed++
	board.mu.Unlock()
}

// boardCounts is what the full-screen view's header summarises (OR-559).
type boardCounts struct {
	running, queued, landed, failed int
	spend                           float64
}

// boardSummary reads the header's counts. It takes board.mu itself, so the
// screen asks for it before taking its own lock, never under it.
func boardSummary() boardCounts {
	board.mu.Lock()
	defer board.mu.Unlock()
	return boardCounts{running: len(board.jobs), queued: board.queue["queued"],
		landed: board.landed, failed: board.queue["failed"], spend: board.spend}
}

func boardJobFor(key string) *boardJob {
	if board.jobs == nil {
		board.jobs = map[string]*boardJob{}
	}
	j := board.jobs[key]
	if j == nil {
		j = &boardJob{key: key, started: clock(), stage: "starting"}
		board.jobs[key] = j
	}
	return j
}

func boardStart(key string) {
	board.mu.Lock()
	boardJobFor(key)
	board.mu.Unlock()
}

func boardDone(key string) {
	board.mu.Lock()
	delete(board.jobs, key)
	board.mu.Unlock()
	actors.Release(key)
}

func boardTitle(key, title string) {
	board.mu.Lock()
	boardJobFor(key).title = title
	board.mu.Unlock()
}

func boardNote(key, actor, note string) {
	board.mu.Lock()
	j := boardJobFor(key)
	if actor != "" {
		j.actor = actor
	}
	j.note = note
	board.mu.Unlock()
}

func boardStage(key, stage, actor string) {
	if key == "" {
		return
	}
	board.mu.Lock()
	if j := board.jobs[key]; j != nil {
		j.stage = stage
		if actor != "" {
			j.actor = actor
		}
		j.note, j.fan, j.agents = "", nil, 0
	}
	board.mu.Unlock()
}

func boardAgents(key string) {
	board.mu.Lock()
	boardJobFor(key).agents++
	board.mu.Unlock()
}

// BoardFan records a fan-out starting under a ticket: one label per child, and
// how many run at once. The children appear as a tree under the ticket's row.
func BoardFan(key string, labels []string, limit int) {
	board.mu.Lock()
	defer board.mu.Unlock()
	if !board.active {
		return
	}
	f := &boardFan{limit: limit}
	for _, l := range labels {
		f.children = append(f.children, &boardChild{label: l, state: "pending"})
	}
	boardJobFor(key).fan = f
}

// BoardFanChild moves one child of a ticket's fan-out to a new state.
func BoardFanChild(key string, i int, state string, took time.Duration) {
	board.mu.Lock()
	defer board.mu.Unlock()
	j := board.jobs[key]
	if j == nil || j.fan == nil || i < 0 || i >= len(j.fan.children) {
		return
	}
	j.fan.children[i].state, j.fan.children[i].took = state, took
}

func boardQueue(rows []QueueRow) {
	board.mu.Lock()
	board.queue = map[string]int{}
	for _, r := range rows {
		board.queue[r.Stage]++
	}
	board.mu.Unlock()
}

func boardBatchStart(ref string, members []string) {
	board.mu.Lock()
	b := &boardBatch{ref: ref, phase: BatchAssembling, started: clock(),
		at: map[BatchPhase]time.Time{BatchAssembling: clock()}}
	for _, k := range members {
		b.members = append(b.members, &boardMember{key: k, state: MemberPending})
	}
	board.batch = b
	board.mu.Unlock()
}

func boardBatchPhase(p BatchPhase) {
	board.mu.Lock()
	if b := board.batch; b != nil {
		b.phase = p
		if _, ok := b.at[p]; !ok {
			b.at[p] = clock()
		}
	}
	board.mu.Unlock()
}

func boardBatchMember(key string, s MemberState, detail string) {
	board.mu.Lock()
	defer board.mu.Unlock()
	b := board.batch
	if b == nil {
		return
	}
	for _, m := range b.members {
		if m.key == key {
			m.state, m.detail = s, detail
			return
		}
	}
}

func boardBatchEnd() {
	board.mu.Lock()
	defer board.mu.Unlock()
	b := board.batch
	if b == nil {
		return
	}
	var landed, out []string
	for _, m := range b.members {
		switch m.state {
		case MemberLanded:
			landed = append(landed, m.key)
		case MemberCulprit, MemberEjected:
			out = append(out, m.key+" "+string(m.state))
		}
	}
	switch {
	case len(landed) > 0:
		board.last = "landed " + strings.Join(landed, " ")
		board.lastOK = true
	case len(out) > 0:
		board.last = "landed nothing: " + strings.Join(out, ", ")
		board.lastOK = false
	default:
		board.last = b.ref + " ended without a result"
		board.lastOK = false
	}
	board.lastAt = clock()
	board.batch = nil
}

// BoardTick prints the board if its content changed or boardEvery has passed
// with work in flight. Called once per watcher tick.
func BoardTick(w io.Writer) {
	board.mu.Lock()
	defer board.mu.Unlock()
	// The full-screen view draws the board itself (OR-548).
	if !board.active || ScreenOn() {
		return
	}
	sig := boardSignature()
	busy := len(board.jobs) > 0 || board.batch != nil || board.inCI > 0
	due := busy && clock().Sub(board.printed) >= boardEvery
	if sig == board.sig && !due {
		return
	}
	Reset(w)
	fmt.Fprint(w, renderBoard(w, clock()))
	board.sig, board.printed = sig, clock()
}

// boardSignature is the board without its clocks: what changing means.
func boardSignature() string {
	var b strings.Builder
	for _, k := range sortedJobKeys() {
		j := board.jobs[k]
		fmt.Fprintf(&b, "%s|%s|%s|a%d;", j.key, j.stage, j.actor, j.agents)
		if j.fan != nil {
			for _, c := range j.fan.children {
				b.WriteString(c.state[:1])
			}
		}
	}
	fmt.Fprintf(&b, "q%v|h%d|ci%d|l%d|", board.queue, board.held, board.inCI, board.landed)
	for _, c := range board.checks {
		fmt.Fprintf(&b, "c%s=%s;", c.Name, c.State)
	}
	if bt := board.batch; bt != nil {
		fmt.Fprintf(&b, "B%s|%s;", bt.ref, bt.phase)
		for _, m := range bt.members {
			fmt.Fprintf(&b, "%s=%s;", m.key, m.state)
		}
	}
	fmt.Fprintf(&b, "L%s|N%s", board.last, strings.Join(board.needs, ";"))
	return b.String()
}

func sortedJobKeys() []string {
	keys := make([]string, 0, len(board.jobs))
	for k := range board.jobs {
		keys = append(keys, k)
	}
	// Numerically within a project, so LTA-2 sorts before LTA-117.
	sort.Slice(keys, func(i, j int) bool {
		pi, ni := splitKey(keys[i])
		pj, nj := splitKey(keys[j])
		if pi != pj {
			return pi < pj
		}
		return ni < nj
	})
	return keys
}

func splitKey(k string) (string, int) {
	i := strings.LastIndex(k, "-")
	if i < 0 {
		return k, 0
	}
	n := 0
	fmt.Sscanf(k[i+1:], "%d", &n)
	return k[:i], n
}

// renderBoard draws the block. Caller holds board.mu.
func renderBoard(w io.Writer, now time.Time) string {
	top, bottom := renderBoardParts(w, now, false)
	return top + bottom
}

// The running table's columns (OR-559), in terminal cells. Each ends in at
// least one space, and a value wider than its column pushes the rest of the
// row right rather than being cut -- the same rule the event lines keep. The
// key column is the exception, widened to the longest key on the board, so
// an ORION-1234 among LTA-2s does not put its own row out of line.
const (
	colKey   = 9
	colStage = 14
	colTook  = 8
	colWho   = 27
)

// keyColor and whoColor are the one colour each for a ticket key and an
// agent on the board (OR-559). The event lines keep their per-ticket and
// per-actor identity colours; on the board those made every row a rainbow in
// which the one red thing did not stand out. Here colour means state, and
// the key and the agent are told apart by column, not hue.
const (
	keyColor = sky
	whoColor = amber
)

// stateColor is statusColor for the board (OR-559): the same states, but
// work in progress is amber, not cyan. Cyan sat beside the sky-blue keys and
// read as one more identity colour; amber says "going" next to green done,
// red failed and blue waiting. A warning shares it, and only ever appears on
// the board with its word ("ejected") beside it.
func stateColor(verb string) string {
	if verb == VerbWorking {
		return yellow
	}
	return statusColor(verb)
}

// renderBoardParts renders the board; with split, the batch, CI and last
// result come back separately as the full-screen view's bottom panel
// (OR-557), and the top ends at the running tickets, the queue and NEEDS YOU.
//
// Split is the full-screen view, and there the panel's ground sets sections
// apart, so a blank row stands where the plain log draws a rule, and the
// totals row is gone: the header carries those counts (OR-559).
func renderBoardParts(w io.Writer, now time.Time, split bool) (string, string) {
	// The terminal's own width, so the rules span it; 100 when it cannot be
	// read (a pipe, a log file).
	width := columns()
	if width <= 0 {
		width = 100
	}
	if width > 200 {
		width = 200
	}
	rule := Dim(w, strings.Repeat("─", width))
	if split {
		// A space, not nothing: the frame trims trailing newlines, and the
		// last separator is a row of panel the design keeps.
		rule = " "
	}
	label := func(verb, s string) string {
		return paint(w, stateColor(verb), strings.TrimSpace(boardIcon(verb))+" "+s)
	}
	head := func(s string) string { return sectionChip(w, s) }

	var b strings.Builder
	b.WriteString(rule + "\n")
	renderRunning(&b, w, now)
	if split {
		b.WriteString(rule + "\n")
	}

	// Queue and totals: counts that add up. Waiting is labelled and not yet
	// started; in integration is finished and being batched or in CI.
	q := board.queue
	waiting := q["queued"]
	ready := waiting - board.held
	if ready < 0 {
		ready = 0
	}
	integ := q["ci-wait"] + q["ready"]
	wait := fmt.Sprintf("%d waiting", waiting)
	switch {
	case waiting > 0 && ready == 0:
		wait += " · all blocked"
	case board.held > 0:
		wait += fmt.Sprintf(" · %d ready · %d blocked", ready, board.held)
	}
	fmt.Fprintf(&b, " %s%s%s\n", head("QUEUE"), label(VerbWaiting, wait), heldSummary(w))
	if !split {
		totals := []string{label(VerbWorking, fmt.Sprintf("%d in integration", integ)),
			label(VerbDone, fmt.Sprintf("%d landed", board.landed))}
		if q["failed"] > 0 {
			totals = append(totals, label(VerbFail, fmt.Sprintf("%d failed", q["failed"])))
		}
		totals = append(totals, Dim(w, fmt.Sprintf("$%.2f", board.spend)))
		fmt.Fprintf(&b, " %s%s\n", head(""), strings.Join(totals, Dim(w, " · ")))
	}

	var bottom strings.Builder
	batchTo := &b
	if split {
		batchTo = &bottom
	}
	renderBatch(batchTo, w, now, head)

	// Needs you: only when there is something, and loudest.
	if len(board.needs) > 0 {
		b.WriteString(rule + "\n")
		for i, n := range board.needs {
			h := ""
			if i == 0 {
				h = "NEEDS YOU"
			}
			fmt.Fprintf(&b, " %s%s\n", sectionChip(w, h),
				paint(w, brightMagenta, " "+needsGlyph()+" "+n))
		}
	}
	b.WriteString(rule + "\n")
	return b.String(), bottom.String()
}

// renderRunning writes RUNNING as a table (OR-559): a dim header row on the
// chip's line, then one row per ticket -- icon, key, stage, elapsed, who,
// and what it is doing -- each column starting in the same place on every
// row. Before, each row was a sentence whose words moved with the length of
// the one before, and comparing two tickets' stages meant reading both rows
// to the end. Caller holds board.mu.
func renderRunning(b *strings.Builder, w io.Writer, now time.Time) {
	keys := sortedJobKeys()
	if len(keys) == 0 {
		fmt.Fprintf(b, " %s%s\n", sectionChip(w, "RUNNING"), Dim(w, "nothing"))
		return
	}
	keyW := colKey
	for _, k := range keys {
		if n := visibleCells(k) + 1; n > keyW {
			keyW = n
		}
	}
	fmt.Fprintf(b, " %s%s\n", sectionChip(w, "RUNNING"), Dim(w, strings.Repeat(" ", iconWidth)+
		pad("TICKET", keyW)+pad("STAGE", colStage)+fmt.Sprintf("%*s", colTook, "ELAPSED")+"   "+
		pad("WHO", colWho)+"DOING"))
	for _, k := range keys {
		j := board.jobs[k]
		who := ""
		if j.actor != "" {
			who = actors.DisplayFor(j.key, j.actor)
		}
		verb := VerbWorking
		failed := fanFailed(j.fan)
		if failed {
			verb = VerbFail
		}
		row := paint(w, stateColor(verb), boardIcon(verb)) +
			padCells(paint(w, bold+keyColor, j.key), keyW-1) + " " +
			padCells(j.stage, colStage-1) + " " +
			Dim(w, fmt.Sprintf("%*s", colTook, roundDur(now.Sub(j.started)))) + "   " +
			padCells(paint(w, whoColor, who), colWho-1) + " " + jobDoing(w, j)
		fmt.Fprintf(b, " %s%s\n", sectionChip(w, ""), strings.TrimRight(row, " "))
		// The tree only when a child failed: which one, and what the others
		// are doing, is then the question. Otherwise the bar in DOING says
		// all there is to say, in one row instead of six (OR-559).
		if failed {
			b.WriteString(renderFan(w, j.fan, sectionChip(w, "")))
		}
	}
}

// fanFailed reports whether any child of a fan-out has failed.
func fanFailed(f *boardFan) bool {
	if f == nil {
		return false
	}
	for _, c := range f.children {
		if c.state == "failed" {
			return true
		}
	}
	return false
}

// jobDoing is the DOING cell: a fan-out folded to its label, a count and a
// bar, or the latest activity; then any subagents. Secondary detail, so
// italic. Caller holds board.mu.
func jobDoing(w io.Writer, j *boardJob) string {
	var parts []string
	if f := j.fan; f != nil {
		done, running, failed := 0, 0, 0
		for _, c := range f.children {
			switch c.state {
			case "done":
				done++
			case "failed":
				done++
				failed++
			case "running":
				running++
			}
		}
		s := fmt.Sprintf("%s %d/%d ", fanLabel(j), done, len(f.children)) + fanBar(w, done, len(f.children))
		if failed > 0 {
			s += paint(w, stateColor(VerbFail), fmt.Sprintf("  %d failed", failed))
		} else {
			s += Italic(w, fmt.Sprintf("  %d running", running))
		}
		parts = append(parts, s)
	} else if j.note != "" {
		parts = append(parts, Italic(w, shortenPaths(j.note)))
	}
	if j.agents > 0 {
		parts = append(parts, Italic(w, fmt.Sprintf("%d subagent(s)", j.agents)))
	}
	return strings.Join(parts, Italic(w, " · "))
}

// fanLabel names a fan-out by what its children are: "#3 qa · 4 case(s)"
// is a qa fan. The stage when a label does not have that shape.
func fanLabel(j *boardJob) string {
	if len(j.fan.children) > 0 {
		l := j.fan.children[0].label
		if strings.HasPrefix(l, "#") {
			if _, rest, ok := strings.Cut(l, " "); ok {
				l = rest
			}
		}
		l, _, _ = strings.Cut(l, " · ")
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return j.stage
}

// fanBarMax caps a fan's bar: one cell per child up to here, scaled beyond,
// so a forty-case fan does not push the row off the screen.
const fanBarMax = 12

// fanBar is done out of total as a bar, the done part in the done colour.
func fanBar(w io.Writer, done, total int) string {
	n, fill := total, done
	if total > fanBarMax {
		n, fill = fanBarMax, done*fanBarMax/total
	}
	return progressBar(w, VerbDone, fill, n)
}

// progressBar is fill of n cells in a state's colour, the rest dim.
func progressBar(w io.Writer, verb string, fill, n int) string {
	if fill > n {
		fill = n
	}
	on, off := "▰", "▱"
	if !glyphs() {
		on, off = "#", "."
	}
	return paint(w, stateColor(verb), strings.Repeat(on, fill)) + Dim(w, strings.Repeat(off, n-fill))
}

func needsGlyph() string {
	if glyphs() {
		return "⚑"
	}
	return "?"
}

// batchPipeline is the batch's steps, each with how long it took or has
// taken (OR-559), and the CI step measured against the usual CI time when
// there is one: "CI ◐ 4m of ~6m" and a bar is the answer to "how much
// longer", which the step's name alone never gave. Caller holds board.mu.
func batchPipeline(w io.Writer, bt *boardBatch, now time.Time, failed bool) string {
	steps := []struct {
		name  string
		phase BatchPhase
	}{{"assembling", BatchAssembling}, {"CI", BatchTesting}, {"isolating", BatchIsolating}, {"landing", BatchDone}}
	// took is how long step i ran: until the next phase the batch entered,
	// or until now for the phase it is in. Unknown -- a phase never entered,
	// or a resumed batch whose assembling happened in another process -- is
	// negative, and the step shows no time.
	took := func(i int) time.Duration {
		start, ok := bt.at[steps[i].phase]
		if !ok {
			return -1
		}
		if steps[i].phase == bt.phase {
			return now.Sub(start)
		}
		for _, next := range steps[i+1:] {
			if end, ok := bt.at[next.phase]; ok {
				return end.Sub(start)
			}
		}
		return -1
	}
	elapsed := func(i int) string {
		if d := took(i); d >= 0 {
			return Italic(w, " "+roundDur(d))
		}
		return ""
	}
	reached := true
	var out []string
	for i, s := range steps {
		switch {
		case s.phase == bt.phase && failed && bt.phase == BatchTesting:
			out = append(out, paint(w, stateColor(VerbFail), s.name+" "+strings.TrimSpace(boardIcon(VerbFail)))+elapsed(i))
			reached = false
		case s.phase == bt.phase:
			step := paint(w, stateColor(VerbWorking), s.name+" "+strings.TrimSpace(boardIcon(VerbWorking)))
			if d := took(i); s.phase == BatchTesting && d >= 0 && board.median > 0 {
				const cells = 11
				step += Italic(w, " "+roundDur(d)+" of ~"+roundDur(board.median)+" ") +
					progressBar(w, VerbWorking, int(d*cells/board.median), cells)
			} else {
				step += elapsed(i)
			}
			out = append(out, step)
			reached = false
		case reached:
			out = append(out, paint(w, stateColor(VerbDone), s.name+" "+strings.TrimSpace(boardIcon(VerbDone)))+elapsed(i))
		default:
			out = append(out, Dim(w, s.name))
		}
	}
	return strings.Join(out, Dim(w, "  →  "))
}

func memberWord(w io.Writer, m *boardMember) string {
	switch m.state {
	case MemberLanded:
		return paint(w, stateColor(VerbDone), m.key+" landed")
	case MemberCulprit:
		return paint(w, stateColor(VerbFail), m.key+" culprit"+detailSuffix(m.detail))
	case MemberEjected:
		return paint(w, stateColor(VerbWarn), m.key+" ejected"+detailSuffix(m.detail))
	}
	return ""
}

func detailSuffix(d string) string {
	if d == "" {
		return ""
	}
	return " (" + d + ")"
}

// checkVerb is the board state of a CI check's state.
func checkVerb(state string) string {
	switch state {
	case CheckPassed:
		return VerbDone
	case CheckFailed:
		return VerbFail
	}
	return VerbWorking
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// resetBoard clears everything, for tests.
func resetBoard() {
	board.mu.Lock()
	defer board.mu.Unlock()
	board.active, board.jobs, board.queue = false, nil, nil
	board.held, board.inCI, board.checks, board.spend, board.landed, board.median = 0, 0, nil, 0, 0, 0
	board.batch, board.last, board.lastOK, board.lastAt, board.needs = nil, "", false, time.Time{}, nil
	board.sig, board.printed = "", time.Time{}
	board.spinning, board.spin = false, 0
	actors.ResetTeams()
}

// fanShown is how many children a fan tree lists before summarising the rest.
const fanShown = 6

// renderFan draws a fan-out as a tree under its ticket: since OR-559 only
// when a child has failed, with that child in the failure colour so it is
// the first thing read. Caller holds board.mu.
func renderFan(w io.Writer, f *boardFan, indent string) string {
	branch, last := "├ ", "└ "
	if !glyphs() {
		branch, last = "|- ", "`- "
	}
	running := 0
	for _, c := range f.children {
		if c.state == "running" {
			running++
		}
	}
	var b strings.Builder
	n := len(f.children)
	shown := n
	if shown > fanShown {
		shown = fanShown - 1
	}
	for i := 0; i < shown; i++ {
		c := f.children[i]
		mark := branch
		if i == n-1 {
			mark = last
		}
		verb, name, tail := VerbWorking, c.label, ""
		switch c.state {
		case "done":
			verb, tail = VerbDone, "  "+Italic(w, roundDur(c.took))
		case "failed":
			verb = VerbFail
			name = paint(w, stateColor(VerbFail), c.label)
			tail = "  " + paint(w, stateColor(VerbFail), roundDur(c.took))
		case "pending":
			verb = "pending"
			if f.limit > 0 && running >= f.limit {
				tail = "  " + Italic(w, fmt.Sprintf("queued (%d at a time)", f.limit))
			} else {
				tail = "  " + Italic(w, "queued")
			}
		}
		fmt.Fprintf(&b, " %s      %s%s%s%s\n", indent, Dim(w, mark),
			paint(w, stateColor(verb), boardIcon(verb)), name, tail)
	}
	if shown < n {
		rest := map[string]int{}
		for _, c := range f.children[shown:] {
			rest[c.state]++
		}
		fmt.Fprintf(&b, " %s      %s%s\n", indent, Dim(w, last),
			Dim(w, fmt.Sprintf("%d more: %d done · %d failed · %d running · %d queued",
				n-shown, rest["done"], rest["failed"], rest["running"], rest["pending"])))
	}
	return b.String()
}

// heldSummary is the held groups as one clause: "12 on LTA-30 · 10 on
// LTA-137". Caller holds board.mu.
func heldSummary(w io.Writer) string {
	if len(board.heldBy) == 0 {
		return ""
	}
	var parts []string
	for _, g := range board.heldBy {
		n := len(strings.Split(g[0], ", "))
		reason := g[1]
		if r, ok := strings.CutPrefix(reason, "blocked by "); ok {
			reason = "on " + r
		} else if _, by, ok := strings.Cut(reason, " is already spoken for by "); ok {
			// A file-overlap hold (fanout.scope) names every shared path and
			// a paragraph of why; the board has room for who, not what
			// (OR-553). The full reason stays in the log line.
			by, _, _ = strings.Cut(by, ";")
			reason = "sharing files with " + by
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, reason))
	}
	return Dim(w, ": "+strings.Join(parts, " · "))
}

// shortReason makes an ejection reason fit a row: the conflicting file when
// the git output names one, the reason otherwise.
func shortReason(r string) string {
	for _, marker := range []string{"Merge conflict in ", "CONFLICT (content): Merge conflict in ", "Auto-merging "} {
		if i := strings.LastIndex(r, marker); i >= 0 {
			f := strings.Fields(r[i+len(marker):])
			if len(f) > 0 {
				return "conflict in " + path.Base(strings.TrimRight(f[0], ")."))
			}
		}
	}
	if len(r) > 40 {
		return r[:39] + "…"
	}
	return r
}

// BoardWhere is what a ticket's row says now: its stage and how long it has
// run. Empty when the board holds no row for it -- a stop that names what it
// waits for must not invent a stage (OR-547).
func BoardWhere(key string) (stage string, took time.Duration) {
	board.mu.Lock()
	defer board.mu.Unlock()
	j := board.jobs[key]
	if j == nil {
		return "", 0
	}
	if !j.started.IsZero() {
		took = clock().Sub(j.started)
	}
	return j.stage, took
}

// boardIcon is iconFor, with the in-progress icon turning while the
// full-screen view redraws it (OR-551), as `orion plan`'s live line does.
// Caller holds board.mu.
func boardIcon(verb string) string {
	if verb == VerbWorking && board.spinning {
		set := spinASCII
		if glyphs() {
			set = spinGlyphs
		}
		g := set[board.spin%len(set)]
		return g + strings.Repeat(" ", iconWidth-cells(g))
	}
	return iconFor(verb)
}

// A section's chip is one slate for every section (OR-559), and magenta for
// NEEDS YOU alone. Each section had its own colour (OR-555), and six colours
// competing with the status colours meant none of them stood out; colour now
// means state, and the one chip that is a state -- a person is needed -- is
// the one that keeps a colour. Dark enough for white text on any terminal.
const (
	chipBg  = "\x1b[48;5;60m"
	needsBg = "\x1b[48;5;127m"
)

// sectionChipWidth fits the longest label, NEEDS YOU, with a space each side.
const sectionChipWidth = 11

// sectionChip is a section's label as a filled chip, or blank space of the
// same width on its continuation rows, so every row's content starts in the
// same column. Without colour -- off a terminal, NO_COLOR, the mono theme --
// a chip is the label in brackets, which still reads as a heading and keeps
// the width (OR-559).
// chipMargin sets the section labels in from the screen edge, so a label
// reads as a heading on the panel rather than a tab stuck to its border
// (approved mockup C, 2026-09-28). Every row carries it -- blank-chip rows
// too -- so the columns behind the labels stay aligned.
const chipMargin = "  "

func sectionChip(w io.Writer, s string) string {
	if s == "" {
		return chipMargin + strings.Repeat(" ", sectionChipWidth)
	}
	if !enabled(w) {
		return chipMargin + pad("["+s+"]", sectionChipWidth)
	}
	bg := chipBg
	if s == "NEEDS YOU" {
		bg = needsBg
	}
	return chipMargin + paint(w, bold+"\x1b[97m"+bg, pad(" "+s, sectionChipWidth))
}

// renderBatch writes the batch, its CI checks and the last batch's result.
func renderBatch(b *strings.Builder, w io.Writer, now time.Time, head func(string) string) {
	// Batch and CI.
	failedCheck := false
	for _, c := range board.checks {
		if c.State == CheckFailed {
			failedCheck = true
		}
	}
	if bt := board.batch; bt != nil {
		var in, out []string
		for _, m := range bt.members {
			switch m.state {
			case MemberEjected:
				out = append(out, m.key+" ("+shortReason(m.detail)+")")
			default:
				in = append(in, m.key)
			}
		}
		ref := paint(w, bold, bt.ref)
		if failedCheck {
			ref = paint(w, stateColor(VerbFail), bt.ref+" red")
		}
		fmt.Fprintf(b, " %s%s  %s%s\n", head("BATCH"), ref, paint(w, keyColor, strings.Join(in, " ")),
			Italic(w, "  started "+roundDur(now.Sub(bt.started))+" ago"))
		fmt.Fprintf(b, " %s%s\n", head(""), batchPipeline(w, bt, now, failedCheck))
		var ms []string
		for _, m := range bt.members {
			if m.state == MemberLanded || m.state == MemberCulprit {
				ms = append(ms, memberWord(w, m))
			}
		}
		if len(ms) > 0 {
			fmt.Fprintf(b, " %s%s\n", head(""), strings.Join(ms, "  "))
		}
		if len(out) > 0 {
			fmt.Fprintf(b, " %s%s\n", head(""), paint(w, stateColor(VerbWarn),
				strings.TrimSpace(boardIcon(VerbWarn))+" ejected, next batch: "+strings.Join(out, ", ")))
		}
	}
	// A check's state is its colour and its icon. No durations: a Check
	// carries a name and a state and nothing about when it started, so a
	// time here would be invented (OR-559).
	if len(board.checks) > 0 {
		var cs []string
		for _, c := range board.checks {
			v := checkVerb(c.State)
			cs = append(cs, paint(w, stateColor(v), c.Name+" "+strings.TrimSpace(boardIcon(v))))
		}
		fmt.Fprintf(b, " %s%s\n", head("CI"), strings.Join(cs, "   "))
	}
	if board.last != "" {
		verb := VerbDone
		if !board.lastOK {
			verb = VerbFail
		}
		fmt.Fprintf(b, " %s%s%s\n", head("LAST"),
			paint(w, stateColor(verb), strings.TrimSpace(boardIcon(verb))+" "+board.last),
			Italic(w, " · "+roundDur(now.Sub(board.lastAt))+" ago"))
	}
}
