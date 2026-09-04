//go:build !windows

package supervisor

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
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

// TestFanFailedChildReportsErrForCaller proves the ticket's "report which
// failed and why" half of the failure policy, not just that the sibling
// results survive. FanResult carries an Err alongside the Result precisely
// so a caller can distinguish a clean exit from a failed one without
// re-deriving it from ExitCode -- a caller that only looks at ExitCode is
// one missed check away from treating a failure as a pass.
func TestFanFailedChildReportsErrForCaller(t *testing.T) {
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
	}

	results := Fan(w, jobs)
	if results[0].Err != nil {
		t.Errorf("passing child reported an error it did not have: %v", results[0].Err)
	}
	if results[1].Err == nil {
		t.Fatalf("failing child reported no error -- a caller has no way to learn why it failed")
	}
	if !strings.Contains(results[1].Err.Error(), "broken") {
		t.Errorf("failing child's error does not name its own stage, so a caller cannot "+
			"tell which of several failures this is: %v", results[1].Err)
	}
}

// TestFanWithNoJobsReturnsEmptyWithoutBlocking guards the boundary the other
// tests never exercise: zero jobs. A WaitGroup of zero and a results slice
// of length zero must return immediately rather than hang -- the kind of
// off-by-one a fan-out primitive is exactly the place to get wrong.
func TestFanWithNoJobsReturnsEmptyWithoutBlocking(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	w := ws(t, "")

	done := make(chan []FanResult, 1)
	go func() { done <- Fan(w, nil) }()

	select {
	case results := <-done:
		if len(results) != 0 {
			t.Fatalf("got %d results for 0 jobs, want 0", len(results))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fan(w, nil) did not return -- it hung on zero jobs")
	}
}

// captureStderr redirects os.Stderr to a pipe for the duration of fn,
// recording the elapsed time (from the moment capture starts) at which each
// line arrives. Lines are timestamped as they are READ from the pipe, not
// after fn returns, so a caller can tell "announced as it happened" from
// "announced once, after the fact, in the right order" -- two
// implementations that would otherwise look identical from the final text
// alone.
func captureStderr(t *testing.T, fn func()) (lines []string, at []time.Duration) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	// fanOut holds its own reference to the stderr it was initialised with, so
	// reassigning os.Stderr alone leaves a fan-out writing to the real one and
	// this helper reading an empty pipe. Both are swapped, and both restored.
	oldFan := fanOut
	fanOut = w
	start := time.Now()

	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			mu.Lock()
			lines = append(lines, sc.Text())
			at = append(at, time.Since(start))
			mu.Unlock()
		}
	}()

	fn()

	os.Stderr = old
	fanOut = oldFan
	_ = w.Close()
	<-done
	_ = r.Close()
	return lines, at
}

// TestFanAnnouncesEachChildAsItLands is the acceptance criterion for
// "progress updates announce each child as it lands, not silent until the
// last one": three children with staggered sleeps must produce their landing
// line close to their OWN finish time, not all bunched at the end once every
// child is done. A Fan that buffered every announcement until it returned
// would pass every other test in this file and still leave an operator
// watching nothing happen for the full run.
func TestFanAnnouncesEachChildAsItLands(t *testing.T) {
	fakeClaudeTree(t, `for a in "$@"; do
  case "$a" in
    FAST) sleep 0.1;;
    MID) sleep 0.3;;
    SLOW) sleep 0.6;;
  esac
done
echo '`+fanResultJSON+`'
exit 0
`)

	w := ws(t, `{"limits":{"max_concurrent_children":3}}`)
	jobs := []Options{
		{Stage: "fast", Prompt: "FAST", MaxMinutes: 1, MaxTurns: 1},
		{Stage: "mid", Prompt: "MID", MaxMinutes: 1, MaxTurns: 1},
		{Stage: "slow", Prompt: "SLOW", MaxMinutes: 1, MaxTurns: 1},
	}

	var totalElapsed time.Duration
	lines, at := captureStderr(t, func() {
		start := time.Now()
		results := Fan(w, jobs)
		totalElapsed = time.Since(start)
		if len(results) != 3 {
			t.Fatalf("got %d results, want 3", len(results))
		}
	})

	// A landing line is the one that reports a child's exit; the roster lines
	// printed before dispatch carry the same running count and must not be
	// mistaken for landings. Keyed on "exit" rather than on the running count
	// for exactly that reason.
	var landedAt []time.Duration
	for i, l := range lines {
		if strings.Contains(l, "exit") {
			landedAt = append(landedAt, at[i])
		}
	}
	if len(landedAt) != 3 {
		t.Fatalf("got %d landing announcements, want 3:\n%s", len(landedAt), strings.Join(lines, "\n"))
	}
	// The fast child (sleeps 0.1s) must be announced well before the whole
	// fan returns (dominated by the slow child's 0.6s) -- a wide margin so
	// scheduling jitter under a loaded CI runner cannot flip this.
	if landedAt[0] > totalElapsed/2 {
		t.Errorf("first landing announced at %s, %s after Fan started and Fan took %s total -- "+
			"that is not distinguishable from every announcement arriving at the end",
			landedAt[0], landedAt[0], totalElapsed)
	}
	// Strictly increasing: children land in the order they actually finish,
	// and each is announced at that time, not re-ordered into dispatch order.
	for i := 1; i < len(landedAt); i++ {
		if landedAt[i] <= landedAt[i-1] {
			t.Errorf("landing %d arrived at %s, not after landing %d at %s",
				i, landedAt[i], i-1, landedAt[i-1])
		}
	}
}

// TestFanAnnouncesTheCostShapeBeforeAnyChildStarts is CONVENTIONS-orchestration
// §C's other half: how many children and their cap must be stated before
// dispatch, not discovered by counting landing lines after the fact.
func TestFanAnnouncesTheCostShapeBeforeAnyChildStarts(t *testing.T) {
	fakeClaudeTree(t, "echo '"+fanResultJSON+"'\nexit 0\n")
	w := ws(t, `{"limits":{"max_concurrent_children":2}}`)
	jobs := []Options{
		{Stage: "a", Prompt: "x", MaxMinutes: 1, MaxTurns: 1},
		{Stage: "b", Prompt: "x", MaxMinutes: 1, MaxTurns: 1},
	}

	lines, _ := captureStderr(t, func() { Fan(w, jobs) })
	if len(lines) == 0 {
		t.Fatal("Fan announced nothing at all")
	}
	first := lines[0]
	if !strings.Contains(first, "fan-out 2 children") || !strings.Contains(first, "cap 2") {
		t.Errorf("first announcement = %q, want the fan width and cap stated before any child runs", first)
	}
	if strings.Contains(first, "landed") {
		t.Error("the cost-shape announcement and a landing announcement are the same line")
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
