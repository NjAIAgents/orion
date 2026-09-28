package ui

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
)

// OR-544: the board's content, and when it prints.
func boardRig(t *testing.T) (*bytes.Buffer, *time.Time) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	t.Setenv("COLUMNS", "100")
	resetBoard()
	ConsoleReset()
	now := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now; resetBoard() })
	BoardEnable()
	return &bytes.Buffer{}, &now
}

func TestTheBoardShowsRunningQueueBatchAndNeeds(t *testing.T) {
	out, now := boardRig(t)
	LiveStart("LTA-2")
	LiveStart("LTA-117")
	Stage(out, nil, Handoff{Key: "LTA-117", From: "implementing", To: "qa", Next: "qa"})
	BoardFan("LTA-117", []string{"#1 qa", "#2 qa", "#3 qa"}, 2)
	BoardFanChild("LTA-117", 0, "done", 28*time.Second)
	BoardFanChild("LTA-117", 1, "running", 0)
	LiveQueue([]QueueRow{{Stage: "queued"}, {Stage: "queued"}, {Stage: "failed"}})
	BoardHeld(1)
	LiveBatchStart("orion/batch", "develop", []string{"LTA-119", "LTA-120"})
	LiveBatchPhase(BatchTesting)
	LiveChecks([]Check{{Name: "test", State: CheckRunning}, {Name: "secret scan", State: CheckPassed}})
	BoardNeedsYou([]string{"LTA-112 is out of automatic retries"})
	out.Reset()
	*now = now.Add(4 * time.Minute)
	BoardTick(out)
	got := out.String()
	// OR-559: a fan-out with no failed child folds into the DOING column --
	// label, done/total and a bar -- and draws no tree.
	for _, want := range []string{
		"RUNNING", "LTA-2", "LTA-117", "qa 1/3 #..  1 running",
		"2 waiting · 1 ready · 1 blocked", "1 failed", "in integration",
		"BATCH", "orion/batch  LTA-119 LTA-120", "CI", "test >", "secret scan +",
		"NEEDS YOU", "LTA-112 is out of automatic retries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("board lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "#1 qa") || strings.Contains(got, "|- ") {
		t.Errorf("a fan-out with nothing failed drew its tree:\n%s", got)
	}
	// Plain text: no escape sequences, ASCII icons.
	if strings.Contains(got, "\x1b[") {
		t.Errorf("NO_COLOR output carries escape codes:\n%s", got)
	}
}

func TestTheBoardPrintsOnChangeNotOnEveryTick(t *testing.T) {
	out, now := boardRig(t)
	LiveStart("LTA-2")
	BoardTick(out)
	first := out.Len()
	if first == 0 {
		t.Fatal("the first tick printed nothing")
	}
	*now = now.Add(30 * time.Second)
	BoardTick(out)
	if out.Len() != first {
		t.Errorf("an unchanged board printed again after 30s")
	}
	LiveStart("LTA-117")
	BoardTick(out)
	if out.Len() == first {
		t.Errorf("a new ticket did not reprint the board")
	}
	second := out.Len()
	*now = now.Add(boardEvery)
	BoardTick(out)
	if out.Len() == second {
		t.Errorf("a busy board was not restated after %s", boardEvery)
	}
}

func TestAFinishedBatchStaysAsLast(t *testing.T) {
	out, now := boardRig(t)
	LiveBatchStart("orion/batch", "develop", []string{"LTA-119", "LTA-120"})
	LiveBatchMember("LTA-119", MemberLanded)
	LiveBatchMemberDetail("LTA-120", MemberEjected, "conflict in errors.py")
	LiveBatchEnd()
	*now = now.Add(5 * time.Minute)
	BoardTick(out)
	if got := out.String(); !strings.Contains(got, "LAST") || !strings.Contains(got, "landed LTA-119") {
		t.Errorf("the finished batch was not kept:\n%s", got)
	}
}

