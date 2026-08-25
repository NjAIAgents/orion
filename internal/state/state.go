// Package state persists per-session counters across hook invocations.
//
// Hooks are separate processes: every fire is a cold start with no memory
// of the last one. Loop detection and budget enforcement are therefore
// only possible against durable state. Parallel worktree sessions may
// share a state directory, so every read-modify-write is serialized by a
// lock that works on every platform (os.Mkdir is atomic everywhere;
// flock is not available on Windows without cgo or syscall build tags).
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session holds every counter a hook can consult or increment.
type Session struct {
	ID              string         `json:"id"`
	StartedAt       time.Time      `json:"started_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	ToolCalls       int            `json:"tool_calls"`
	Repeats         map[string]int `json:"repeats"`
	ConsecFailures  int            `json:"consecutive_failures"`
	CmdFailures     map[string]int `json:"cmd_failures"`
	EditsSinceCheck int            `json:"edits_since_verify"`
	FilesTouched    map[string]int `json:"files_touched"`
	// Tripped records which breaker fired first, so repeated hook fires
	// after a block report the original cause rather than a new one.
	Tripped       string `json:"tripped,omitempty"`
	TrippedDetail string `json:"tripped_detail,omitempty"`
}

func newSession(id string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID: id, StartedAt: now, UpdatedAt: now,
		Repeats: map[string]int{}, CmdFailures: map[string]int{}, FilesTouched: map[string]int{},
	}
}

// Store is a lock-guarded directory of session JSON files.
type Store struct {
	dir string
}

func New(dir string) *Store { return &Store{dir: dir} }

// sanitize keeps a hostile or empty session id from escaping the state
// directory. Hook input is untrusted: session_id arrives as JSON from the
// harness and must never be used as a path component unfiltered.
func sanitize(id string) string {
	if id == "" {
		return "unknown-session"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := b.String()
	if len(s) > 96 {
		s = s[:96]
	}
	return s
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, sanitize(id)+".json")
}

func (s *Store) lockPath(id string) string {
	return filepath.Join(s.dir, sanitize(id)+".lock")
}

const (
	lockTimeout = 3 * time.Second
	lockStale   = 30 * time.Second
)

// acquire takes the per-session lock, breaking it if it is older than
// lockStale. A crashed hook must not wedge every later invocation, and a
// wedged hook that blocks all tool use is worse than a briefly racy
// counter.
func (s *Store) acquire(id string) (func(), error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	lp := s.lockPath(id)
	deadline := time.Now().Add(lockTimeout)
	for {
		err := os.Mkdir(lp, 0o755)
		if err == nil {
			return func() { _ = os.Remove(lp) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, statErr := os.Stat(lp); statErr == nil {
			if time.Since(fi.ModTime()) > lockStale {
				_ = os.Remove(lp)
				continue
			}
		}
		if time.Now().After(deadline) {
			// Proceed unlocked rather than block the session. Report it so
			// the degradation is visible in the transcript.
			return func() {}, ErrLockTimeout
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// ErrLockTimeout signals the lock could not be taken and the update ran
// unserialized. Counters may undercount under heavy parallelism.
var ErrLockTimeout = errors.New("orion: state lock timeout; counter update ran unserialized")

// Update applies fn to the session under lock and persists the result.
// The mutated session is returned so the caller can make its decision
// from the same snapshot that was written.
func (s *Store) Update(id string, fn func(*Session)) (*Session, error) {
	release, lockErr := s.acquire(id)
	defer release()

	sess := s.readUnlocked(id)
	fn(sess)
	sess.UpdatedAt = time.Now().UTC()
	err := s.writeUnlocked(sess)
	if err == nil {
		err = lockErr
	}
	return sess, err
}

// Read returns the current session without mutating it.
func (s *Store) Read(id string) *Session {
	release, _ := s.acquire(id)
	defer release()
	return s.readUnlocked(id)
}

func (s *Store) readUnlocked(id string) *Session {
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return newSession(sanitize(id))
	}
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		// Corrupt state must not disable enforcement. Start clean.
		return newSession(sanitize(id))
	}
	if sess.Repeats == nil {
		sess.Repeats = map[string]int{}
	}
	if sess.CmdFailures == nil {
		sess.CmdFailures = map[string]int{}
	}
	if sess.FilesTouched == nil {
		sess.FilesTouched = map[string]int{}
	}
	if sess.ID == "" {
		sess.ID = sanitize(id)
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = time.Now().UTC()
	}
	return &sess
}

// writeUnlocked persists atomically: write to a temp file in the same
// directory, then rename. A torn state file would silently reset every
// counter, which is a control failure, not a cosmetic one.
func (s *Store) writeUnlocked(sess *Session) error {
	b, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path(sess.ID))
}

// Reset removes a session's state. Used by SessionStart so a fresh
// session never inherits a previous session's exhausted budget.
func (s *Store) Reset(id string) error {
	release, _ := s.acquire(id)
	defer release()
	err := os.Remove(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Sweep deletes session files untouched for longer than maxAge, keeping
// the state directory from growing without bound across many sessions.
func (s *Store) Sweep(maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(fi.ModTime()) > maxAge {
			if os.Remove(filepath.Join(s.dir, e.Name())) == nil {
				n++
			}
		}
	}
	return n, nil
}

// Elapsed reports how long the session has been running.
func (sess *Session) Elapsed() time.Duration { return time.Since(sess.StartedAt) }
