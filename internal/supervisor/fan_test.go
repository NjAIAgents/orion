package supervisor

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/cost"
	"github.com/orion-sdlc/orion/internal/events"
)

const fanResultJSON = `{"type":"result","session_id":"s","result":"done",` +
	`"total_cost_usd":0.01,"is_error":false}`

// TestFanReturnsResultsInInputOrder proves N children run concurrently
// (OR-181) and that results[i] always corresponds to jobs[i], regardless of
// which goroutine happens to finish first.
func TestFanReturnsResultsInInputOrder(t *testing.T) {
	fakeClaudeTree(t, "sleep 0.2\necho '"+fanResultJSON+"'\nexit 0\n")

	w := ws(t, "")
	var jobs []Options
	for i := 0; i < 5; i++ {
		jobs = append(jobs, Options{Stage: fmt.Sprintf("child-%d", i), Prompt: "x",
			MaxMinutes: 1, MaxTurns: 1})
	}

	results := Fan(w, jobs)
	if len(results) != len(jobs) {
		t.Fatalf("got %d results for %d jobs", len(results), len(jobs))
	}
	for i, r := range results {
		if r.Result == nil {
			t.Fatalf("result %d is nil: %+v", i, r)
		}
		want := fmt.Sprintf("child-%d", i)
		if !strings.Contains(r.Result.LogPath, want) {
			t.Errorf("result %d belongs to a different job: log %q does not name %q",
				i, r.Result.LogPath, want)
		}
	}
}

// TestFanCapsConcurrency is OR-181's central acceptance criterion: a cap of
// 2 must never have 3 children running at once, and the cap must be
// configurable per project rather than hardcoded.
func TestFanCapsConcurrency(t *testing.T) {
	probeDir := t.TempDir()
	t.Setenv("FAN_PROBE_DIR", probeDir)
	// Each invocation marks itself present for the duration of its sleep, so
	// polling the directory's entry count at any instant is a live read of
	// how many children are actually running right now.
	fakeClaudeTree(t, `id="p$$"
touch "$FAN_PROBE_DIR/$id"
sleep 0.3
rm -f "$FAN_PROBE_DIR/$id"
echo '`+fanResultJSON+`'
exit 0
`)

	w := ws(t, `{"limits":{"max_concurrent_children":2}}`)
	var jobs []Options
	for i := 0; i < 6; i++ {
		jobs = append(jobs, Options{Stage: fmt.Sprintf("child-%d", i), Prompt: "x",
			MaxMinutes: 1, MaxTurns: 1})
	}

	var maxSeen int32
	stop := make(chan struct{})
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			entries, _ := os.ReadDir(probeDir)
			if n := int32(len(entries)); n > atomic.LoadInt32(&maxSeen) {
				atomic.StoreInt32(&maxSeen, n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	results := Fan(w, jobs)
	close(stop)
	<-probeDone

	if len(results) != len(jobs) {
		t.Fatalf("got %d results for %d jobs", len(results), len(jobs))
	}
	seen := atomic.LoadInt32(&maxSeen)
	if seen > 2 {
		t.Fatalf("saw %d children running at once, cap was 2", seen)
	}
	if seen < 2 {
		t.Fatalf("never observed concurrency at all (max seen %d) -- "+
			"either Fan is running jobs sequentially or the probe missed the overlap", seen)
	}
}

// TestFanOneChildFailingReturnsTheOthers is OR-181's other central
// criterion: a fan-out where one timeout or crash discards completed work
// from its siblings is worse than running sequentially.
func TestFanOneChildFailingReturnsTheOthers(t *testing.T) {
	// Distinguishes a failing child from a passing one by its stage name,
	// which reaches the fake claude script through argv (the -p prompt is
	// not what varies here, the stage is what the test keys off of via the
	// log path instead -- so key off Prompt, which the script CAN see).
	fakeClaudeTree(t, `for a in "$@"; do
  if [ "$a" = "FAIL" ]; then
    echo "boom" >&2
    exit 1
  fi
done
echo '`+fanResultJSON+`'
exit 0
`)

	w := ws(t, "")
	jobs := []Options{
		{Stage: "ok-1", Prompt: "PASS", MaxMinutes: 1, MaxTurns: 1},
		{Stage: "broken", Prompt: "FAIL", MaxMinutes: 1, MaxTurns: 1},
		{Stage: "ok-2", Prompt: "PASS", MaxMinutes: 1, MaxTurns: 1},
	}

	results := Fan(w, jobs)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if results[0].Result == nil || results[0].Result.ExitCode != 0 {
		t.Errorf("child 0 (ok-1) was discarded by its sibling's failure: %+v", results[0])
	}
	if results[1].Result == nil || results[1].Result.ExitCode == 0 {
		t.Errorf("the failing child did not report a failure: %+v", results[1])
	}
	if results[2].Result == nil || results[2].Result.ExitCode != 0 {
		t.Errorf("child 2 (ok-2) was discarded by its sibling's failure: %+v", results[2])
	}
}

// TestFanCostReportShowsARowPerChild proves recordTicketCost's existing
// per-run accounting (OR-143) survives being called from N goroutines
// concurrently rather than from one caller in sequence -- each child keeps
// its own actor, so a ticket's cost report still shows one row per child.
func TestFanCostReportShowsARowPerChild(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")

	w := ws(t, "")
	jobs := []Options{
		{Stage: "a", Prompt: "x", MaxMinutes: 1, MaxTurns: 1, Actor: "child-a", Key: "OR-181"},
		{Stage: "b", Prompt: "x", MaxMinutes: 1, MaxTurns: 1, Actor: "child-b", Key: "OR-181"},
		{Stage: "c", Prompt: "x", MaxMinutes: 1, MaxTurns: 1, Actor: "child-c", Key: "OR-181"},
	}

	if results := Fan(w, jobs); len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	rep := cost.Aggregate(cost.ReadAll(events.Path(w.Dir)), "OR-181")
	if len(rep.Rows) != 3 {
		t.Fatalf("cost report has %d rows, want one per child (3): %+v", len(rep.Rows), rep.Rows)
	}
	seen := map[string]bool{}
	for _, row := range rep.Rows {
		seen[row.ID] = true
	}
	for _, actor := range []string{"child-a", "child-b", "child-c"} {
		if !seen[actor] {
			t.Errorf("no cost row for %s: %+v", actor, rep.Rows)
		}
	}
}
