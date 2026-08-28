// Package procsafe holds the two operations that have to stay correct when
// SEVERAL Orion processes share one ORION_HOME.
//
// That is the supported workflow: `orion watch A` and `orion watch B` in two
// terminals, one per project repository. Ticket claiming is already safe
// across processes -- the orion-working label is the lock and it lives on the
// Jira ticket -- and workspaces and worktrees are per project and per ticket.
// What is shared is the small amount of mutable state under ORION_HOME:
// usage.json and repos.json.
//
// Both were load-modify-save with an atomic rename but no lock, and both used
// a FIXED temp path. Two processes therefore raced twice over: on the temp
// file itself, and on the read-modify-write, where last-rename-wins silently
// dropped one process's update (OR-138).
//
// The lock is a directory rather than flock(2). os.Mkdir is atomic on every
// platform Orion ships to; flock is not available on Windows without cgo or
// syscall build tags, and the release Makefile cross-compiles six targets
// with CGO_ENABLED=0. internal/state has used exactly this approach for the
// hook path since the beginning; this package is that implementation lifted
// out so budget and registry can share it rather than grow a second one.
package procsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultTimeout bounds how long a caller waits for the lock.
	DefaultTimeout = 3 * time.Second
	// DefaultStale is when a lock is assumed abandoned by a crashed process.
	// A crash must not wedge every later invocation.
	DefaultStale = 30 * time.Second
)

// ErrLockTimeout signals the lock could not be taken and the caller ran
// unserialized. Callers report it; they do not fail on it. See Lock.
var ErrLockTimeout = errors.New("orion: lock timeout; update ran unserialized")

// Lock takes the cross-process lock named by lockPath, with the default
// timeout and staleness.
func Lock(lockPath string) (release func(), err error) {
	return LockFor(lockPath, DefaultTimeout, DefaultStale)
}

// LockFor is Lock with explicit bounds.
//
// It NEVER returns a nil release function. Every caller does `defer
// release()`, so a nil here would be an immediate panic -- and in the hook
// path a panic means Claude Code receives a crash instead of a verdict: the
// guardrail stops guarding at the exact moment it is being exercised
// hardest. Failure degrades to running unlocked and reporting it, never to a
// nil func and never to blocking forever.
func LockFor(lockPath string, timeout, stale time.Duration) (func(), error) {
	noop := func() {}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return noop, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err := os.Mkdir(lockPath, 0o755)
		if err == nil {
			return func() { _ = os.Remove(lockPath) }, nil
		}
		// Not just ErrExist. On Windows a directory pending deletion by a
		// racing process reports access-denied rather than already-exists, so
		// treating anything but ErrExist as fatal turns ordinary contention
		// into a crash. Keep retrying until the deadline and let the timeout
		// below decide.
		if !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrPermission) {
			return noop, err
		}
		if fi, statErr := os.Stat(lockPath); statErr == nil {
			if time.Since(fi.ModTime()) > stale {
				_ = os.Remove(lockPath)
				continue
			}
		}
		if time.Now().After(deadline) {
			// Proceed unlocked rather than block. A watcher that stalls
			// because another watcher is mid-write is worse than a rare
			// unserialized update, and the caller reports the degradation.
			return noop, ErrLockTimeout
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// WriteFile atomically replaces path, via a temp file UNIQUE TO THIS PROCESS.
//
// The uniqueness is the point. A fixed "<path>.tmp" is shared by every
// process using this ORION_HOME, so two writers interleave inside one temp
// file and the rename publishes whatever mixture happened to be there. That
// is a corrupt file, not merely a lost update, and it survives the lock
// failing open above -- which is exactly when it would matter.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // do not leave litter behind a failed publish
		return err
	}
	return nil
}
