package cost

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// record runs one finished run through the single writer, both sinks.
func record(t *testing.T, log *events.Log, home, actor, key string, r Run) {
	t.Helper()
	if err := Record(log, home, actor, key, r); err != nil {
		t.Fatalf("recording a run: %v", err)
	}
}

func completedRun() Run {
	r := FromBudgetRun(budget.Run{
		Turns: 123, PromptTokens: 182, OutputTokens: 48_336,
		CacheCreateTokens: 178_093, CacheReadTokens: 14_835_264, CostUSD: 10.407,
	}, true, false, "completed", 1_139*time.Second)
	r.Model, r.Effort = "opus", "high"
	r.Project, r.Stage, r.Session = "OR", "implement", "415ee91a"
	return r
}

func TestOneCompletedRunAppendsOneHistoryRowWithModelAndEffort(t *testing.T) {
	home := t.TempDir()
	record(t, nil, home, events.ActorImplementer, "OR-193", completedRun())

	rows, err := ReadHistory(home)
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("appended %d rows for one run, want exactly 1 -- a duplicated "+
			"row double-counts a benchmark and a missing one loses it", len(rows))
	}
	row := rows[0]
	if row.Model != "opus" || row.Effort != "high" {
		t.Errorf("row recorded model %q effort %q, want opus/high -- without "+
			"both, no history can answer whether opus was worth it",
			row.Model, row.Effort)
	}
	if row.Key != "OR-193" || row.Project != "OR" || row.Actor != events.ActorImplementer {
		t.Errorf("row is not relatable: key %q project %q actor %q",
			row.Key, row.Project, row.Actor)
	}
	if row.Stage != "implement" || row.Session != "415ee91a" {
		t.Errorf("row stage %q session %q, want implement/415ee91a", row.Stage, row.Session)
	}
	if row.Started.IsZero() || row.Ended.IsZero() || !row.Ended.After(row.Started) {
		t.Errorf("row is not bounded in wall time: started %v ended %v",
			row.Started, row.Ended)
	}
	// Cache dominates: OR-184 read 37M cached tokens against 136k of output.
	// A row that drops the cache columns is measuring the wrong thing.
	if row.CacheR != 14_835_264 || row.CacheW != 178_093 {
		t.Errorf("cache columns lost: read %d create %d", row.CacheR, row.CacheW)
	}
	if row.CostUSD != 10.407 || row.Turns != 123 || row.Output != 48_336 {
		t.Errorf("token or cost columns lost: %+v", row)
	}
	if !row.UsageKnown {
		t.Error("a run that reported usage came back marked as not reporting it")
	}
}

// The whole reason model and effort are recorded per run: the roster is
// mutable, so a later lookup would answer for today's agents.json.
func TestChangingTheRosterDoesNotAlterHistoricalRows(t *testing.T) {
	home := t.TempDir()

	// Dispatched on whatever the roster says today, and recorded as such.
	r := completedRun()
	was := actors.Model(events.ActorImplementer)
	if was != r.Model {
		t.Fatalf("the shipped roster runs the implementer on %q, not %q; the "+
			"fixture no longer matches a real dispatch", was, r.Model)
	}
	record(t, nil, home, events.ActorImplementer, "OR-193", r)

	// Move the implementer to another model, exactly as editing agents.json
	// would.
	if err := actors.Configure(map[string]config.Agent{
		events.ActorImplementer: {Model: "sonnet", Effort: "low"},
	}); err != nil {
		t.Fatalf("reconfiguring the roster: %v", err)
	}
	t.Cleanup(func() { _ = actors.Configure(nil) })

	if now := actors.Model(events.ActorImplementer); now == was {
		t.Fatalf("the roster did not actually change (still %q)", now)
	}
	rows, err := ReadHistory(home)
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if rows[0].Model != was || rows[0].Effort != "high" {
		t.Errorf("the historical row now reads model %q effort %q, want %q/high "+
			"-- a row that follows the roster mislabels every run that came "+
			"before the change", rows[0].Model, rows[0].Effort, was)
	}
}

// The event log rotates by design; this file must not, which is the entire
// reason it is a second sink rather than a query over the first.
func TestEventLogRotationLeavesTheHistoryIntact(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()

	// A ceiling small enough to cross in a handful of events, restored after.
	origBytes, origFiles := events.MaxBytes, events.MaxFiles
	events.MaxBytes, events.MaxFiles = 1024, 2
	t.Cleanup(func() { events.MaxBytes, events.MaxFiles = origBytes, origFiles })

	log, err := events.Open(events.Path(dir), events.Event{})
	if err != nil {
		t.Fatalf("opening the event log: %v", err)
	}
	const runs = 40
	for i := 0; i < runs; i++ {
		record(t, log, home, events.ActorImplementer, "OR-193", completedRun())
	}
	_ = log.Close()

	// The event log rotated: its own history is now partial.
	kept := len(readUsageEvents(t, events.Path(dir)))
	if kept >= runs {
		t.Fatalf("the event log kept %d of %d usage events; it did not rotate, "+
			"so this test proves nothing", kept, runs)
	}

	rows, err := ReadHistory(home)
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if len(rows) != runs {
		t.Fatalf("the history holds %d of %d runs -- it was rotated or "+
			"truncated along with the event log, which is exactly what it "+
			"exists not to be", len(rows), runs)
	}
}

