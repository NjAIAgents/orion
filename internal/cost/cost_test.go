package cost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/events"
)

// ticket writes a lifecycle to a real event log: an implementation run, the
// resumed run after an advisor answered, a fix-loop re-entry, a run that died,
// and a run that never reported usage -- plus a second ticket's run, which
// must not leak into the first one's total.
//
// Written through events.Log rather than constructed in memory on purpose.
// The detail map round-trips through JSON on the way to disk, so every number
// comes back as a float64; a test that skipped the file would pass on a
// reader that cannot read what the writer wrote.
func ticket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := events.Path(dir)
	log, err := events.Open(path, events.Event{})
	if err != nil {
		t.Fatalf("opening the event log: %v", err)
	}
	defer log.Close()

	// Empty home: this fixture exercises the event-log sink only. The durable
	// history sink has its own tests in history_test.go.
	rec := func(actor, key string, b budget.Run, ok, failed bool, reason string, secs int) {
		if err := Record(log, "", actor, key, FromBudgetRun(b, ok, failed, reason,
			time.Duration(secs)*time.Second)); err != nil {
			t.Fatalf("recording a run: %v", err)
		}
	}

	rec(events.ActorImplementer, "OR-9", budget.Run{
		Turns: 34, PromptTokens: 12_410, OutputTokens: 8_922,
		CacheCreateTokens: 41_203, CacheReadTokens: 1_203_554, CostUSD: 3.84,
	}, true, false, "completed", 724)

	rec(events.ActorImplementer, "OR-9", budget.Run{
		Turns: 6, PromptTokens: 100, OutputTokens: 50,
		CacheCreateTokens: 1_000, CacheReadTokens: 5_000, CostUSD: 0.16,
	}, true, false, "completed", 60)

	rec(events.ActorDevOps, "OR-9", budget.Run{
		Turns: 12, PromptTokens: 2_000, OutputTokens: 900,
		CacheCreateTokens: 3_000, CacheReadTokens: 200_000, CostUSD: 0.41,
	}, true, false, "completed", 180)

	// Died on the wall clock. It spent what it spent before it was killed.
	rec(events.ActorDevOps, "OR-9", budget.Run{
		Turns: 40, PromptTokens: 500, OutputTokens: 200,
		CacheCreateTokens: 0, CacheReadTokens: 50_000, CostUSD: 0.09,
	}, true, true, "timed out", 1_800)

	// Crashed before its result JSON: a run, with no numbers to show for it.
	rec(events.ActorImplementer, "OR-9", budget.Run{}, false, true, "claude exited 1", 5)

	// Another ticket entirely.
	rec(events.ActorImplementer, "OR-10", budget.Run{
		Turns: 99, PromptTokens: 999_999, OutputTokens: 999_999, CostUSD: 99.99,
	}, true, false, "completed", 10)

	return path
}

func TestAggregateCountsEveryRunForTheTicket(t *testing.T) {
	rep := Aggregate(ReadAll(ticket(t)), "OR-9")

	if got := len(rep.Runs); got != 5 {
		t.Fatalf("aggregated %d runs, want 5 -- a report that counts only some "+
			"of a ticket's runs understates what it cost", got)
	}
	if got, want := rep.Total.Turns, 34+6+12+40; got != want {
		t.Errorf("total turns %d, want %d", got, want)
	}
	if got, want := rep.Total.Prompt, 12_410+100+2_000+500; got != want {
		t.Errorf("total input %d, want %d", got, want)
	}
	if got, want := rep.Total.Output, 8_922+50+900+200; got != want {
		t.Errorf("total output %d, want %d", got, want)
	}
	// The cache pair stays apart from input and from each other: cache reads
	// are billed at a fraction of fresh input, so a sum misstates both.
	if got, want := rep.Total.CacheW, 41_203+1_000+3_000; got != want {
		t.Errorf("total cache creation %d, want %d", got, want)
	}
	if got, want := rep.Total.CacheR, 1_203_554+5_000+200_000+50_000; got != want {
		t.Errorf("total cache read %d, want %d", got, want)
	}
	if got, want := rep.Total.CostUSD, 3.84+0.16+0.41+0.09; got < want-0.001 || got > want+0.001 {
		t.Errorf("total cost %.2f, want %.2f", got, want)
	}
	if got, want := rep.Total.Seconds, float64(724+60+180+1800+5); got != want {
		t.Errorf("total wall time %.0fs, want %.0fs", got, want)
	}
	if rep.Total.Failed != 2 {
		t.Errorf("counted %d failed runs, want 2 -- a run that died still spent tokens",
			rep.Total.Failed)
	}
	if rep.Total.Missing != 1 {
		t.Errorf("counted %d runs with no usage, want 1", rep.Total.Missing)
	}
}

