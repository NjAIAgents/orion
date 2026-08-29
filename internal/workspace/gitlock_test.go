package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent job starts against ONE shared clone.
//
// This is the hazard that made level-1 parallelism more than a for-loop:
// worktrees isolate files, and share the object store, the refs and
// packed-refs. AddWorktree fetches, reads the ref list to find a free branch
// name, then runs `git worktree add -b`. Run those steps interleaved and two
// jobs are told the same name is free -- the second either fails on a ref lock
// or, worse, ends up sharing a branch with the first.
//
// Ten at once rather than two: the race is a window of microseconds and a pair
// of goroutines can miss it every run.
func TestConcurrentAddWorktreeGivesEveryJobItsOwnBranch(t *testing.T) {
	ws := sandbox(t)
	const jobs = 10

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		got  = map[string]bool{}
		errs []error
	)
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := AddWorktree(ws, "develop", "orion/or-184")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if got[j.Branch] {
				errs = append(errs, fmt.Errorf("branch %s handed out twice", j.Branch))
				return
			}
			got[j.Branch] = true
			if _, err := os.Stat(filepath.Join(j.Path, "README.md")); err != nil {
				errs = append(errs, fmt.Errorf("%s is not a usable checkout: %w", j.Path, err))
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Error(err)
	}
	if len(got) != jobs {
		t.Fatalf("%d distinct branches for %d concurrent jobs", len(got), jobs)
	}

	// And git itself agrees: the worktree registry is intact, not merely the
	// return values.
	list, err := ListWorktrees(ws)
	if err != nil {
		t.Fatalf("the shared clone's worktree list is unreadable after the run: %v", err)
	}
	if len(list) != jobs {
		t.Errorf("git reports %d worktrees, want %d", len(list), jobs)
	}
}

// Adding and removing at the same time is the other half: a finished ticket
// prunes its worktree while the next one is being created, and both rewrite
// .git/worktrees.
func TestAddingAndRemovingWorktreesConcurrentlyIsSafe(t *testing.T) {
	ws := sandbox(t)

	// Six existing jobs to remove while six more are created.
	var existing []*Job
	for i := 0; i < 6; i++ {
		j, err := AddWorktree(ws, "develop", fmt.Sprintf("orion/old-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		existing = append(existing, j)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, err)
	}
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := AddWorktree(ws, "develop", fmt.Sprintf("orion/new-%d", n)); err != nil {
				fail(err)
			}
		}(i)
		wg.Add(1)
		go func(j *Job) {
			defer wg.Done()
			if err := RemoveWorktree(ws, j.Path, false); err != nil {
				fail(err)
			}
		}(existing[i])
	}
	wg.Wait()

	for _, err := range errs {
		t.Error(err)
	}
	list, err := ListWorktrees(ws)
	if err != nil {
		t.Fatalf("the worktree registry did not survive: %v", err)
	}
	if len(list) != 6 {
		t.Errorf("%d worktrees remain, want the 6 that were added", len(list))
	}
}
