package web

// OR-59 QA coverage: the two on-disk edge cases from fixture_logs_test.go's
// table, broken into one dedicated case per rule so a regression in any one
// of them fails by name instead of by a table row that bundles several
// claims into one field-by-field diff.

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// truncatedFixture is one run cut off mid-append: three complete events, plus
// a fourth line that never got its closing bytes. Shared by every truncated-
// log case below so they all reason about the same on-disk log.
func truncatedFixture(t *testing.T, path string) []events.Event {
	t.Helper()
	writeRunLog(t, path, []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Edit internal/web/cards.go"},
	})
	appendRaw(t, path, `{"at":"2026-09-09T13:34:04Z","kind":"tool","key":"OR-59","run":"r1","msg":"Edit internal/w`)

	evs, err := events.Read(path)
	if err != nil {
		t.Fatalf("events.Read(%s) = %v, want the complete prefix to read cleanly", path, err)
	}
	return evs
}

// The half-written line must not surface as a fourth event: a reader that
// turned it into anything -- even a zero-value one -- would draw a step that
// never actually happened.
func TestScanTruncatedFinalLineIsNotIncludedInScanResult(t *testing.T) {
	evs := truncatedFixture(t, events.Path(t.TempDir()))
	if got, want := len(evs), 3; got != want {
		t.Fatalf("events.Read returned %d events, want %d (the truncated line must be dropped, not one of them)", got, want)
	}
}

// The three events written before the cut are undamaged evidence and must
// still produce their card: a reader that discarded the whole file on a bad
// tail would throw away everything the run did up to the moment it died.
func TestScanTruncatedFinalLineCompleteEventsBeforeItProduceCorrectCard(t *testing.T) {
	evs := truncatedFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	if got, want := cards[0].Key, "OR-59"; got != want {
		t.Errorf("cards[0].Key = %q, want %q", got, want)
	}
	if got, want := cards[0].Session.Steps, 2; got != want {
		t.Errorf("cards[0].Session.Steps = %d, want %d: only the two complete tool calls count", got, want)
	}
}

// Activity is the newest thing the agent said, and the newest thing it said
// completely: the half-written "Edit internal/w" must not leak through as
// the card's activity, since that string names no file that exists.
func TestScanTruncatedFinalLineActivityShowsLastCompleteEvent(t *testing.T) {
	evs := truncatedFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Activity, "Edit internal/web/cards.go"; got != want {
		t.Errorf("cards[0].Session.Activity = %q, want %q (the truncated line, not the last complete one)", got, want)
	}
}

// Elapsed is first event to last, and "last" must stop at the last event that
// actually parsed. The truncated line's timestamp (13:34:04, four minutes in)
// is never seen at all, so a card that counted it would report an elapsed
// time the log never established.
func TestScanTruncatedFinalLineElapsedReflectsOnlyCompleteEvents(t *testing.T) {
	evs := truncatedFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Elapsed(), 2*time.Minute; got != want {
		t.Errorf("cards[0].Session.Elapsed() = %s, want %s (bounded by the last complete event, not the truncated one)", got, want)
	}
}

