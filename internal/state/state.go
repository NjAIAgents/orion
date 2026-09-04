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

	"github.com/orion-sdlc/orion/internal/procsafe"
)

// Session holds every counter a hook can consult or increment.
type Session struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ToolCalls int       `json:"tool_calls"`
	// Repeats counts identical calls per ACTOR, not per session: keyed first
	// by actor (the main thread or one specific subagent), then by call
	// signature. A session run in parallel fan-out has one Session file
	// shared by the parent and every subagent it spawns (they carry the same
	// SessionID), so a flat signature->count map would let two agents each
	// reading the same file twice add up to a false loop trip that neither
	// individually caused (OR-170). Nesting by actor keeps each one's count
	// its own.
	Repeats         map[string]map[string]int `json:"repeats"`
	ConsecFailures  int                       `json:"consecutive_failures"`
	CmdFailures     map[string]int            `json:"cmd_failures"`
	EditsSinceCheck int                       `json:"edits_since_verify"`
	FilesTouched    map[string]int            `json:"files_touched"`
	// Tripped records which breaker fired first, so repeated hook fires
	// after a block report the original cause rather than a new one.
	Tripped       string `json:"tripped,omitempty"`
	TrippedDetail string `json:"tripped_detail,omitempty"`
	// CleanupCalls counts the post-trip cleanup allowance already spent.
	//
	// A trip must stop the agent from LOOPING without also stopping it from
	// leaving the worktree in a reportable state (OR-194), so a tripped
	// session may still run a short, fixed list of git commands. Counting
	// them here is what keeps the allowance an exit rather than a reprieve:
	// it is bounded, it survives the cold start between hook processes, and
	// spending it never clears Tripped.
	CleanupCalls int `json:"cleanup_calls,omitempty"`
	// TripSnapshot records what became of the work that was uncommitted at
	// the moment of the trip -- "committed 14 file(s)", or why it could not
	// be. Read by the block message and by Orion's end-of-run report, so
	// neither has to guess: a run that says "committed for you" when the
	// commit failed is the misleading-but-technically-shaped message this
	// codebase keeps having to unlearn (OR-143).
	TripSnapshot string `json:"trip_snapshot,omitempty"`
	// Awaiting holds the output paths of background commands this session
	// launched. A read of one of these is a WAIT, not a repeat: the agent is
	// polling a file it was told to expect output in. Without this the only
	// available way to wait was indistinguishable from a no-progress loop,
	// and two finished tickets died for it (OR-207).
	Awaiting map[string]bool `json:"awaiting,omitempty"`
	// LastPoll fingerprints the response of the previous identical poll, per
	// actor then per call signature. A read that returns something DIFFERENT
	// from last time observed progress, whatever its input was.
	LastPoll map[string]map[string]string `json:"last_poll,omitempty"`
	// ConsecPolls counts polls since the last call that did anything else
	// (OR-331): waiting is allowed, waiting forever is not.
	ConsecPolls int `json:"consec_polls,omitempty"`
}

func newSession(id string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID: id, StartedAt: now, UpdatedAt: now,
		Repeats: map[string]map[string]int{}, CmdFailures: map[string]int{}, FilesTouched: map[string]int{},
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

// acquire takes the per-session lock, breaking it if it has gone stale. A
// crashed hook must not wedge every later invocation, and a wedged hook that
// blocks all tool use is worse than a briefly racy counter.
//
// The implementation lives in internal/procsafe, which is this code lifted
// out so budget and registry could stop growing a second copy of it (OR-138).
// The contract is unchanged, including the one that matters most here: it
// NEVER returns a nil release function, because every caller does `defer
// release()` and a panic inside a hook means Claude Code receives a crash
// instead of a verdict.
func (s *Store) acquire(id string) (func(), error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return func() {}, err
	}
	return procsafe.Lock(s.lockPath(id))
}

// ErrLockTimeout signals the lock could not be taken and the update ran
// unserialized. Counters may undercount under heavy parallelism.
//
// Aliased rather than redefined so `errors.Is(err, state.ErrLockTimeout)` in
// internal/hook keeps working while the lock itself lives in procsafe.
var ErrLockTimeout = procsafe.ErrLockTimeout

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
		sess.Repeats = map[string]map[string]int{}
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

// AnyTripped returns the first session in this store whose breaker fired.
//
// Deliberately not "look up session X". A run's worktree accumulates one
// state file per session -- the implementer, QA, each fix round -- and any
// one of them tripping leaves the same residue behind. OR-192 tripped in
// QA, not in the implementer's session, so a lookup by the id the caller
// happens to be holding is exactly the check that would have missed it.
//
// An unreadable or corrupt file is skipped rather than reported: the caller
// uses this to decide whether to tidy up, and failing that decision over
// one bad file would leave the worktree dirty for a reason unrelated to it.
func (s *Store) AnyTripped() (*Session, bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var sess Session
		if json.Unmarshal(b, &sess) != nil {
			continue
		}
		if sess.Tripped != "" {
			return &sess, true
		}
	}
	return nil, false
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
		// maxAge <= 0 means sweep everything, stated rather than inferred.
		// Relying on time.Since(modTime) > 0 to express "remove all" is a
		// race against filesystem timestamp granularity: on Windows the
		// recorded mtime of a file written moments ago can equal now, the
		// comparison is false, and nothing is removed.
		if maxAge <= 0 || time.Since(fi.ModTime()) > maxAge {
			if os.Remove(filepath.Join(s.dir, e.Name())) == nil {
				n++
			}
		}
	}
	return n, nil
}

// Elapsed reports how long the session has been running.
func (sess *Session) Elapsed() time.Duration { return time.Since(sess.StartedAt) }

// ResetCommand is the exact thing an operator types to un-park a session whose
// breaker tripped.
//
// One spelling, in the package that owns the session id, because the command
// was previously written out wherever it was needed and every one of those
// places was addressed to the AGENT or to a file on disk: the block message in
// its session, plans/BLOCKED.md inside a hashed path under ORION_HOME, and
// `orion --help`. None of them was the surface an operator watches, so on
// OR-217 the command was found only by reading BLOCKED.md off disk by hand
// (OR-232). Callers that put it on that surface share this rather than adding
// a fifth literal nobody can grep for.
//
// Empty for an unknown session: a command with a blank id is worse than no
// command, because it looks runnable and is not.
func ResetCommand(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return "orion reset --session " + sessionID
}
