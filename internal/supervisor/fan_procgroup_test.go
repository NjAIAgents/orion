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
	// Shrunk for the same reason as the single-run case -- the concurrency is
	// what this test is about, not the length of the wait (OR-202) -- but
	// deliberately three times the single-run budget, and that asymmetry is
	// the point rather than an oversight.
	//
	// The budget has to cover something this test cannot control: three
	// shells forking and writing their pid file before the deadline they are
	// racing fires. A child killed before it reports is a child whose sweep
	// this test cannot verify, so it fails as "test setup is broken" -- not a
	// product defect, but a red build either way.
	//
	// Measured under the CPU contention of a full parallel `go test ./...`:
	// three children take ~200ms to all report, with outliers to 1.38s. At
	// 500ms that was a 16%-per-run flake (4 failures in 25). At 1.5s it is
	// 0 in 40, worst run 2.04s end to end.
	//
	// The grace drops to 100ms to buy that headroom back: it is a ceiling on
	// how long a killed process may take to flush, and nothing in this test
	// flushes anything.
	shrinkWallClock(t, 1500*time.Millisecond, 100*time.Millisecond)
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

	start := time.Now()
	results := Fan(w, jobs)
	// Fan must return on the children's own deadlines. Without this guard the
	// test still passes when the sweep is broken -- it just waits out the
	// grandchildren's `sleep 300`, and a survivor that eventually exits on
	// its own is indistinguishable from one that was killed.
	if time.Since(start) > 10*time.Second {
		t.Fatalf("Fan took %s to return: it waited for the children rather than "+
			"killing them on their wall clock", time.Since(start))
	}
	// OR-202's own acceptance criterion: this test "completes in under a
	// second". The 10s check above only catches the old failure mode (waiting
	// out the real children); this catches the deadline regressing to
	// "shorter but still not sub-second" -- non-fatal so a loaded CI runner
	// doesn't flake the suite over a budget that isn't this test's point.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Fan took %s: OR-202 expects this test under a second", elapsed)
	}
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
