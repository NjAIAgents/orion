package collect

// Three more properties the fixture code in fixture_test.go (OR-315) depends
// on but does not itself assert: where the seed is built, that concurrent
// callers building it for the first time are safe, and that reopening a copy
// yields the id the copy actually has rather than the one baked into the
// seed's task.json.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

// seedWorkspace builds under whatever ORION_HOME is current at the moment it
// is called -- workspace.New has no other way to place a workspace -- and
// then relocates the tree so it outlives that home. A seed left behind under
// ORION_HOME would be torn down with the test that happened to trigger the
// build, and every later idea's first caller would build under a home it
// does not otherwise touch.
func TestSeedWorkspaceBuildsUnderCurrentHomeThenOutlivesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	seed := seedWorkspace(t, "home-callsite")

	if strings.HasPrefix(seed, home) {
		t.Fatalf("seed %s still lives under the ORION_HOME (%s) it was built under", seed, home)
	}
	entries, err := os.ReadDir(filepath.Join(home, "projects"))
	if err != nil {
		t.Fatalf("reading %s/projects: %v", home, err)
	}
	if len(entries) != 0 {
		t.Errorf("ORION_HOME's projects dir still holds the build: %v", entries)
	}
}

// The first request for a given idea from several goroutines at once must
// build the seed exactly once and hand every caller the same directory, with
// no race-detector violation on the sync.Once inside sync.Map's stored
// *built or on workspaceNo's counter in newWorkspace.
func TestSeedWorkspaceIsSafeUnderConcurrentFirstRequests(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())

	const n = 8
	dirs := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			dirs[i] = seedWorkspace(t, "concurrent-seed")
		}(i)
	}
	wg.Wait()

	for i, d := range dirs {
		if d != dirs[0] {
			t.Errorf("goroutine %d got seed %s, want %s (same as goroutine 0)", i, d, dirs[0])
		}
	}

	var wg2 sync.WaitGroup
	wg2.Add(n)
	ids := make([]string, n)
	for i := range n {
		go func(i int) {
			defer wg2.Done()
			ids[i] = newWorkspace(t, "concurrent-seed").ID
		}(i)
	}
	wg2.Wait()

	seen := make(map[string]bool, n)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("two concurrent newWorkspace calls produced the same id %q", id)
		}
		seen[id] = true
	}
}

// A copy's task.json still carries the seed's original id: only the
// directory name is renamed. workspace.Open must take the id from where the
// copy actually lives, not from that stale file, or two copies of one seed
// would open as the same workspace.
func TestOpenReadsIDFromTheDirectoryNameNotTheCopiedTaskJSON(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())

	seed := seedWorkspace(t, "open-id-mismatch")

	const newID = "open-id-mismatch-renamed"
	dir := filepath.Join(workspace.Home(), "projects", newID)
	copyTree(t, seed, dir)

	// newWorkspace would rewrite task.json's ID to match; skip that step so
	// this test exercises Open() against a task.json that still names the
	// seed's own id, exactly as a fresh copy looks before that fix-up runs.
	ws, err := workspace.Open(newID)
	if err != nil {
		t.Fatalf("opening the copied workspace: %v", err)
	}
	if ws.ID != newID {
		t.Errorf("ws.ID = %q, want %q (the directory name)", ws.ID, newID)
	}
	if ws.Dir != dir {
		t.Errorf("ws.Dir = %q, want %q", ws.Dir, dir)
	}

	reopened, err := workspace.Open(newID)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if reopened.ID != newID {
		t.Errorf("reopened ws.ID = %q, want %q", reopened.ID, newID)
	}
}