// concurrentFixture is two runs of one ticket, alive at once, their lines
// interleaved in the single log both write to -- an implementer still
// working while CI's rerun of the same ticket starts. Shared by every
// concurrent-runs case below.
func concurrentFixture(t *testing.T, path string) []events.Event {
	t.Helper()
	writeRunLog(t, path, []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindRunStart, Key: "OR-59", Run: "r2"},
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
		{At: base.Add(3 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r2", Msg: "Grep internal/web/timing.go"},
		{At: base.Add(5 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r2", Msg: "Edit internal/web/timing.go"},
		{At: base.Add(6 * time.Minute), Kind: events.KindRunEnd, Key: "OR-59", Run: "r1"},
	})

	evs, err := events.Read(path)
	if err != nil {
		t.Fatalf("events.Read(%s) = %v, want the log to read cleanly", path, err)
	}
	return evs
}

// The fixture itself: both runs' lines must actually land in the one file at
// their written positions, interleaved rather than segregated, or every case
// below would be proving something about two separate logs instead of one
// workspace's single log carrying two runs at once.
func TestScanConcurrentRunsEventsFromBothRunsAreInterleavedInOneLogFile(t *testing.T) {
	evs := concurrentFixture(t, events.Path(t.TempDir()))
	if got, want := len(evs), 6; got != want {
		t.Fatalf("events.Read returned %d events, want %d", got, want)
	}

	var r1, r2 int
	for i, e := range evs {
		switch e.Run {
		case "r1":
			r1++
		case "r2":
			r2++
		default:
			t.Fatalf("evs[%d].Run = %q, want %q or %q", i, e.Run, "r1", "r2")
		}
	}
	if r1 != 3 || r2 != 3 {
		t.Fatalf("r1, r2 event counts = %d, %d, want 3, 3 (both runs' lines must be present in the one file)", r1, r2)
	}
	// r1 and r2 alternate rather than sitting in two separate runs of lines --
	// index 2 (r1) sits between two r2 lines at index 1 and 3.
	if evs[1].Run != "r2" || evs[2].Run != "r1" || evs[3].Run != "r2" {
		t.Fatalf("events are not interleaved: evs[1..3].Run = %q, %q, %q, want %q, %q, %q",
			evs[1].Run, evs[2].Run, evs[3].Run, "r2", "r1", "r2")
	}
}

// The grouping decision this whole fixture exists to pin: two runs of one key
// in one file must become two cards, not one. Folding them by key alone would
// report a single three-step card spanning both runs, and neither run took
// three steps.
func TestScanConcurrentRunsInOneWorkspaceProduceTwoSeparateCardsNotMerged(t *testing.T) {
	evs := concurrentFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := len(cards), 2; got != want {
		t.Fatalf("Scan returned %d cards, want %d (the two runs must not merge into one)", got, want)
	}
	if cards[0].Key != "OR-59" || cards[1].Key != "OR-59" {
		t.Fatalf("cards[0].Key, cards[1].Key = %q, %q, want both %q", cards[0].Key, cards[1].Key, "OR-59")
	}
}

// Each card counts only its own run's tool calls. A merged three-step count
// would be the tell that grouping folded the runs together.
func TestScanConcurrentRunsEachCardHasIndependentStepCount(t *testing.T) {
	evs := concurrentFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Steps, 1; got != want {
		t.Errorf("cards[0].Session.Steps = %d, want %d (r1 took one tool call)", got, want)
	}
	if got, want := cards[1].Session.Steps, 2; got != want {
		t.Errorf("cards[1].Session.Steps = %d, want %d (r2 took two tool calls)", got, want)
	}
}

// Each card is timed from its own first event to its own last, not from the
// earliest event in the file to the latest. r1 started first but r2's second
// run is still what r2 alone spans.
func TestScanConcurrentRunsEachCardHasIndependentTiming(t *testing.T) {
	evs := concurrentFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Started, base; !got.Equal(want) {
		t.Errorf("cards[0].Session.Started = %s, want %s", got, want)
	}
	if got, want := cards[0].Session.Elapsed(), 6*time.Minute; got != want {
		t.Errorf("cards[0].Session.Elapsed() = %s, want %s", got, want)
	}
	if got, want := cards[1].Session.Started, base.Add(time.Minute); !got.Equal(want) {
		t.Errorf("cards[1].Session.Started = %s, want %s", got, want)
	}
	if got, want := cards[1].Session.Elapsed(), 4*time.Minute; got != want {
		t.Errorf("cards[1].Session.Elapsed() = %s, want %s", got, want)
	}
}

// Each card's activity and done-state describe only that run. r1 ended and
// last spoke by reading a file; r2 is still going and last spoke by editing a
// different one -- a card showing the other run's activity or done-state
// would mean the grouping leaked between them even while producing the right
// count.
func TestScanConcurrentRunsEachCardShowsOnlyItsOwnActivityAndState(t *testing.T) {
	evs := concurrentFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Activity, "Read internal/web/cards.go"; got != want {
		t.Errorf("cards[0].Session.Activity = %q, want %q", got, want)
	}
	if !cards[0].Session.Done {
		t.Error("cards[0].Session.Done = false, want true: r1's log carries its run-end")
	}

	if got, want := cards[1].Session.Activity, "Edit internal/web/timing.go"; got != want {
		t.Errorf("cards[1].Session.Activity = %q, want %q", got, want)
	}
	if cards[1].Session.Done {
		t.Error("cards[1].Session.Done = true, want false: r2 has no run-end in this log")
	}
}

// A log file that exists but cannot be opened (mode 0 -- no read bit for
// anyone) must report a permission error, not fs.ErrNotExist. The two are
// different facts about a workspace: "nothing has run yet" and "something
// ran but this reader cannot see it" call for different responses from a
// caller, and events.Read must let them tell it apart.
//
// Skipped when running as root: root ignores the read-permission bit
// entirely, so the open would succeed and the case would prove nothing.
func TestScanUnreadableLogReportsPermissionErrorNotAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits don't apply on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-permission bit; the fixture can't force a permission error")
	}

	path := events.Path(t.TempDir())
	writeRunLog(t, path, []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
	})
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod fixture log unreadable: %v", err)
	}

	_, err := events.Read(path)
	if err == nil {
		t.Fatal("events.Read of an unreadable log = nil error, want a permission error")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("events.Read(%s) error = %v, reported as \"absent\" -- want it distinguished as a permission error", path, err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("events.Read(%s) error = %v, want it to satisfy fs.ErrPermission", path, err)
	}
}

