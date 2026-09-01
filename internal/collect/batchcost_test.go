package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// writeLog puts events on disk in the shape events.Read expects.
func writeLog(t *testing.T, evs []events.Event) string {
	t.Helper()
	dir := t.TempDir()
	log, err := events.Open(filepath.Join(dir, "events.jsonl"), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		log.Emit(e)
	}
	_ = log.Close()
	return filepath.Join(dir, "events.jsonl")
}

func landing(key string, pushed, merged time.Time) []events.Event {
	return []events.Event{
		{At: pushed, Kind: events.KindPush, Key: key, Msg: "pushed"},
		{At: merged, Kind: events.KindMerge, Key: key, Msg: "merged"},
	}
}

// The baseline is the middle of what per-branch landings actually cost here.
func TestTheBaselineIsTheMedianOfPastLandings(t *testing.T) {
	t0 := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	var evs []events.Event
	for i, d := range []time.Duration{10 * time.Minute, 30 * time.Minute, 20 * time.Minute} {
		at := t0.Add(time.Duration(i) * time.Hour)
		evs = append(evs, landing("OR-"+string(rune('1'+i)), at, at.Add(d))...)
	}

	got := perBranchBaseline(writeLog(t, evs))
	if got.Samples != 3 {
		t.Fatalf("samples = %d, want 3", got.Samples)
	}
	if got.Median != 20*time.Minute {
		t.Errorf("median = %s, want 20m0s (the middle of 10, 20, 30)", got.Median)
	}
}

// Too little history is SAID, not guessed at. A median of one landing is an
// anecdote wearing a median's clothes.
func TestTooLittleHistoryReportsNoBaselineRatherThanANumber(t *testing.T) {
	t0 := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	path := writeLog(t, landing("OR-1", t0, t0.Add(10*time.Minute)))

	got := perBranchBaseline(path)
	if got.Median != 0 {
		t.Errorf("median = %s from one landing; want none reported", got.Median)
	}
	if got.Samples != 1 {
		t.Errorf("samples = %d, want the count still reported so the reader knows why", got.Samples)
	}

	line := costLine(1, 3, 5*time.Minute, got)
	if !strings.Contains(line, "no baseline yet") {
		t.Errorf("line = %q, want it to say there is no baseline rather than omit it", line)
	}
}

// A branch pushed, rebased and pushed again cost ALL of it. Taking the last
// push would delete the rebase cycle from the baseline -- which is exactly
// the cost batching claims to remove, so removing it from the comparison
// would rig the comparison.
func TestTheBaselineCountsFromTheFirstPushNotTheLast(t *testing.T) {
	t0 := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	var evs []events.Event
	for i := 0; i < 3; i++ {
		at := t0.Add(time.Duration(i) * time.Hour)
		evs = append(evs,
			events.Event{At: at, Kind: events.KindPush, Key: "OR-" + string(rune('1'+i))},
			// A rebase: pushed again twenty minutes later.
			events.Event{At: at.Add(20 * time.Minute), Kind: events.KindPush,
				Key: "OR-" + string(rune('1'+i))},
			events.Event{At: at.Add(30 * time.Minute), Kind: events.KindMerge,
				Key: "OR-" + string(rune('1'+i))},
		)
	}

	if got := perBranchBaseline(writeLog(t, evs)); got.Median != 30*time.Minute {
		t.Errorf("median = %s, want 30m0s measured from the FIRST push: "+
			"counting from the last hides the rebase that batching removes", got.Median)
	}
}

// A batch emits no per-key push, so batch landings contribute no samples.
// Without that, every batch would poison the baseline it is measured against.
func TestBatchLandingsDoNotEnterTheBaseline(t *testing.T) {
	t0 := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	evs := []events.Event{
		// Three real per-branch landings.
		{At: t0, Kind: events.KindPush, Key: "OR-1"},
		{At: t0.Add(30 * time.Minute), Kind: events.KindMerge, Key: "OR-1"},
		{At: t0.Add(time.Hour), Kind: events.KindPush, Key: "OR-2"},
		{At: t0.Add(90 * time.Minute), Kind: events.KindMerge, Key: "OR-2"},
		{At: t0.Add(2 * time.Hour), Kind: events.KindPush, Key: "OR-3"},
		{At: t0.Add(150 * time.Minute), Kind: events.KindMerge, Key: "OR-3"},
		// A batch: merged with no push of its own.
		{At: t0.Add(3 * time.Hour), Kind: events.KindMerge, Key: "OR-9"},
	}

	got := perBranchBaseline(writeLog(t, evs))
	if got.Samples != 3 {
		t.Errorf("samples = %d, want 3: a batch landing has no push and must not "+
			"contribute to the baseline it is compared against", got.Samples)
	}
}

// The measurement must speak up when the batch LOST. One that only reports a
// saving is not a measurement.
func TestASlowerBatchIsReportedAsSlower(t *testing.T) {
	b := baseline{Median: 5 * time.Minute, Samples: 9}

	slow := costLine(1, 2, 30*time.Minute, b) // 30m against ~10m
	if !strings.Contains(slow, "SLOWER") {
		t.Errorf("line = %q, want it to say the batch was slower", slow)
	}

	fast := costLine(1, 4, 10*time.Minute, b) // 10m against ~20m
	if strings.Contains(fast, "SLOWER") {
		t.Errorf("line = %q, want no slower-claim when the batch won", fast)
	}
	if !strings.Contains(fast, "per-branch path took") {
		t.Errorf("line = %q, want the comparison stated", fast)
	}
}

// Elapsed covers assembly too, and is recorded even when the batch fails.
func TestElapsedCoversTheWholeCycleIncludingAFailure(t *testing.T) {
	clock := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	tick := func() time.Time {
		clock = clock.Add(time.Minute)
		return clock
	}
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-1": true}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1"), nil, WithClock(tick))
	if err != nil {
		t.Fatal(err)
	}
	if b.Elapsed <= 0 {
		t.Error("a batch that went red recorded no elapsed; it cost what it cost")
	}
}

// The done line leads with elapsed: it is the number a person came to the
// terminal with a question about.
func TestTheCostLineLeadsWithElapsed(t *testing.T) {
	line := costLine(1, 3, 18*time.Minute, baseline{Median: 20 * time.Minute, Samples: 5})
	if !strings.Contains(line, "18m") {
		t.Fatalf("line = %q, want the elapsed in it", line)
	}
	if i, j := strings.Index(line, "18m"), strings.Index(line, "median"); i > j && j >= 0 {
		t.Errorf("line = %q, want elapsed before the median: cost is the headline", line)
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }
