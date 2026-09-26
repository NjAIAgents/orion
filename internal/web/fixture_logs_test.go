package web

// OR-59: the card derivation exercised over LOG FILES, not over event slices.
//
// Every other test in this package hands Scan a slice it built in memory,
// which proves the grouping but skips the half that actually breaks in the
// field: the file. A workspace's log can be absent, empty, cut off mid-line
// by a kill, carrying two runs at once, or missing its first half because the
// writer rotated it. None of those states is reachable from a slice literal,
// and each of them has already produced a card that describes no run that
// happened -- or a reader that returned an error where "nothing has run yet"
// was the honest answer.
//
// ONE TABLE, ONE PATH THROUGH THE PIPELINE. Each case lays a workspace out on
// disk, then runs the same events.Read -> Scan the page will run, and states
// the cards that must come back. A new edge case is a row, not another copy
// of the plumbing -- which is what keeps the next one cheap enough to be
// written at all.
//
// THE FIXTURES GO THROUGH THE REAL WRITER. events.Open/Emit produce the bytes,
// and the rotation case drives the writer's OWN rotation rather than renaming
// files aside by hand, so these tests read back the format and the on-disk
// shape Orion actually leaves behind. A hand-rolled fixture proves the test's
// idea of the log, not the log.

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// wantCard is one expected card, stated in the terms the page reads: whose
// ticket, how far in, what it is doing, when it started and for how long.
// Started and elapsed are offsets from base so a row reads as a timeline.
type wantCard struct {
	key      string
	steps    int
	done     bool
	activity string
	started  time.Duration
	elapsed  time.Duration
}

func TestScanOverFixtureLogs(t *testing.T) {
	// One run, cut off by a kill: the tail of the file is half an event.
	truncated := []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Edit internal/web/cards.go"},
	}

	// Two runs alive at once in one workspace, their lines interleaved in the
	// single log both write to -- an implementer still working while CI's
	// rerun of the same ticket starts.
	concurrent := []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindRunStart, Key: "OR-59", Run: "r2"},
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
		{At: base.Add(3 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r2", Msg: "Grep internal/web/timing.go"},
		{At: base.Add(5 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r2", Msg: "Edit internal/web/timing.go"},
		{At: base.Add(6 * time.Minute), Kind: events.KindRunEnd, Key: "OR-59", Run: "r1"},
	}

	// One run whose first three events were rotated into events.jsonl.1, so
	// the file at the live path opens in the middle of the run.
	rotated := []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Grep internal/web/timing.go"},
		{At: base.Add(3 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Edit internal/web/timing.go"},
		{At: base.Add(4 * time.Minute), Kind: events.KindRunEnd, Key: "OR-59", Run: "r1"},
	}

	// One run whose log picked up a corrupted line partway through -- not
	// the truncated-tail case below, where the cut is at EOF, but a
	// complete line of garbage a hand-edit or a partial overwrite can leave
	// mid-file. events.Read must skip it like any other malformed line, and
	// Scan must still assemble a card from what did parse.
	malformedHead := []events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-59", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Read internal/web/cards.go"},
	}
	malformedTail := []events.Event{
		{At: base.Add(2 * time.Minute), Kind: events.KindTool, Key: "OR-59", Run: "r1", Msg: "Edit internal/web/cards.go"},
		{At: base.Add(3 * time.Minute), Kind: events.KindRunEnd, Key: "OR-59", Run: "r1"},
	}

	cases := []struct {
		name string
		// write lays the workspace's log out at path. Nil writes nothing --
		// the workspace exists, the log does not.
		write func(t *testing.T, path string)
		// readErr is what events.Read must report, if anything. Stated rather
		// than merely tolerated: "no log" and "unreadable log" are different
		// answers and a caller has to be able to tell them apart.
		readErr error
		want    []wantCard
	}{{
		// Nothing has ever run here. The distinguishing signal must be the
		// error, not an empty result: a page that cannot tell a fresh
		// workspace from an unreadable log shows "no runs" for both.
		name:    "absent log",
		readErr: fs.ErrNotExist,
	}, {
		// The writer opened the file and the run said nothing before it went
		// away -- a claimed ticket whose agent never started. Zero bytes is a
		// legitimate log, not a corrupt one, so the read succeeds and the
		// grid is simply empty.
		name: "empty log",
		write: func(t *testing.T, path string) {
			writeRunLog(t, path, nil)
		},
	}, {
		// Killed mid-append: the last line is half an event with no newline.
		// Everything written before the cut is evidence and must survive --
		// discarding the whole file would throw the log away at the exact
		// moment somebody needs to know what the run was doing when it died.
		name: "truncated final line",
		write: func(t *testing.T, path string) {
			writeRunLog(t, path, truncated)
			appendRaw(t, path, `{"at":"2026-09-09T13:34:04Z","kind":"tool","key":"OR-59","run":"r1","msg":"Edit internal/w`)
		},
		want: []wantCard{{
			key:      "OR-59",
			steps:    2,
			activity: "Edit internal/web/cards.go",
			started:  0,
			elapsed:  2 * time.Minute,
		}},
	}, {
		// Two runs, one file. Each keeps its own timing, step count and
		// activity: folding them by key would report one card of three steps
		// spanning both, and neither run took three steps.
		name: "two concurrent runs in one workspace",
		write: func(t *testing.T, path string) {
			writeRunLog(t, path, concurrent)
		},
		want: []wantCard{{
			key:      "OR-59",
			steps:    1,
			done:     true,
			activity: "Read internal/web/cards.go",
			started:  0,
			elapsed:  6 * time.Minute,
		}, {
			key:      "OR-59",
			steps:    2,
			activity: "Edit internal/web/timing.go",
			started:  time.Minute,
			elapsed:  4 * time.Minute,
		}},
	}, {
		// Rotated: the live file no longer holds the run's run-start. A card
		// must still be drawn from what is left, timed by the oldest event
		// that survived, because a derivation that needs a run-start goes
		// blank on every long run -- which is precisely the run worth
		// watching.
		name: "rotated log",
		write: func(t *testing.T, path string) {
			writeRotatedLog(t, path, rotated, 3)
		},
		want: []wantCard{{
			key:      "OR-59",
			steps:    1,
			done:     true,
			activity: "Edit internal/web/timing.go",
			started:  3 * time.Minute,
			elapsed:  time.Minute,
		}},
	}, {
		// The corrupted line sits between two otherwise-valid events, with a
		// newline of its own -- a complete line, unlike the truncated tail
		// above. Only it should vanish; both halves of the run must survive
		// and fold into one card.
		name: "malformed JSON mid-file",
		write: func(t *testing.T, path string) {
			writeRunLog(t, path, malformedHead)
			appendRaw(t, path, "not json at all\n")
			writeRunLog(t, path, malformedTail)
		},
		want: []wantCard{{
			key:      "OR-59",
			steps:    2,
			done:     true,
			activity: "Edit internal/web/cards.go",
			started:  0,
			elapsed:  3 * time.Minute,
		}},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := events.Path(t.TempDir())
			if tc.write != nil {
				tc.write(t, path)
			}

			evs, err := events.Read(path)
			switch {
			case tc.readErr != nil && !errors.Is(err, tc.readErr):
				t.Fatalf("events.Read(%s) error = %v, want %v", path, err, tc.readErr)
			case tc.readErr == nil && err != nil:
				t.Fatalf("events.Read(%s) = %v, want the log to read cleanly", path, err)
			}

			cards := Scan(evs, nil)
			if got, want := len(cards), len(tc.want); got != want {
				t.Fatalf("Scan returned %d cards, want %d: %+v", got, want, cards)
			}
			for i, want := range tc.want {
				got := cards[i]
				if got.Key != want.key {
					t.Errorf("cards[%d].Key = %q, want %q", i, got.Key, want.key)
				}
				if got.Session.Steps != want.steps {
					t.Errorf("cards[%d].Session.Steps = %d, want %d", i, got.Session.Steps, want.steps)
				}
				if got.Session.Done != want.done {
					t.Errorf("cards[%d].Session.Done = %t, want %t", i, got.Session.Done, want.done)
				}
				if got.Session.Activity != want.activity {
					t.Errorf("cards[%d].Session.Activity = %q, want %q", i, got.Session.Activity, want.activity)
				}
				if wantStart := base.Add(want.started); !got.Session.Started.Equal(wantStart) {
					t.Errorf("cards[%d].Session.Started = %s, want %s", i, got.Session.Started, wantStart)
				}
				if got.Session.Elapsed() != want.elapsed {
					t.Errorf("cards[%d].Session.Elapsed() = %s, want %s", i, got.Session.Elapsed(), want.elapsed)
				}
			}
		})
	}
}

