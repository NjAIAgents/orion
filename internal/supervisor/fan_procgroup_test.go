//go:build !windows

package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFanLeavesNoSurvivingChildOnWallClockTimeout extends OR-141's
// single-run guarantee (see TestRunKillsGrandchildrenOnWallClockTimeout) to
// N concurrently spawned children: every one of them must have its process
// group swept when it times out, not just whichever happened to be the
// last one running -- the failure mode Fan could introduce if any part of
// its cleanup path were shared across goroutines instead of scoped to each.
func TestFanLeavesNoSurvivingChildOnWallClockTimeout(t *testing.T) {
	pidDir := t.TempDir()
	t.Setenv("FAN_PID_DIR", pidDir)
	fakeClaudeTree(t, `id="p$$"
sleep 300 & echo $! > "$FAN_PID_DIR/$id"
wait
`)

	// Cap raised to match the job count: all three must start at once, or a
	// cap-gated queue would stagger their timeouts and this test would take
	// multiples of MaxMinutes to run instead of proving the concurrent case.
	w := ws(t, `{"limits":{"max_concurrent_children":3}}`)
	var jobs []Options
	for i := 0; i < 3; i++ {
		jobs = append(jobs, Options{Stage: fmt.Sprintf("child-%d", i), Prompt: "x",
			MaxMinutes: 1, MaxTurns: 1})
	}

	results := Fan(w, jobs)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for i, r := range results {
		if r.Result == nil || !r.Result.Killed {
			t.Fatalf("child %d was not reported as killed: %+v", i, r)
		}
	}

	var pids []int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(pids) < 3 {
		entries, _ := os.ReadDir(pidDir)
		pids = pids[:0]
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(pidDir, e.Name()))
			if err != nil {
				continue
			}
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pids = append(pids, pid)
			}
		}
		if len(pids) < 3 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if len(pids) != 3 {
		t.Fatalf("only %d of 3 grandchildren reported a pid -- test setup is broken", len(pids))
	}
	for _, pid := range pids {
		if !waitGone(t, pid) {
			killGroupPID(pid)
			t.Fatalf("grandchild %d survived Fan() -- an orphan left behind by a "+
				"concurrently spawned child, not just the last one to finish", pid)
		}
	}
}
