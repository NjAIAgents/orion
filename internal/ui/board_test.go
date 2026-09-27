package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
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
	for _, want := range []string{
		"RUNNING", "LTA-2", "LTA-117", "1/3 done", "#1 qa", "#3 qa  queued",
		"2 waiting · 1 ready · 1 blocked", "1 failed", "in integration",
		"BATCH", "orion/batch  LTA-119 LTA-120", "CI", "test >", "secret scan +",
		"NEEDS YOU", "LTA-112 is out of automatic retries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("board lacks %q:\n%s", want, got)
		}
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
