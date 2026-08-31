package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A command with a blank session id looks runnable and is not, which is worse
// than saying nothing: it sends an operator to a terminal to be told the
// command is malformed, at the moment they are already trying to work out what
// went wrong (OR-232).
func TestResetCommandIsEmptyForAnUnknownSession(t *testing.T) {
	if got := ResetCommand(""); got != "" {
		t.Errorf("ResetCommand of an unknown session = %q, want nothing at all", got)
	}
	if got := ResetCommand("4b6af93d"); got != "orion reset --session 4b6af93d" {
		t.Errorf("ResetCommand = %q; the spelling is the one `orion --help` documents", got)
	}
}

func TestUpdatePersists(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Update("sess", func(x *Session) { x.ToolCalls = 5 }); err != nil {
		t.Fatal(err)
	}
	if got := s.Read("sess").ToolCalls; got != 5 {
		t.Errorf("ToolCalls = %d, want 5 (state did not survive the process boundary)", got)
	}
}

func TestSessionsAreIsolated(t *testing.T) {
	s := New(t.TempDir())
	s.Update("a", func(x *Session) { x.ToolCalls = 3 })
	s.Update("b", func(x *Session) { x.ToolCalls = 7 })
	if s.Read("a").ToolCalls != 3 || s.Read("b").ToolCalls != 7 {
		t.Error("parallel worktree sessions must not share counters")
	}
}

func TestResetClearsTrip(t *testing.T) {
	s := New(t.TempDir())
	s.Update("sess", func(x *Session) { x.Tripped = "breaker/loop"; x.ToolCalls = 99 })
	if err := s.Reset("sess"); err != nil {
		t.Fatal(err)
	}
	got := s.Read("sess")
	if got.Tripped != "" || got.ToolCalls != 0 {
		t.Error("a new session must not inherit an exhausted budget or a tripped breaker")
	}
}

func TestResetMissingSessionIsNotAnError(t *testing.T) {
	if err := New(t.TempDir()).Reset("never-existed"); err != nil {
		t.Errorf("resetting an absent session should be a no-op, got %v", err)
	}
}

// A hostile or malformed session id must not escape the state directory.
func TestSanitizePreventsPathEscape(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Update("../../etc/passwd", func(x *Session) { x.ToolCalls = 1 })

	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one state file inside the dir, got %v", matches)
	}
}

func TestConcurrentUpdatesDoNotLoseState(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Update("shared", func(x *Session) { x.ToolCalls++ })
		}()
	}
	wg.Wait()
	// The lock is best-effort with a timeout, so an exact count is not
	// guaranteed under contention. What must hold is that the file stays
	// readable and the counter advanced.
	got := s.Read("shared").ToolCalls
	if got == 0 {
		t.Fatal("concurrent updates lost every increment")
	}
	if got > 25 {
		t.Fatalf("counter overcounted: %d", got)
	}
}

func TestSweepRemovesOnlyStale(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Update("fresh", func(x *Session) { x.ToolCalls = 1 })
	s.Update("old", func(x *Session) { x.ToolCalls = 2 })

	// Backdate one file rather than sleeping. A sleep long enough to beat
	// filesystem timestamp granularity is both slow and still a race.
	old := filepath.Join(dir, "old.json")
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	n, err := s.Sweep(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d, want 1: only the backdated session is stale", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.json")); err != nil {
		t.Error("swept a fresh session; that loses an active budget")
	}
}

// Sweep(0) means "remove everything". Expressing that as time.Since(mtime) > 0
// is a race against filesystem timestamp granularity, and it lost on Windows.
func TestSweepZeroAgeRemovesEverything(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Update("a", func(x *Session) { x.ToolCalls = 1 })
	s.Update("b", func(x *Session) { x.ToolCalls = 1 })

	n, err := s.Sweep(0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("swept %d with a zero max age, want 2", n)
	}
}

func TestCorruptStateStartsCleanRatherThanFailing(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Update("sess", func(x *Session) { x.ToolCalls = 3 })
	// Corrupt state must not disable enforcement; it must restart it.
	if err := writeRaw(filepath.Join(dir, "sess.json"), "{not json"); err != nil {
		t.Fatal(err)
	}
	if got := s.Read("sess"); got.ToolCalls != 0 || got.Repeats == nil {
		t.Error("corrupt state should yield a usable zeroed session")
	}
}

// acquire must never hand back a nil release function. Every caller does
// `defer release()`, so a nil is an instant panic, and a panic inside a hook
// gives Claude Code a crash instead of a verdict: enforcement disappears
// exactly when it is under the most pressure. Windows CI found this by
// racing 25 goroutines on one lock directory.
func TestAcquireNeverReturnsNilRelease(t *testing.T) {
	s := New(t.TempDir())
	release, _ := s.acquire("sess")
	if release == nil {
		t.Fatal("acquire returned a nil release on the happy path")
	}
	release()

	// Unwritable store directory: the error path must still be callable.
	bad := New(filepath.Join(t.TempDir(), "file-not-dir"))
	if err := os.WriteFile(filepath.Dir(bad.path("x")), []byte("x"), 0o600); err == nil {
		r2, err2 := bad.acquire("sess")
		if r2 == nil {
			t.Fatal("acquire returned a nil release on an error path")
		}
		r2() // must not panic
		_ = err2
	}
}