// A failure line is never cut to the terminal width.
func TestAFailureLineIsNotClipped(t *testing.T) {
	out, _ := boardRig(t)
	long := "suite red: 3 failed / 1124 -- " + strings.Repeat("tests/unit/test_x.py::test_y, ", 6)
	Print(out, Line{Key: "LTA-117", Actor: "qa", Verb: VerbFail, Msg: long})
	if !strings.Contains(out.String(), strings.TrimSpace(long)) {
		t.Errorf("a failure line was clipped:\n%s", out.String())
	}
}

// OR-544: a failed check turns the batch red, an ejected ticket leaves the
// member list with a short reason, and held tickets are grouped by blocker.
func TestARedBatchAndAnEjectionReadCorrectly(t *testing.T) {
	out, now := boardRig(t)
	LiveQueue([]QueueRow{{Stage: "queued"}, {Stage: "queued"}, {Stage: "queued"}})
	BoardHeld(3)
	BoardHeldBy([][2]string{{"LTA-31, LTA-44", "blocked by LTA-30"}, {"LTA-77", "blocked by LTA-30, LTA-98"}})
	LiveBatchStart("orion/batch", "develop", []string{"LTA-2", "LTA-117", "LTA-118"})
	LiveBatchPhase(BatchTesting)
	LiveBatchMemberDetail("LTA-118", MemberEjected,
		"conflicts with the batch: orion/lta-118-2 does not merge into the batch: Auto-merging specs/001-log-triage-agent/tasks.md")
	LiveChecks([]Check{{Name: "test", State: CheckFailed}})
	*now = now.Add(time.Minute)
	BoardTick(out)
	got := out.String()
	for _, want := range []string{
		"3 waiting · all blocked", "2 on LTA-30", "1 on LTA-30, LTA-98",
		"orion/batch red", "LTA-2 LTA-117", "CI x",
		"ejected, next batch: LTA-118 (conflict in tasks.md)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("board lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "LTA-117 LTA-118") {
		t.Errorf("an ejected ticket is still listed as a member:\n%s", got)
	}
}

// OR-553: a file-overlap hold names who it waits on, not every shared path
// and a paragraph of why -- that line overflowed the board's QUEUE row.
func TestAFileOverlapHoldIsShortOnTheBoard(t *testing.T) {
	resetBoard()
	t.Cleanup(resetBoard)
	BoardEnable()
	BoardHeldBy([][2]string{{"LTA-44", "src/log_triage/store.py, src/log_triage/cli.py is already spoken for by LTA-31; two tickets that declare the same ground collide"}})
	board.mu.Lock()
	got := stripANSI(heldSummary(&bytes.Buffer{}))
	board.mu.Unlock()
	if !strings.Contains(got, "1 sharing files with LTA-31") || strings.Contains(got, "store.py") {
		t.Fatalf("held summary = %q", got)
	}
}

// OR-553: a batch whose members are marked landed before it ends reads as
// landed, not "ended without a result" -- the resumed-landing path.
func TestAResumedBatchThatLandedSaysSo(t *testing.T) {
	resetBoard()
	t.Cleanup(resetBoard)
	BoardEnable()
	LiveBatchResume("orion/batch", "develop", []string{"LTA-2", "LTA-118"}, time.Time{})
	LiveBatchMember("LTA-2", MemberLanded)
	LiveBatchMember("LTA-118", MemberLanded)
	LiveBatchEnd()
	board.mu.Lock()
	defer board.mu.Unlock()
	if !board.lastOK || board.last != "landed LTA-2 LTA-118" {
		t.Fatalf("last = %q ok=%v", board.last, board.lastOK)
	}
}

// OR-559 tests: the watch view's redesign.

