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
	testing time.Time
}

var board struct {
	mu      sync.Mutex
	active  bool
	jobs    map[string]*boardJob
	queue   map[string]int // stage word -> count, from LiveQueue
	held    int
	inCI    int
	checks  []Check
	spend   float64
	landed  int
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
	b := &boardBatch{ref: ref, phase: BatchAssembling, started: clock()}
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
		if p == BatchTesting && b.testing.IsZero() {
			b.testing = clock()
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
	if !board.active {
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
	width := columns()
	if width <= 0 || width > 100 {
		width = 100
	}
	rule := Dim(w, strings.Repeat("─", width))
	label := func(verb, s string) string { return paint(w, statusColor(verb), iconFor(verb)+s) }
	head := func(s string) string { return paint(w, bold, fmt.Sprintf("%-8s", s)) }

	var b strings.Builder
	b.WriteString(rule + "\n")

	// Running.
	keys := sortedJobKeys()
	if len(keys) == 0 {
		fmt.Fprintf(&b, " %s %s\n", head("RUNNING"), Dim(w, "nothing"))
	}
	for i, k := range keys {
		j := board.jobs[k]
		h := ""
		if i == 0 {
			h = "RUNNING"
		}
		who := ""
		if j.actor != "" {
			who = actors.DisplayFor(j.key, j.actor)
		}
		line := fmt.Sprintf("%s %-12s %s", paint(w, ticketColor(j.key), pad(j.key, keyWidth)),
			j.stage, Dim(w, fmt.Sprintf("%6s", roundDur(now.Sub(j.started)))))
		if who != "" {
			line += "  " + paint(w, actorColor(j.actor), who)
		}
		if j.fan != nil {
			done := 0
			for _, c := range j.fan.children {
				if c.state == "done" || c.state == "failed" {
					done++
				}
			}
			line += Dim(w, fmt.Sprintf(" · %d/%d done", done, len(j.fan.children)))
		}
		if j.agents > 0 {
			line += Dim(w, fmt.Sprintf(" · %d subagent(s)", j.agents))
		}
		if j.note != "" && j.fan == nil {
			line += Dim(w, " · "+shortenPaths(j.note))
		}
		fmt.Fprintf(&b, " %s %s %s\n", head(h), label(VerbWorking, ""), line)
		if j.fan != nil {
			b.WriteString(renderFan(w, j.fan, head("")))
		}
	}

	// Queue and totals.
	q := board.queue
	queued := q["queued"]
	ready := queued - board.held
	if ready < 0 {
		ready = 0
	}
	fmt.Fprintf(&b, " %s %s  %s  %s  %s\n", head("QUEUE"),
		label(VerbWaiting, fmt.Sprintf("%d queued", queued)),
		Dim(w, fmt.Sprintf("%d ready · %d blocked · %d in CI · %d failed", ready, board.held, q["ci-wait"]+q["ready"], q["failed"])),
		label(VerbOK, fmt.Sprintf("%d landed", board.landed)),
		Dim(w, fmt.Sprintf("$%.2f", board.spend)))

	// Batch and CI.
	if bt := board.batch; bt != nil {
		since := bt.started
		if !bt.testing.IsZero() {
			since = bt.testing
		}
		var keys []string
		for _, m := range bt.members {
			keys = append(keys, m.key)
		}
		fmt.Fprintf(&b, " %s %s %s  %s\n", head("BATCH"), paint(w, bold, bt.ref),
			strings.Join(keys, " "), Dim(w, roundDur(now.Sub(since))))
		fmt.Fprintf(&b, " %s %s\n", head(""), batchPipeline(w, bt.phase))
		var ms []string
		for _, m := range bt.members {
			s := memberWord(w, m)
			if s != "" {
				ms = append(ms, s)
			}
		}
		if len(ms) > 0 {
			fmt.Fprintf(&b, " %s %s\n", head(""), strings.Join(ms, "  "))
		}
	}
	if len(board.checks) > 0 {
		var cs []string
		for _, c := range board.checks {
			cs = append(cs, c.Name+" "+checkIcon(w, c.State))
		}
		fmt.Fprintf(&b, " %s %s\n", head("CI"), strings.Join(cs, Dim(w, " · ")))
	}
	if board.last != "" {
		verb := VerbOK
		if !board.lastOK {
			verb = VerbFail
		}
		fmt.Fprintf(&b, " %s %s %s\n", head("LAST"), label(verb, board.last), Dim(w, "· "+roundDur(now.Sub(board.lastAt))+" ago"))
	}

	// Needs you: only when there is something, and loudest.
	if len(board.needs) > 0 {
		b.WriteString(rule + "\n")
		for i, n := range board.needs {
			h := ""
			if i == 0 {
				h = "NEEDS YOU"
			}
			fmt.Fprintf(&b, " %s %s\n", paint(w, bold+brightMagenta, fmt.Sprintf("%-9s", h)),
				paint(w, brightMagenta, needsGlyph()+" "+n))
		}
	}
	b.WriteString(rule + "\n")
	return b.String()
}

func needsGlyph() string {
	if glyphs() {
		return "⚑"
	}
	return "?"
}

func batchPipeline(w io.Writer, p BatchPhase) string {
	steps := []struct {
		name  string
		phase BatchPhase
	}{{"assembling", BatchAssembling}, {"CI", BatchTesting}, {"isolating", BatchIsolating}, {"landing", BatchDone}}
	reached := true
	var out []string
	for _, s := range steps {
		switch {
		case s.phase == p:
			out = append(out, paint(w, statusColor(VerbWorking), s.name+" "+strings.TrimSpace(iconFor(VerbWorking))))
			reached = false
		case reached:
			out = append(out, paint(w, statusColor(VerbOK), s.name+" "+strings.TrimSpace(iconFor(VerbOK))))
		default:
			out = append(out, Dim(w, s.name))
		}
	}
	return strings.Join(out, Dim(w, " → "))
}

func memberWord(w io.Writer, m *boardMember) string {
	switch m.state {
	case MemberLanded:
		return paint(w, statusColor(VerbOK), m.key+" landed")
	case MemberCulprit:
		return paint(w, statusColor(VerbFail), m.key+" culprit"+detailSuffix(m.detail))
	case MemberEjected:
		return paint(w, statusColor(VerbWarn), m.key+" ejected"+detailSuffix(m.detail))
	}
	return ""
}

func detailSuffix(d string) string {
	if d == "" {
		return ""
	}
	return " (" + d + ")"
}

func checkIcon(w io.Writer, state string) string {
	switch state {
	case CheckPassed:
		return paint(w, statusColor(VerbOK), strings.TrimSpace(iconFor(VerbOK)))
	case CheckFailed:
		return paint(w, statusColor(VerbFail), strings.TrimSpace(iconFor(VerbFail)))
	}
	return paint(w, statusColor(VerbWorking), strings.TrimSpace(iconFor(VerbWorking)))
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
	board.held, board.inCI, board.checks, board.spend, board.landed = 0, 0, nil, 0, 0
	board.batch, board.last, board.lastOK, board.lastAt, board.needs = nil, "", false, time.Time{}, nil
	board.sig, board.printed = "", time.Time{}
	actors.ResetTeams()
}

// fanShown is how many children a fan tree lists before summarising the rest.
const fanShown = 6

// renderFan draws a fan-out as a tree under its ticket. Caller holds board.mu.
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
		verb, tail := VerbWorking, ""
		switch c.state {
		case "done":
			verb, tail = VerbOK, "  "+Dim(w, roundDur(c.took))
		case "failed":
			verb, tail = VerbFail, "  "+Dim(w, roundDur(c.took))
		case "pending":
			verb = "pending"
			if f.limit > 0 && running >= f.limit {
				tail = "  " + Dim(w, fmt.Sprintf("queued (%d at a time)", f.limit))
			} else {
				tail = "  " + Dim(w, "queued")
			}
		}
		fmt.Fprintf(&b, " %s     %s%s%s%s\n", indent, Dim(w, mark),
			paint(w, statusColor(verb), iconFor(verb)), c.label, tail)
	}
	if shown < n {
		rest := map[string]int{}
		for _, c := range f.children[shown:] {
			rest[c.state]++
		}
		fmt.Fprintf(&b, " %s     %s%s\n", indent, Dim(w, last),
			Dim(w, fmt.Sprintf("%d more: %d done · %d running · %d queued",
				n-shown, rest["done"]+rest["failed"], rest["running"], rest["pending"])))
	}
	return b.String()
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
