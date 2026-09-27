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
		"2 queued", "1 ready · 1 blocked", "1 failed",
		"BATCH", "orion/batch LTA-119 LTA-120", "CI", "test >", "secret scan +",
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
