package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEmitAppendsAndStampsBaseFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(p, Event{Project: "FCIA", Key: "FCIA-6", Run: "r1", Actor: ActorOrion})
	if err != nil {
		t.Fatal(err)
	}
	l.Emitf(KindClaimed, ActorOrion, "claimed %s", "FCIA-6")
	l.Emit(Event{Kind: KindAnswer, Actor: ActorArchitect, Msg: "by issuer",
		Detail: map[string]any{"grounding": "spec.md §4"}})
	_ = l.Close()

	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events", len(got))
	}
	if got[0].Project != "FCIA" || got[0].Key != "FCIA-6" || got[0].Run != "r1" {
		t.Errorf("base fields not stamped: %+v", got[0])
	}
	if got[1].Actor != ActorArchitect {
		t.Errorf("explicit actor was overwritten by the base: %+v", got[1])
	}
	if got[1].Detail["grounding"] != "spec.md §4" {
		t.Errorf("detail lost: %+v", got[1].Detail)
	}
	if got[0].At.IsZero() {
		t.Error("timestamp not set")
	}
}

// A live log that buffers is not live, and the moment events matter most is
// the moment a process is killed. Every event must be on disk when Emit
// returns, not when the file is closed.
func TestEmitIsDurableImmediately(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := Open(p, Event{})
	l.Emitf(KindRunStart, ActorOrion, "started")

	// Read WITHOUT closing: simulates another process tailing, and a crash.
	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("event was buffered, not written: got %d", len(got))
	}
	_ = l.Close()
}

// Logging must never be the reason a run fails.
func TestNilLogIsSafe(t *testing.T) {
	var l *Log
	l.Emitf(KindNote, ActorOrion, "no log configured")
	l.Emit(Event{Kind: KindNote})
	if err := l.Close(); err != nil {
		t.Errorf("Close on nil = %v", err)
	}
	if l.Path() != "" {
		t.Error("Path on nil should be empty")
	}
}

// A log truncated by a kill still has value up to the cut. Refusing to parse
// any of it would discard the evidence exactly when it is most wanted.
func TestReadSkipsMalformedLinesRatherThanFailing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	body := `{"at":"2026-08-26T10:00:00Z","kind":"claimed","msg":"one"}
not json at all
{"at":"2026-08-26T10:00:01Z","kind":"pr","msg":"two"}
{"at":"2026-08-26T10:00:02Z","kind":"ci","msg":"trunc` // killed mid-write
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Msg != "one" || got[1].Msg != "two" {
		t.Fatalf("got %+v; the intact lines must survive", got)
	}
}

func TestConcurrentEmitDoesNotInterleave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := Open(p, Event{Project: "FCIA"})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			l.Emitf(KindNote, ActorOrion, "line %d", n)
		}(i)
	}
	wg.Wait()
	_ = l.Close()

	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Errorf("got %d events, want 50; concurrent writes were mangled", len(got))
	}
}

// Follow must deliver events appended after it started, and must not emit a
// half-written line as if it were an event.
func TestFollowStreamsNewEventsAndWaitsForPartialLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := Open(p, Event{Project: "FCIA"})
	l.Emitf(KindClaimed, ActorOrion, "before")

	seen := make(chan Event, 8)
	stop := make(chan struct{})
	go func() {
		_ = Follow(p, 0, 10*time.Millisecond, stop, func(e Event) { seen <- e })
	}()

	first := <-seen
	if first.Msg != "before" {
		t.Fatalf("first = %+v, want the pre-existing event", first)
	}

	l.Emitf(KindPR, ActorOrion, "after")
	select {
	case e := <-seen:
		if e.Msg != "after" {
			t.Errorf("got %+v, want the appended event", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Follow never delivered the appended event")
	}
	close(stop)
	_ = l.Close()
}

func TestPathIsUnderTheWorkspace(t *testing.T) {
	if got := Path("/x/ws"); !strings.HasSuffix(got, filepath.Join(".orion", "events.jsonl")) {
		t.Errorf("Path = %q", got)
	}
}

// The file quotes artifact text and questions; it lives in ORION_HOME and
// must inherit its owner-only posture.
func TestLogIsOwnerOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(p, Event{})
	if err != nil {
		t.Fatal(err)
	}
	l.Emitf(KindNote, ActorOrion, "x")
	_ = l.Close()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %o, want owner-only", mode)
	}
}

// shrink makes rotation reachable without writing megabytes, and restores
// the real limits afterwards.
func shrink(t *testing.T, bytes int64, files int) {
	t.Helper()
	ob, of := MaxBytes, MaxFiles
	MaxBytes, MaxFiles = bytes, files
	t.Cleanup(func() { MaxBytes, MaxFiles = ob, of })
}