func readUsageEvents(t *testing.T, path string) []events.Event {
	t.Helper()
	var out []events.Event
	for _, e := range ReadAll(path) {
		if e.Kind == events.KindUsage {
			out = append(out, e)
		}
	}
	return out
}

// Two `orion watch` processes share one ORION_HOME and append at the same
// moment. The unlocked version of this pattern dropped one run in twelve
// (OR-138); goroutines here stand in for the processes.
func TestConcurrentWritersBothAppendWithoutLoss(t *testing.T) {
	home := t.TempDir()

	const writers, each = 12, 8
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				r := completedRun()
				r.Session = strings.Repeat("x", w+1)
				if err := Record(nil, home, events.ActorImplementer, "OR-193", r); err != nil {
					t.Errorf("writer %d: %v", w, err)
				}
			}
		}(w)
	}
	wg.Wait()

	rows, err := ReadHistory(home)
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if len(rows) != writers*each {
		t.Fatalf("%d rows survived %d concurrent appends -- a lost or "+
			"interleaved row is a run that silently never happened",
			len(rows), writers*each)
	}
}

// A run that died before its result JSON still spent everything it sent. It
// gets a row, marked, so a missing row stays distinguishable from a zero.
func TestARunThatNeverReportedUsageStillGetsAMarkedRow(t *testing.T) {
	home := t.TempDir()

	r := FromBudgetRun(budget.Run{}, false, true, "claude exited 1", 5*time.Second)
	r.Model, r.Effort, r.Stage = "opus", "high", "implement"
	record(t, nil, home, events.ActorImplementer, "OR-193", r)

	rows, err := ReadHistory(home)
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("appended %d rows, want 1 -- dropping the row makes a run that "+
			"spent tokens look like one that never ran", len(rows))
	}
	if rows[0].UsageKnown {
		t.Error("the row claims usage was reported; a zero that is really a gap " +
			"is how a lowball total gets presented as complete")
	}
	if rows[0].Exit == 0 {
		t.Error("the row records exit 0 for a run that died")
	}
	if rows[0].Model != "opus" {
		t.Errorf("the dead run lost its model (%q); it is still a data point "+
			"about that model", rows[0].Model)
	}
}

// The overwhelmingly common row -- a run that DID start -- must serialize
// exactly as it did before NeverStarted existed. omitempty is what makes
// that true; without it every historical row gains a `"never_started":false`
// column no earlier reader or query expects.
func TestAStartedRunsHistoryRowOmitsTheNeverStartedField(t *testing.T) {
	home := t.TempDir()
	record(t, nil, home, events.ActorImplementer, "OR-193", completedRun())

	raw, err := os.ReadFile(HistoryPath(home))
	if err != nil {
		t.Fatalf("reading the raw history file: %v", err)
	}
	if strings.Contains(string(raw), "never_started") {
		t.Errorf("a run that started wrote a never_started field, breaking "+
			"byte-comparability with rows written before OR-219:\n%s", raw)
	}

	// And the field appears, true, exactly when the run actually never
	// started -- so the omission above is "false", not "absent from the type".
	r := FromBudgetRun(budget.Run{}, false, true, "claude exited 1", 5*time.Second)
	r.NeverStarted = true
	record(t, nil, home, events.ActorImplementer, "OR-193", r)

	raw, err = os.ReadFile(HistoryPath(home))
	if err != nil {
		t.Fatalf("reading the raw history file: %v", err)
	}
	if !strings.Contains(string(raw), `"never_started":true`) {
		t.Errorf("a never-started run's history row does not carry the flag:\n%s", raw)
	}
}

// Both sinks come from one function, so neither can be written without the
// other. Model and effort have to survive the event log's JSON round trip too.
func TestRecordWritesBothSinksFromOneCall(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()

	log, err := events.Open(events.Path(dir), events.Event{})
	if err != nil {
		t.Fatalf("opening the event log: %v", err)
	}
	record(t, log, home, events.ActorImplementer, "OR-193", completedRun())
	_ = log.Close()

	evs := readUsageEvents(t, events.Path(dir))
	if len(evs) != 1 {
		t.Fatalf("the event log holds %d usage events, want 1", len(evs))
	}
	run := runFrom(evs[0])
	if run.Model != "opus" || run.Effort != "high" {
		t.Errorf("the usage event reads model %q effort %q, want opus/high",
			run.Model, run.Effort)
	}
	if _, err := os.Stat(HistoryPath(home)); err != nil {
		t.Fatalf("the same call wrote no history row: %v", err)
	}
}