// writeRunLog writes evs to path through the real writer, creating the
// workspace's .orion directory the way a run would.
func writeRunLog(t *testing.T, path string, evs []events.Event) {
	t.Helper()
	log, err := events.Open(path, events.Event{})
	if err != nil {
		t.Fatalf("open fixture log: %v", err)
	}
	for _, e := range evs {
		log.Emit(e)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close fixture log: %v", err)
	}
}

// writeRotatedLog writes evs through the real writer, rotating it just before
// the event at index after -- so evs[:after] end up in events.jsonl.1 and the
// rest in events.jsonl.
//
// The split is forced by dropping the size ceiling to a byte for exactly one
// event, rather than by padding events until the real ceiling happens to be
// crossed. Same rotation code either way, but the boundary lands where the
// test says it does instead of where the JSON encoding put it, so the
// expected cards are a fact about the fixture and not about a byte count.
func writeRotatedLog(t *testing.T, path string, evs []events.Event, after int) {
	t.Helper()
	maxBytes, maxFiles := events.MaxBytes, events.MaxFiles
	t.Cleanup(func() { events.MaxBytes, events.MaxFiles = maxBytes, maxFiles })
	// Keep every generation: a fixture that silently dropped the archive
	// would still pass the current-file assertions for the wrong reason.
	events.MaxFiles = len(evs) + 1

	log, err := events.Open(path, events.Event{})
	if err != nil {
		t.Fatalf("open fixture log: %v", err)
	}
	for i, e := range evs {
		if i == after {
			events.MaxBytes = 1
		} else {
			events.MaxBytes = maxBytes
		}
		log.Emit(e)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close fixture log: %v", err)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("fixture did not rotate: %v", err)
	}
}

// appendRaw appends bytes to the log without a trailing newline, which is
// what a killed writer leaves behind and what no Emit can produce.
func appendRaw(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("append to fixture log: %v", err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append to fixture log: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fixture log: %v", err)
	}
}