// populateBoard fills the board with the approved design's state: a folded
// fan, a failed fan, a note, a queue, a batch in CI against a median, checks,
// a last result and NEEDS YOU. Caller has reset the board and set the clock.
func populateBoard(now *time.Time) {
	BoardEnable()
	LiveQueue([]QueueRow{{Stage: "queued"}, {Stage: "queued"}, {Stage: "failed"}})
	BoardHeld(2)
	BoardHeldBy([][2]string{{"LTA-31, LTA-44", "blocked by LTA-137"}})
	LiveSpend(193.74)
	BoardLanded()
	LiveStart("LTA-63")
	boardStage("LTA-63", "qa", "qa")
	BoardFan("LTA-63", []string{"#1 qa · 4 case(s)", "#2 qa · 4 case(s)", "#3 qa · 4 case(s)"}, 2)
	BoardFanChild("LTA-63", 0, "done", 28*time.Second)
	BoardFanChild("LTA-63", 1, "running", 0)
	LiveStart("LTA-133")
	boardStage("LTA-133", "implementing", "implementer")
	LiveActivityNote("LTA-133", "implementer", "Edit src/log_triage/cluster.py")
	LiveStart("LTA-117")
	boardStage("LTA-117", "qa", "qa")
	BoardFan("LTA-117", []string{"#1 qa · 4 case(s)", "#2 qa · 4 case(s)", "#3 qa · 4 case(s)"}, 2)
	BoardFanChild("LTA-117", 0, "done", 28*time.Second)
	BoardFanChild("LTA-117", 1, "failed", 41*time.Second)
	BoardFanChild("LTA-117", 2, "running", 0)
	LiveBatchStart("orion/batch", "develop", []string{"LTA-119", "LTA-120"})
	*now = now.Add(12 * time.Second)
	LiveBatchPhase(BatchTesting)
	LiveBatchMedian(6 * time.Minute)
	LiveChecks([]Check{{Name: "test", State: CheckRunning}, {Name: "secret scan", State: CheckPassed}})
	board.last, board.lastOK, board.lastAt = "landed LTA-123", true, *now
	BoardNeedsYou([]string{"LTA-112 is out of automatic retries"})
	*now = now.Add(4 * time.Minute)
}

// fullFrame renders one full-screen frame of the populated board.
func fullFrame(t *testing.T, rows, cols int) []string {
	t.Helper()
	var w bytes.Buffer
	s := &Screen{title: "LTA", started: clock().Add(-2 * time.Hour), term: &w, logPath: "/tmp/watch.log"}
	h := s.header(cols, boardSummary())
	board.mu.Lock()
	top, foot := renderBoardParts(&w, clock(), true)
	board.mu.Unlock()
	log := []string{"\x1b[31m" + strings.Repeat("a long failed log line ", 20) + "\x1b[0m", strings.Repeat("plain ", 40)}
	f := frame(&w, rows, cols, h, top, log, foot)
	return strings.Split(strings.TrimSuffix(strings.TrimPrefix(f, escHome), escBelow), "\r\n")
}

func designRig(t *testing.T) *time.Time {
	t.Helper()
	resetBoard()
	ConsoleReset()
	now := time.Date(2026, 9, 27, 22, 14, 7, 0, time.UTC)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now; resetBoard() })
	populateBoard(&now)
	return &now
}

// The header's counts are the board's: running tickets, queued and failed
// from the queue, landed and the spend.
func TestTheHeaderCountsComeFromTheBoard(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	designRig(t)
	s := &Screen{title: "LTA", started: clock().Add(-2*time.Hour - 14*time.Minute), term: &bytes.Buffer{}}
	h := s.header(118, boardSummary())
	for _, want := range []string{"orion watch LTA · up 2h14m", "3 running", "2 queued", "1 landed", "1 failed", "$193.74", "22:18:19"} {
		if !strings.Contains(h, want) {
			t.Errorf("header lacks %q: %q", want, h)
		}
	}
	if w := visibleWidth(h); w > 117 {
		t.Errorf("header is %d wide on a 118-column terminal", w)
	}
}

// A fan-out with no failed child is one row: its label, done of total and a
// bar in DOING, and no tree under it.
func TestAFoldedFanShowsABarAndNoTree(t *testing.T) {
	colourOn(t)
	designRig(t)
	board.mu.Lock()
	top, _ := renderBoardParts(&bytes.Buffer{}, clock(), true)
	board.mu.Unlock()
	plain := stripANSI(top)
	if !strings.Contains(plain, "qa 1/3 ▰▱▱  1 running") {
		t.Fatalf("LTA-63's fan is not folded to a bar:\n%s", plain)
	}
	// LTA-63's row is followed by LTA-117's, with no tree between them.
	rows := strings.Split(plain, "\n")
	for i, r := range rows {
		if strings.Contains(r, "LTA-63") && (i+1 >= len(rows) || !strings.Contains(rows[i+1], "LTA-117")) {
			t.Fatalf("something is drawn under the folded fan:\n%s", plain)
		}
	}
}