func TestAggregateAttributesPerActor(t *testing.T) {
	rep := Aggregate(ReadAll(ticket(t)), "OR-9")

	rows := map[string]Row{}
	for _, r := range rep.Rows {
		rows[r.ID] = r
	}
	if len(rows) != 2 {
		t.Fatalf("got %d actor rows, want 2: %+v", len(rows), rep.Rows)
	}

	impl := rows[events.ActorImplementer]
	if impl.Runs != 3 || impl.Turns != 40 {
		t.Errorf("implementer: %d runs / %d turns, want 3 / 40 -- a fix-loop "+
			"re-entry is another run for the same actor", impl.Runs, impl.Turns)
	}
	if impl.CacheR != 1_203_554+5_000 {
		t.Errorf("implementer cache reads %d, want %d", impl.CacheR, 1_203_554+5_000)
	}

	ops := rows[events.ActorDevOps]
	if ops.Runs != 2 || ops.Failed != 1 {
		t.Errorf("devops: %d runs / %d failed, want 2 / 1", ops.Runs, ops.Failed)
	}
	if got, want := ops.CostUSD, 0.41+0.09; got < want-0.001 || got > want+0.001 {
		t.Errorf("devops cost %.2f, want %.2f", got, want)
	}

	// Rows carry the registry's display form, not the persisted id, so a
	// renamed actor still reads correctly in an old log.
	if !strings.Contains(impl.Actor, "·") {
		t.Errorf("actor rendered as %q, want the registry's name and designation", impl.Actor)
	}
	if impl.Actor == impl.ID {
		t.Errorf("row shows the raw id %q rather than a display name", impl.ID)
	}
}

func TestAggregateIgnoresOtherTicketsAndOtherEvents(t *testing.T) {
	path := ticket(t)
	log, err := events.Open(path, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	log.Emit(events.Event{Kind: events.KindTool, Actor: events.ActorImplementer,
		Key: "OR-9", Msg: "Read something"})
	log.Close()

	rep := Aggregate(ReadAll(path), "OR-9")
	if len(rep.Runs) != 5 {
		t.Errorf("a non-usage event was counted as a run: %d runs", len(rep.Runs))
	}
	if rep.Total.CostUSD > 5 {
		t.Errorf("another ticket's spend leaked in: $%.2f", rep.Total.CostUSD)
	}
}

func TestRenderStatesEveryThingTheReportPromises(t *testing.T) {
	out := Render(Aggregate(ReadAll(ticket(t)), "OR-9"))

	for _, want := range []string{
		"cost report OR-9",   // named on the opening rule
		"end of cost report", // and again on the closing one
		"cache w", "cache r", // the pair, reported separately
		"1,208,554", // grouped so a seven-figure count is readable
		"$4.50",     // the total, as reported by the runner
		"total",
		"failed",        // a run that died is marked, not hidden
		"usage missing", // and one that reported nothing is stated
		"FLOOR",         // so the total is not presented as complete
		"estimates",     // never presented as a settled bill
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
}

func TestRenderSaysSoWhenNothingWasRecorded(t *testing.T) {
	out := Render(Aggregate(nil, "OR-9"))
	if !strings.Contains(out, "No per-run usage was recorded") {
		t.Errorf("a ticket with no usage must say so rather than show a $0 table:\n%s", out)
	}
	if strings.Contains(out, "$0.00") {
		t.Errorf("rendered a zero total for a ticket nothing is known about:\n%s", out)
	}
}

// A long-running ticket rotates its own early runs into events.jsonl.1. Those
// runs are exactly the expensive ones, so a reader that starts at the current
// file reports a large ticket as a small one.
func TestReadAllIncludesRotatedGenerations(t *testing.T) {
	dir := t.TempDir()
	path := events.Path(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	old, err := events.Open(path, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	Record(old, "", events.ActorImplementer, "OR-9", FromBudgetRun(
		budget.Run{Turns: 10, PromptTokens: 5, CostUSD: 2}, true, false, "completed", 30))
	old.Close()
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}

	cur, err := events.Open(path, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	Record(cur, "", events.ActorImplementer, "OR-9", FromBudgetRun(
		budget.Run{Turns: 1, PromptTokens: 5, CostUSD: 0.5}, true, false, "completed", 5))
	cur.Close()

	rep := Aggregate(ReadAll(path), "OR-9")
	if len(rep.Runs) != 2 || rep.Total.CostUSD != 2.5 {
		t.Errorf("rotated runs were dropped: %d runs, $%.2f", len(rep.Runs), rep.Total.CostUSD)
	}
}
