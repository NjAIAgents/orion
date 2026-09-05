// Package claim records which process is working a ticket, so a claim left
// behind by a dead one can be told apart from a claim that is doing work.
//
// THE LABEL IS THE LOCK and it lives on the ticket, which is what lets a
// restarted watcher see the same answer a second watcher sees. What the label
// cannot say is whether anyone is still holding it: `orion-working` on a
// ticket whose agent was killed looks exactly like `orion-working` on a ticket
// mid-run, and the queue excludes both. So an interrupted ticket was stranded
// until somebody removed the label by hand (OR-265).
//
// The tracker cannot answer this. internal/watch's existing staleness check
// clears a claim only where the tracker categorises the ticket's status as
// Done, which most workflows -- including this project's own -- do not.
//
// A PID and a heartbeat can. Both are needed, and neither alone is enough:
//
//   - A PID alone is wrong across a reboot, where the number is reused by an
//     unrelated process and the claim reads as live forever.
//   - A heartbeat alone is wrong for a legitimately long run. An agent that
//     works for fifty-eight minutes is not stalled, and a watcher that stole
//     its ticket at thirty would be the more expensive bug.
//
// Together they are decisive: a claim is dead when the process that wrote it
// is gone, and only then. The heartbeat is the tiebreaker for the reboot case,
// where the PID may have been reused.
package claim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/procsafe"
)

// staleAfter is how long a claim may go without a heartbeat before it is
// considered dead EVEN IF its PID still exists.
//
// Only reached when the PID is live, which after a reboot means it belongs to
// some unrelated process. Generously long, because the cost of being wrong in
// this direction is stealing a ticket from a working agent: two hours is well
// past any run the wall-clock ceiling permits, so a heartbeat this old means
// the writer is not the process now wearing its number.
const staleAfter = 2 * time.Hour

// beat is how often a holder refreshes its record. Frequent enough that a
// two-hour gap is unambiguous, rare enough to cost nothing.
const beat = time.Minute

// Record is one claim, as written to disk.
type Record struct {
	Key string `json:"key"`
	PID int    `json:"pid"`
	// Branch and Worktree are what a RESUME needs: the point of knowing a
	// claim is dead is picking its work back up, and that work is on disk
	// under these two paths.
	Branch   string    `json:"branch,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Started  time.Time `json:"started"`
	Beat     time.Time `json:"beat"`
}

// dir is where claims live, under the Orion home rather than in a project's
// repository: a claim describes a PROCESS, and the repository is the thing
// being worked on rather than the thing doing the work.
func dir(home string) string { return filepath.Join(home, "claims") }

func path(home, key string) string {
	// The key is a tracker key (OR-135), not arbitrary input, but it becomes a
	// filename, so anything that could climb out of the directory is replaced
	// rather than trusted.
	safe := strings.NewReplacer("/", "-", "\\", "-", "..", "-").Replace(key)
	return filepath.Join(dir(home), safe+".json")
}

// Take records that this process is working key. Called at dispatch, before
// any work begins, so a run killed in its first second still leaves a record
// that says who was holding it.
func Take(home, key, branch, worktree string) error {
	if home == "" || key == "" {
		return nil
	}
	if err := os.MkdirAll(dir(home), 0o700); err != nil {
		return err
	}
	now := time.Now()
	r := Record{
		Key: key, PID: os.Getpid(), Branch: branch, Worktree: worktree,
		Started: now, Beat: now,
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// Atomic, and unique per process: two watchers claiming different tickets
	// write here at the same time.
	return procsafe.WriteFile(path(home, key), b, 0o600)
}

// Beat refreshes the heartbeat. Cheap enough to call on every tick.
func Beat(home, key string) error {
	r, err := Read(home, key)
	if err != nil || r == nil {
		return err
	}
	// Only the holder beats. A watcher that refreshed another process's claim
	// would keep a dead one alive forever, which is the failure this whole
	// package exists to end.
	if r.PID != os.Getpid() {
		return nil
	}
	r.Beat = time.Now()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return procsafe.WriteFile(path(home, key), b, 0o600)
}

// Release removes the record. Called when a ticket finishes, however it
// finishes: the claim outliving the work is the residue this prevents.
func Release(home, key string) error {
	if home == "" || key == "" {
		return nil
	}
	err := os.Remove(path(home, key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Read returns the claim for key, or nil when there is none.
func Read(home, key string) (*Record, error) {
	if home == "" || key == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path(home, key))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		// A corrupt record is not a live claim. Reporting it as one would
		// strand the ticket exactly as the missing record used to.
		return nil, fmt.Errorf("claim for %s is unreadable: %w", key, err)
	}
	return &r, nil
}

// Dead reports whether key's claim is provably abandoned, and the record it
// left behind so a resume can pick up where it stopped.
//
// PROVABLY is the operative word. Every uncertain case answers "not dead":
// releasing a claim that is actually being worked would let a second agent
// start on the same ticket, which is worse than leaving one stranded for a
// person to clear.
func Dead(home, key string) (bool, *Record) {
	r, err := Read(home, key)
	if err != nil || r == nil {
		// No record at all is NOT proof of death. Claims taken before this
		// package existed have none, and neither does a claim on another
		// machine sharing a tracker. Those stay for a human.
		return false, nil
	}
	if r.PID == os.Getpid() {
		return false, r // this process is the holder
	}
	if alive(r.PID) && time.Since(r.Beat) < staleAfter {
		return false, r
	}
	return true, r
}

// alive reports whether a process with this pid exists.
//
// alive reports whether pid names a process that still exists.
//
// The implementation is per-platform (alive_unix.go, alive_windows.go)
// because the question has no portable spelling: signal 0 is the POSIX way
// to ask and Windows has no signals at all. Both answer the same way on the
// case that matters -- a process this one may not touch still EXISTS, and a
// claim held by it must never be stolen.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return processExists(pid)
}
