package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// TestBoardPreview renders a realistic watcher screen to BOARD_PREVIEW, for a
// person to look at with `cat`. Skipped unless the variable is set.
func TestBoardPreview(t *testing.T) {
	path := os.Getenv("BOARD_PREVIEW")
	if path == "" {
		t.Skip("set BOARD_PREVIEW=<file> to render")
	}
	resetBoard()
	ConsoleReset()
	var out bytes.Buffer
	base := time.Date(2026, 9, 27, 13, 0, 0, 0, time.Local)
	now := base
	clock = func() time.Time { return now }
	defer func() { clock = time.Now }()
	at := func(m, s int) time.Time { return base.Add(time.Duration(m)*time.Minute + time.Duration(s)*time.Second) }

	BoardEnable()
	LiveQueue([]QueueRow{{Stage: "queued"}, {Stage: "queued"}, {Stage: "queued"}, {Stage: "queued"},
		{Stage: "queued"}, {Stage: "queued"}, {Stage: "queued"}, {Stage: "queued"}, {Stage: "failed"}})
	BoardHeld(6)
	LiveSpend(4.20)
	for i := 0; i < 12; i++ {
		BoardLanded()
	}

	// Event lines, as the watcher prints them at default verbosity.
	LiveStart("LTA-2")
	Stage(&out, discardLog(), Handoff{At: at(0, 5), Key: "LTA-2", From: "routing", To: "implementing", By: "orion", Next: "implementer"})
	LiveStart("LTA-117")
	Stage(&out, discardLog(), Handoff{At: at(3, 10), Key: "LTA-117", From: "implementing", To: "qa", By: "implementer", Next: "qa", Detail: "5 case(s) planned"})
	LiveStart("LTA-118")
	Stage(&out, discardLog(), Handoff{At: at(3, 40), Key: "LTA-118", From: "routing", To: "implementing", By: "orion", Next: "implementer"})
	Print(&out, Line{At: at(4, 31), Key: "LTA-117", Actor: "qa", Verb: VerbOK, Msg: "QA cases 5/5 passed"})
	Print(&out, Line{At: at(6, 55), Key: "LTA-117", Actor: "qa", Verb: VerbFail,
		Msg: "suite red: 3 failed / 1124 -- tests/unit/test_store_sink_emit.py::test_retry_on_locked, tests/unit/test_store_keys.py::test_null_primary_key_rejected, +1 more (orion logs LTA-117)"})
	BoardFan("LTA-117", []string{"#1 qa · 4 case(s)", "#2 qa · 4 case(s)", "#3 qa · 4 case(s)", "#4 qa · 4 case(s)", "#5 qa · 4 case(s)"}, 2)
	BoardFanChild("LTA-117", 0, "done", 28*time.Second)
	BoardFanChild("LTA-117", 1, "running", 0)
	BoardFanChild("LTA-117", 2, "done", 32*time.Second)
	BoardFanChild("LTA-117", 3, "running", 0)
	LiveAgents("LTA-2")
	LiveAgents("LTA-2")
	LiveActivityNote("LTA-2", "implementer", "Edit src/log_triage/evidence.py")
	LiveActivityNote("LTA-118", "implementer", "Edit tests/unit/test_store.py")

	LiveBatchStart("orion/batch", "develop", []string{"LTA-119", "LTA-120", "LTA-124"})
	LiveBatchPhase(BatchTesting)
	LiveChecks([]Check{{Name: "test", State: CheckRunning}, {Name: "secret scan", State: CheckPassed}})
	board.last, board.lastOK, board.lastAt = "landed LTA-123 LTA-128", true, at(-11, 0)
	BoardNeedsYou([]string{"LTA-112 is out of automatic retries -- look at it, then requeue"})

	now = at(7, 12)
	if sp := os.Getenv("SCREEN_PREVIEW"); sp != "" {
		// The top-style view (OR-548): the same state, one frame.
		log := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		h := " " + Heading(&out, "orion watch LTA") + "  " + Dim(&out, now.Format("15:04:05")) +
			Dim(&out, "   log ~/.orion/logs/watch-20260927-130000.log   ctrl-c stops")
		f := frame(&out, 32, 118, h, renderBoard(&out, now), log)
		f = strings.ReplaceAll(f, escHome, "")
		f = strings.ReplaceAll(f, escBelow, "")
		if err := os.WriteFile(sp, []byte(strings.ReplaceAll(f, "\r\n", "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	BoardTick(&out)

	out.WriteString("\n   ...later, QA has finished and a batch went red:\n\n")
	Stage(&out, discardLog(), Handoff{At: at(6, 56), Key: "LTA-117", From: "qa", To: "fix round 1", By: "qa", Next: "implementer", Detail: "round 1 of 3"})
	LiveBatchPhase(BatchIsolating)
	LiveBatchMemberDetail("LTA-120", MemberCulprit, "test")
	LiveBatchMemberDetail("LTA-124", MemberEjected, "conflict in errors.py")
	LiveChecks([]Check{{Name: "test", State: CheckFailed}, {Name: "secret scan", State: CheckPassed}})
	BoardNeedsYou(nil)
	now = at(12, 40)
	BoardTick(&out)

	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func discardLog() *events.Log { return nil }