func manyEvents(t *testing.T, l *Log, n int, pad string) {
	t.Helper()
	for i := 0; i < n; i++ {
		l.Emit(Event{Kind: KindNote, Actor: ActorOrion, Msg: pad})
	}
}

// A live log runs unattended for days; without a ceiling it fills the disk.
func TestRotationKeepsAtMostMaxFiles(t *testing.T) {
	shrink(t, 4096, 5)
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	l, err := Open(p, Event{Project: "FCIA"})
	if err != nil {
		t.Fatal(err)
	}
	// ~200 bytes per event against a 4KB ceiling: enough generations to
	// overflow MaxFiles several times over.
	manyEvents(t, l, 400, strings.Repeat("x", 150))
	_ = l.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > MaxFiles {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%d files kept, want at most %d: %v", len(entries), MaxFiles, names)
	}
	for _, e := range entries {
		fi, _ := e.Info()
		// Allow one event of overshoot: rotation happens before a write, so
		// a file can end slightly under and the next begins fresh.
		if fi.Size() > MaxBytes+1024 {
			t.Errorf("%s is %d bytes, over the %d ceiling", e.Name(), fi.Size(), MaxBytes)
		}
	}
}

// An event split across two files would force every reader to handle half a
// line at a generation boundary.
func TestRotationNeverSplitsAnEvent(t *testing.T) {
	shrink(t, 4096, 5)
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	l, _ := Open(p, Event{})
	manyEvents(t, l, 200, strings.Repeat("y", 150))
	_ = l.Close()

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			continue
		}
		if b[len(b)-1] != '\n' {
			t.Errorf("%s does not end on a line boundary", e.Name())
		}
		for n, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var ev Event
			if json.Unmarshal([]byte(line), &ev) != nil {
				t.Fatalf("%s line %d is not a whole event: %.80s", e.Name(), n+1, line)
			}
		}
	}
}

// The failure this guards against looks exactly like "nothing is happening":
// a tailer holding the pre-rotation descriptor keeps reading a file nothing
// writes to any more, and goes quiet without erroring.
func TestFollowSurvivesRotation(t *testing.T) {
	shrink(t, 4096, 5)
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	l, _ := Open(p, Event{})
	l.Emitf(KindClaimed, ActorOrion, "before")

	seen := make(chan Event, 64)
	stop := make(chan struct{})
	go func() { _ = Follow(p, 0, 5*time.Millisecond, stop, func(e Event) { seen <- e }) }()

	if e := <-seen; e.Msg != "before" {
		t.Fatalf("first = %+v", e)
	}
	// Force at least one rotation.
	manyEvents(t, l, 60, strings.Repeat("z", 150))
	l.Emitf(KindPR, ActorOrion, "after-rotation")

	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-seen:
			if e.Msg == "after-rotation" {
				close(stop)
				_ = l.Close()
				return
			}
		case <-deadline:
			close(stop)
			t.Fatal("Follow went quiet after the log rotated")
		}
	}
}

// OR-201. "decision" and "note" are one keystroke apart and note is the
// catch-all, so a decision with no rule to test it against drifts into a note
// and the reasoning leaves the log -- which is how KindDecision came to be
// defined, documented and never once emitted while note ran to 98 entries.
//
// Testing a comment looks odd until you notice what the alternative is: the
// rule lives ONLY here, in the file the next person reads when choosing a
// kind, and a rule that an unrelated tidy-up can delete without anything
// failing is a rule that will be deleted.
func TestTheDecisionVersusNoteRuleIsDocumentedNextToTheConstants(t *testing.T) {
	b, err := os.ReadFile("events.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	// Next to the constants, not in a document nobody opens: after the last
	// kind and before the Event type those kinds are a field of.
	after := strings.Index(src, `KindNote     = "note"`)
	before := strings.Index(src, "// Event is one line of the log.")
	if after < 0 || before < 0 || after > before {
		t.Fatal("the kind constants or the Event type moved; the rule's home moved with them")
	}
	rule := src[after:before]

	// Both halves, or it does not tell a decision from a note.
	for _, half := range []string{"alternative", "reason"} {
		if !strings.Contains(rule, half) {
			t.Errorf("the rule beside the constants does not state the %q half of a decision", half)
		}
	}
	// And the pair, so the next person emitting an ask knows it must be closed.
	for _, phrase := range []string{"ask", "answer", "refuse"} {
		if !strings.Contains(rule, phrase) {
			t.Errorf("the rule beside the constants never mentions %q", phrase)
		}
	}
}