// threeConcurrentFixture is three runs of one ticket, alive in overlapping
// windows in the single log all three write to. Beyond concurrentFixture's
// two runs, this is the case that would tempt an implementation into
// "group everything but the first, oldest run" or some other two-case
// special-casing that happens to work for a pair but breaks past it.
func threeConcurrentFixture(t *testing.T, path string) []events.Event {
	t.Helper()
	writeRunLog(t, path, []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindRunStart, Key: "OR-59", Run: "r2"},
		{At: base.Add(2 * time.Minute), Kind: events.KindRunStart, Key: "OR-59", Run: "r3"},
		{At: base.Add(3 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
		{At: base.Add(4 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r2", Msg: "Grep internal/web/timing.go"},
		{At: base.Add(5 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r3", Msg: "Read internal/web/scan.go"},
		{At: base.Add(6 * time.Minute), Kind: events.KindRunEnd, Key: "OR-59", Run: "r1"},
		{At: base.Add(7 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r2", Msg: "Edit internal/web/timing.go"},
		{At: base.Add(8 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r3", Msg: "Edit internal/web/scan.go"},
		{At: base.Add(9 * time.Minute), Kind: events.KindRunEnd, Key: "OR-59", Run: "r3"},
	})

	evs, err := events.Read(path)
	if err != nil {
		t.Fatalf("events.Read(%s) = %v, want the log to read cleanly", path, err)
	}
	return evs
}

// Three or more concurrent runs in one workspace must each produce their own
// card -- not just the pairwise case a two-run implementation might special-
// case its way through.
func TestScanThreeConcurrentRunsProduceThreeSeparateCards(t *testing.T) {
	evs := threeConcurrentFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := len(cards), 3; got != want {
		t.Fatalf("Scan returned %d cards, want %d (three concurrent runs must not merge into fewer)", got, want)
	}
	for i, c := range cards {
		if c.Key != "OR-59" {
			t.Errorf("cards[%d].Key = %q, want %q", i, c.Key, "OR-59")
		}
	}
}

// No run's step count or timing bleeds into another's. A card built by
// folding step counts or extending timing windows across runs would show a
// count or span none of the three runs actually had.
func TestScanThreeConcurrentRunsStepCountsAndTimingAreNotMergedAcrossRuns(t *testing.T) {
	evs := threeConcurrentFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Steps, 1; got != want {
		t.Errorf("cards[0].Session.Steps = %d, want %d (r1 took one tool call)", got, want)
	}
	if got, want := cards[0].Session.Started, base; !got.Equal(want) {
		t.Errorf("cards[0].Session.Started = %s, want %s", got, want)
	}
	if got, want := cards[0].Session.Elapsed(), 6*time.Minute; got != want {
		t.Errorf("cards[0].Session.Elapsed() = %s, want %s", got, want)
	}

	if got, want := cards[1].Session.Steps, 2; got != want {
		t.Errorf("cards[1].Session.Steps = %d, want %d (r2 took two tool calls)", got, want)
	}
	if got, want := cards[1].Session.Started, base.Add(time.Minute); !got.Equal(want) {
		t.Errorf("cards[1].Session.Started = %s, want %s", got, want)
	}
	if got, want := cards[1].Session.Elapsed(), 6*time.Minute; got != want {
		t.Errorf("cards[1].Session.Elapsed() = %s, want %s", got, want)
	}

	if got, want := cards[2].Session.Steps, 2; got != want {
		t.Errorf("cards[2].Session.Steps = %d, want %d (r3 took two tool calls)", got, want)
	}
	if got, want := cards[2].Session.Started, base.Add(2*time.Minute); !got.Equal(want) {
		t.Errorf("cards[2].Session.Started = %s, want %s", got, want)
	}
	if got, want := cards[2].Session.Elapsed(), 7*time.Minute; got != want {
		t.Errorf("cards[2].Session.Elapsed() = %s, want %s", got, want)
	}
}

// Each card's activity and done-state reflect only that run's own last event
// -- not the last event in the file, and not another run's terminal state.
func TestScanThreeConcurrentRunsActivityAndDoneStateAreNotMergedAcrossRuns(t *testing.T) {
	evs := threeConcurrentFixture(t, events.Path(t.TempDir()))
	cards := Scan(evs, nil)

	if got, want := cards[0].Session.Activity, "Read internal/web/cards.go"; got != want {
		t.Errorf("cards[0].Session.Activity = %q, want %q", got, want)
	}
	if !cards[0].Session.Done {
		t.Error("cards[0].Session.Done = false, want true: r1's log carries its run-end")
	}

	if got, want := cards[1].Session.Activity, "Edit internal/web/timing.go"; got != want {
		t.Errorf("cards[1].Session.Activity = %q, want %q", got, want)
	}
	if cards[1].Session.Done {
		t.Error("cards[1].Session.Done = true, want false: r2 has no run-end in this log")
	}

	if got, want := cards[2].Session.Activity, "Edit internal/web/scan.go"; got != want {
		t.Errorf("cards[2].Session.Activity = %q, want %q", got, want)
	}
	if !cards[2].Session.Done {
		t.Error("cards[2].Session.Done = false, want true: r3's log carries its run-end")
	}
}
