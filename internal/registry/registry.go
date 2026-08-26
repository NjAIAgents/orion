// Package registry maps a tracker project key to the repository it belongs
// to, so Orion can act on a ticket without being told where the code lives.
//
// Why this exists. Every command so far resolved the repository from the
// current directory, which works for a person standing in a checkout and not
// at all for a daemon: `orion watch` has no meaningful cwd, and a ticket key
// is the only thing it has to go on. The key already names the project, so
// the mapping is the missing half.
//
// It also catches a mistake that is otherwise silent. Two repositories both
// claiming project FCIA -- a clone, a rename, a copy made for an experiment
// -- would each believe the queue is theirs, and work would land in whichever
// one happened to run last. The registry refuses the second binding and says
// which repository already holds it.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one adopted repository.
type Entry struct {
	// Key is the tracker project key, uppercased. The map key too, but
	// repeated here so an Entry is self-describing once passed around.
	Key string `json:"key"`
	// Source is the user's working copy. Orion never writes to it; it is
	// recorded so a run can fast-forward it afterwards.
	Source string `json:"source"`
	// Workspace is the sandbox that runs happen in.
	Workspace string `json:"workspace"`
	// Channel is the Slack conversation id for progress and failures.
	Channel  string    `json:"channel,omitempty"`
	Remote   string    `json:"remote,omitempty"`
	BoundAt  time.Time `json:"bound_at"`
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// File is the on-disk shape.
type File struct {
	Version int              `json:"version"`
	Repos   map[string]Entry `json:"repos"`
}

func path(home string) string { return filepath.Join(home, "repos.json") }

// Load reads the registry. A missing file is an empty registry, not an
// error: nothing has been adopted yet is a normal state.
func Load(home string) (*File, error) {
	f := &File{Version: 1, Repos: map[string]Entry{}}
	b, err := os.ReadFile(path(home))
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, err
	}
	if err := json.Unmarshal(b, f); err != nil {
		// Refuse rather than silently starting empty. An unreadable registry
		// that reset itself would let a second repository claim a key the
		// first one still owns, which is the exact collision this prevents.
		return nil, fmt.Errorf("%s is not valid JSON: %w\n"+
			"  Fix or delete it; starting empty would let another repository "+
			"claim a project key that is already bound", path(home), err)
	}
	if f.Repos == nil {
		f.Repos = map[string]Entry{}
	}
	return f, nil
}

// Save writes the registry with the same owner-only mode as the rest of
// ORION_HOME: it records local paths and channel ids.
func Save(home string, f *File) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f.Version = 1
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path(home) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path(home))
}

// Bind records a repository against a project key.
//
// Re-binding the SAME source is an update, because `orion init` is how a repo
// is repaired and must stay idempotent. Binding a DIFFERENT source to a key
// that is already taken is refused: silently reassigning it would send the
// next run's work into a repository nobody was looking at.
func Bind(home string, e Entry) error {
	e.Key = NormalizeKey(e.Key)
	if e.Key == "" {
		return fmt.Errorf("a project key is required to register a repository")
	}
	abs, err := filepath.Abs(e.Source)
	if err != nil {
		return err
	}
	e.Source = abs

	f, err := Load(home)
	if err != nil {
		return err
	}
	if prev, ok := f.Repos[e.Key]; ok && !sameDir(prev.Source, e.Source) {
		return fmt.Errorf("project %s is already bound to %s.\n"+
			"  Binding %s to it as well would make the queue ambiguous: work for a\n"+
			"  %s ticket could land in either repository.\n"+
			"  Use a different project key here, or run: orion unbind %s",
			e.Key, prev.Source, e.Source, e.Key, e.Key)
	}
	if e.BoundAt.IsZero() {
		e.BoundAt = time.Now().UTC()
	}
	f.Repos[e.Key] = e
	return Save(home, f)
}

// Unbind forgets a project key.
func Unbind(home, key string) error {
	f, err := Load(home)
	if err != nil {
		return err
	}
	key = NormalizeKey(key)
	if _, ok := f.Repos[key]; !ok {
		return fmt.Errorf("project %s is not registered", key)
	}
	delete(f.Repos, key)
	return Save(home, f)
}

// Lookup resolves a project key, or an ISSUE key such as FCIA-6.
//
// Accepting the issue key is the point of the whole package: `orion work
// FCIA-6` should not also require being told which repository FCIA is.
func Lookup(home, keyOrIssue string) (*Entry, error) {
	f, err := Load(home)
	if err != nil {
		return nil, err
	}
	key := ProjectOf(keyOrIssue)
	e, ok := f.Repos[key]
	if !ok {
		if len(f.Repos) == 0 {
			return nil, fmt.Errorf("no repositories are registered.\n"+
				"  Run orion init inside the repository that owns %s", key)
		}
		return nil, fmt.Errorf("project %s is not registered.\n"+
			"  Registered: %s\n"+
			"  Run orion init inside the repository that owns %s",
			key, strings.Join(f.Keys(), ", "), key)
	}
	return &e, nil
}

// Keys lists registered project keys, sorted so output is stable.
func (f *File) Keys() []string {
	out := make([]string, 0, len(f.Repos))
	for k := range f.Repos {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ProjectOf extracts the project key from an issue key. "FCIA-6" -> "FCIA",
// and a bare project key passes through unchanged.
func ProjectOf(s string) string {
	s = NormalizeKey(s)
	if i := strings.LastIndex(s, "-"); i > 0 {
		// Only treat the tail as an issue number when it IS a number. Some
		// project keys legitimately contain a hyphen, and truncating those
		// would look up a project that does not exist.
		if isDigits(s[i+1:]) {
			return s[:i]
		}
	}
	return s
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// NormalizeKey uppercases and trims. Jira keys are uppercase, but people
// type them however they like.
func NormalizeKey(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// Touch records that a project was seen, for staleness reporting. Failures
// are ignored by callers: a bookkeeping write must never fail a run.
func Touch(home, key string) error {
	f, err := Load(home)
	if err != nil {
		return err
	}
	key = NormalizeKey(key)
	e, ok := f.Repos[key]
	if !ok {
		return nil
	}
	e.LastSeen = time.Now().UTC()
	f.Repos[key] = e
	return Save(home, f)
}

// Prune reports entries whose source directory has gone, without deleting
// them.
//
// Reporting rather than removing is deliberate: an unmounted volume or a
// repository temporarily moved aside looks identical to one deleted on
// purpose, and quietly forgetting a binding would let the key be reassigned
// to a different repository the next time init runs there.
func Prune(home string) ([]Entry, error) {
	f, err := Load(home)
	if err != nil {
		return nil, err
	}
	var missing []Entry
	for _, k := range f.Keys() {
		e := f.Repos[k]
		if _, err := os.Stat(e.Source); err != nil {
			missing = append(missing, e)
		}
	}
	return missing, nil
}

func sameDir(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}