// A failed child unfolds the tree, with the failed child in the fail colour.
func TestAFailedChildShowsTheTree(t *testing.T) {
	colourOn(t)
	designRig(t)
	board.mu.Lock()
	top, _ := renderBoardParts(&bytes.Buffer{}, clock(), true)
	board.mu.Unlock()
	plain := stripANSI(top)
	for _, want := range []string{"✗  LTA-117", "qa 2/3 ▰▰▱  1 failed", "├ ✓  #1 qa · 4 case(s)  28s", "├ ✗  #2 qa · 4 case(s)  41s", "└ ◐  #3 qa"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the failed fan lacks %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(top, red+"#2 qa · 4 case(s)") {
		t.Errorf("the failed child is not in the fail colour:\n%q", top)
	}
}

// Every running row's key and stage start in the same column as the header
// row's TICKET and STAGE, with colour codes in the row.
func TestTheRunningTableColumnsAlign(t *testing.T) {
	colourOn(t)
	designRig(t)
	LiveStart("ORION-1234")
	boardStage("ORION-1234", "fix round 1", "implementer")
	board.mu.Lock()
	top, _ := renderBoardParts(&bytes.Buffer{}, clock(), true)
	board.mu.Unlock()
	col := func(row, word string) int {
		i := strings.Index(row, word)
		if i < 0 {
			return -1
		}
		return visibleWidth(row[:i])
	}
	var keyCol, stageCol, whoCol int
	n := 0
	for _, r := range strings.Split(stripANSI(top), "\n") {
		if strings.Contains(r, "TICKET") {
			keyCol, stageCol, whoCol = col(r, "TICKET"), col(r, "STAGE"), col(r, "WHO")
			continue
		}
		for _, k := range []string{"LTA-63", "LTA-133", "LTA-117", "ORION-1234"} {
			if !strings.Contains(r, k+" ") {
				continue
			}
			n++
			j := board.jobs[k]
			if c := col(r, k); c != keyCol {
				t.Errorf("%s starts at column %d, TICKET at %d:\n%s", k, c, keyCol, r)
			}
			if c := col(r, " "+j.stage+" ") + 1; c != stageCol {
				t.Errorf("%s's stage starts at column %d, STAGE at %d:\n%s", k, c, stageCol, r)
			}
			if c := col(r, actors.DisplayFor(k, j.actor)); c != whoCol {
				t.Errorf("%s's agent starts at column %d, WHO at %d:\n%s", k, c, whoCol, r)
			}
		}
	}
	if n != 4 || keyCol <= 0 {
		t.Fatalf("found %d running rows, key column %d:\n%s", n, keyCol, stripANSI(top))
	}
}

// The light theme draws the panels on pale grounds, and moves the text
// colours to dark shades that read on them.
func TestTheLightThemeUsesLightPanels(t *testing.T) {
	colourOn(t)
	t.Setenv("ORION_THEME", "light")
	designRig(t)
	rows := fullFrame(t, 30, 118)
	if !strings.HasPrefix(rows[0], lightHeaderBg) || !strings.HasPrefix(rows[1], lightPanelBg) ||
		!strings.HasPrefix(rows[len(rows)-1], lightFootBg) {
		t.Fatalf("panels are not light: header %q, top %q, foot %q", rows[0][:24], rows[1][:24], rows[len(rows)-1][:24])
	}
	all := strings.Join(rows, "\n")
	for _, dark := range []string{headerBg, panelBg, footBg, "\x1b[38;5;221m", "\x1b[38;5;248m"} {
		if strings.Contains(all, dark) {
			t.Errorf("a dark-theme code %q is in the light frame", dark)
		}
	}
	if !strings.Contains(all, "\x1b[38;5;130m") {
		t.Errorf("amber was not moved to its dark shade on the light panel")
	}
}

var sgrRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Mono, asked for by name or implied by NO_COLOR, draws a frame with no
// colour code in it at all -- including where a long line is clipped.
func TestMonoAndNoColorEmitNoColourCodes(t *testing.T) {
	for _, env := range [][2]string{{"ORION_THEME", "mono"}, {"NO_COLOR", "1"}} {
		t.Run(env[0], func(t *testing.T) {
			colourOn(t)
			t.Setenv(env[0], env[1])
			designRig(t)
			rows := fullFrame(t, 30, 118)
			for _, r := range rows {
				if m := sgrRE.FindString(r); m != "" {
					t.Fatalf("mono frame carries %q in %q", m, r)
				}
			}
			if !strings.Contains(strings.Join(rows, "\n"), "[RUNNING]") {
				t.Fatalf("mono chips are not bracketed labels")
			}
		})
	}
}

// No frame row is wider than the terminal, in any theme or width.
func TestNoFrameRowIsWiderThanTheTerminal(t *testing.T) {
	for _, th := range []string{"dark", "light", "mono"} {
		for _, cols := range []int{60, 100, 118} {
			t.Run(fmt.Sprintf("%s-%d", th, cols), func(t *testing.T) {
				colourOn(t)
				t.Setenv("ORION_THEME", th)
				designRig(t)
				for _, r := range fullFrame(t, 30, cols) {
					if w := visibleWidth(stripANSI(r)); w > cols-1 {
						t.Fatalf("a row is %d wide on a %d-column terminal: %q", w, cols, stripANSI(r))
					}
				}
			})
		}
	}
}

// The waiting icon is the one-cell clock face, on the QUEUE line too.
func TestTheWaitingIconIsTheOneCellClock(t *testing.T) {
	colourOn(t)
	designRig(t)
	if g := strings.TrimSpace(iconFor(VerbWaiting)); g != "◷" {
		t.Fatalf("waiting icon = %q, want ◷", g)
	}
	if runeWidth('◷') != 1 || cells(iconFor(VerbWaiting)) != iconWidth {
		t.Fatalf("◷ is not one cell: runeWidth %d, padded icon %d cells", runeWidth('◷'), cells(iconFor(VerbWaiting)))
	}
	board.mu.Lock()
	top, _ := renderBoardParts(&bytes.Buffer{}, clock(), true)
	board.mu.Unlock()
	if !strings.Contains(stripANSI(top), "◷ 2 waiting") || strings.Contains(top, "⏳") {
		t.Fatalf("the QUEUE line does not use ◷:\n%s", stripANSI(top))
	}
}

// The batch steps say how long they took, and CI is measured against the
// usual CI time with a bar.
func TestTheBatchStepsShowTheirTimesAndCIProgress(t *testing.T) {
	colourOn(t)
	designRig(t)
	board.mu.Lock()
	_, foot := renderBoardParts(&bytes.Buffer{}, clock(), true)
	board.mu.Unlock()
	plain := stripANSI(foot)
	for _, want := range []string{"started 4m ago", "assembling ✓ 12s", "CI ◐ 4m of ~6m ▰▰▰▰▰▰▰▱▱▱▱", "isolating  →  landing"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the bottom panel lacks %q:\n%s", want, plain)
		}
	}
	LiveBatchMedian(0)
	board.mu.Lock()
	_, foot = renderBoardParts(&bytes.Buffer{}, clock(), true)
	board.mu.Unlock()
	if p := stripANSI(foot); !strings.Contains(p, "CI ◐ 4m  →") || strings.Contains(p, "of ~") {
		t.Errorf("with no median the CI step should show only its time:\n%s", p)
	}
}

// OR-568: a CI fix runs inside collect, so the watch's dispatch never ends
// its row; a batch landing the ticket must.
func TestALandedBatchMemberLeavesTheRunningRows(t *testing.T) {
	resetBoard()
	t.Cleanup(resetBoard)
	boardNote("LTA-144", "", "fixing CI")
	boardBatchStart("orion/batch", []string{"LTA-144", "LTA-77"})
	boardBatchMember("LTA-144", MemberLanded, "")
	board.mu.Lock()
	_, stale := board.jobs["LTA-144"]
	board.mu.Unlock()
	if stale {
		t.Fatal("LTA-144 landed but its running row stayed on the board")
	}
}
