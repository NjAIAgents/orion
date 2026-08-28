package procsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLockIsExclusiveWhileHeld(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "x.lock")
	release, err := Lock(lp)
	if err != nil {
		t.Fatal(err)
	}

	// A second attempt must not succeed while the first is held. Short
	// bounds so the test does not sit for the default three seconds.
	rel2, err := LockFor(lp, 50*time.Millisecond, time.Minute)
	defer rel2()
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("second Lock = %v, want ErrLockTimeout", err)
	}

	release()
	rel3, err := LockFor(lp, time.Second, time.Minute)
	if err != nil {
		t.Fatalf("lock must be takeable once released: %v", err)
	}
	rel3()
}

// A crashed process must not wedge every later invocation.
func TestAStaleLockIsBroken(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "x.lock")
	if err := os.Mkdir(lp, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lp, old, old); err != nil {
		t.Fatal(err)
	}

	release, err := LockFor(lp, time.Second, time.Minute)
	defer release()
	if err != nil {
		t.Fatalf("a stale lock must be broken, got: %v", err)
	}
}

// The release function is never nil, even on failure: every caller does
// `defer release()`, and in the hook path a panic means Claude Code receives
// a crash instead of a verdict.
func TestReleaseIsNeverNil(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "x.lock")
	held, _ := Lock(lp)
	defer held()

	release, err := LockFor(lp, 10*time.Millisecond, time.Minute)
	if err == nil {
		t.Fatal("expected a timeout for this case")
	}
	if release == nil {
		t.Fatal("release must never be nil")
	}
	release() // must not panic
}

func TestWriteFileReplacesAtomically(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.json")
	if err := WriteFile(p, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "second" {
		t.Errorf("content = %q", b)
	}
}

// The whole point of the per-process temp name: a fixed "<path>.tmp" is
// shared by every process on this ORION_HOME, so two writers interleave
// inside one file and the rename publishes the mixture. Concurrent writers
// must each publish a WHOLE value, never a blend of two.
func TestConcurrentWritesNeverPublishAMixture(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.json")
	const writers = 8

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Long enough that a torn write would be obvious.
			body := strings.Repeat(fmt.Sprintf("%d", n), 4096)
			if err := WriteFile(p, []byte(body), 0o600); err != nil {
				t.Errorf("writer %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 4096 {
		t.Fatalf("length = %d, want a single whole value of 4096", len(b))
	}
	first := b[0]
	for i, c := range b {
		if c != first {
			t.Fatalf("byte %d = %q, want all %q: the file is a mixture of two writers", i, c, first)
		}
	}
}

// No temp files left behind after a successful write.
func TestWriteFileLeavesNoLitter(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	if err := WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
}
