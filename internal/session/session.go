// Package session tracks which orion processes are actually alive (OR-52).
//
// A SESSION IS A PROCESS, not a project and not a run. `orion watch` with no
// arguments watches every project, so a session cannot be per-workspace; the
// thing a person needs distinguished is the two terminals they started.
// Runs appear inside a session -- a watcher works many tickets in sequence.
//
// LIVENESS IS A HEARTBEAT FILE, not a last-event timestamp and not a PID
// check. Neither alternative works:
//
//   - Last-event timestamp cannot separate quiet from gone. An agent between
//     turns and a watcher killed an hour ago both emit nothing.
//   - PID + signal-0 is meaningless on Windows, where os.FindProcess always
//     succeeds, and PIDs are reused on every OS, so a dead watcher's pid can
//     be occupied by something unrelated and read as alive.
//
// A goroutine touches the file every ~15s, independently of whatever tick
// the caller is on, so it keeps beating through a long agent run. A reader
// treats the record as stale past a few multiples of that interval -- see
// StaleAfter.
//
// STOPPED SESSIONS VANISH. The file is removed on clean exit (Stop) and
// swept when stale (Sweep, called at start, never by a reader -- see
// Enumerate's own doc comment for why). History is not this package's job;
// events.jsonl and task.json already hold it.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Kind is what a session is running.
type Kind string

const (
	KindWatch Kind = "watch"
	KindWork  Kind = "work"
)

// BeatInterval is how often the heartbeat goroutine touches the record.
const BeatInterval = 15 * time.Second

// StaleAfter is how long since the last beat before a reader treats a
// record as dead. Four times BeatInterval: generous enough that a
// goroutine briefly delayed by GC or a loaded machine does not flicker a
// live session to stale and back, tight enough that a genuinely dead
// process reads as stopped within about a minute.
const StaleAfter = 4 * BeatInterval

// Record is one session's on-disk state: ~/.orion/sessions/<id>.json.
//
// PID and Host are for DISPLAY ONLY, never for liveness -- see the package
// doc comment for why a PID check does not work. Nothing in this package
// reads them to decide whether a session is alive.
type Record struct {
	ID       string    `json:"id"`
	Kind     Kind      `json:"kind"`
	Projects []string  `json:"projects"`
	PID      int       `json:"pid"`
	Host     string    `json:"host"`
	Started  time.Time `json:"started"`
	LastBeat time.Time `json:"last_beat"`
	Doing    string    `json:"doing"`
}

// Stale reports whether r's last beat is old enough that a reader should
// treat it as dead, judged against now.
func (r Record) Stale(now time.Time) bool {
	return now.Sub(r.LastBeat) > StaleAfter
}

// dir is ~/.orion/sessions (or $ORION_HOME/sessions), created on first use.
func dir(home string) string { return filepath.Join(home, "sessions") }

func path(home, id string) string { return filepath.Join(dir(home), id+".json") }

// write is the one place a Record reaches disk -- New and the heartbeat
// both call it, so the encoding can never drift between them.
//
// Written to a temp file and renamed into place: a reader (Enumerate) never
// sees a half-written record, because rename is atomic on every OS this
// project ships for.
func write(home string, r Record) error {
	if err := os.MkdirAll(dir(home), 0o755); err != nil {
		return fmt.Errorf("session: creating %s: %w", dir(home), err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("session: encoding record for %s: %w", r.ID, err)
	}
	tmp := path(home, r.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("session: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path(home, r.ID)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("session: renaming %s into place: %w", tmp, err)
	}
	return nil
}

// hostname resolves the display-only Host field, falling back to "unknown"
// rather than failing the session -- see Session.beat's doc comment for the
// same posture applied to every write this package makes.
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// read loads one record by id. A missing file is reported through the
// returned error (os.IsNotExist), not a special zero value, so a caller
// cannot mistake "never existed" for "exists and is empty".
func read(home, id string) (Record, error) {
	b, err := os.ReadFile(path(home, id))
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("session: decoding %s: %w", path(home, id), err)
	}
	return r, nil
}

// Enumerate lists every LIVE session under home: records that exist and are
// not Stale as of now. A stale record is filtered out here, never deleted --
// enumeration is a read, and OR-35's read-only-dashboard rule extends to
// this package's own readers: a reader that deletes files is a write from a
// a read path. Sweeping stale records happens only in Start, at the moment
// a new session actually begins.
func Enumerate(home string, now time.Time) ([]Record, error) {
	entries, err := os.ReadDir(dir(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: reading %s: %w", dir(home), err)
	}

	var out []Record
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		r, err := read(home, id)
		if err != nil {
			// A record that cannot be read is not a live session -- skipped
			// rather than failing every reader over one corrupt file (the
			// same partial-tolerance posture as events.Follow).
			continue
		}
		if r.Stale(now) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Sweep removes every record under home that is Stale as of now, and
// returns how many it removed. Called only from Start -- see Enumerate's
// doc comment for why a reader must never call this.
func Sweep(home string, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir(home))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("session: reading %s: %w", dir(home), err)
	}

	n := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		r, err := read(home, id)
		if err != nil {
			// Unreadable is not necessarily stale, but it is also not a
			// record anything can act on; leaving it is safe (Enumerate
			// already skips it) and removing a file we failed to parse
			// risks deleting something mid-write by another process.
			continue
		}
		if !r.Stale(now) {
			continue
		}
		if err := os.Remove(path(home, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return n, fmt.Errorf("session: removing stale record %s: %w", id, err)
		}
		n++
	}
	return n, nil
}
