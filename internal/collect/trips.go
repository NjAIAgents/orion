package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Tracking breaker trips per ticket (OR-458).
//
// internal/hook's breaker records a trip per SESSION -- state.Session has no
// ticket key at all, because a hook fires inside a worktree with no tracker
// context of its own. That is the right scope for the breaker itself, but it
// means nothing anywhere counts how many times a given TICKET has tripped it
// across its lifetime: the flag lives one file per session, is read once by
// internal/work's settleTripResidue to decide what a run's own ending says,
// and is never accumulated. queue.Facts.Trips (internal/queue/plan.go) exists
// to evict a ticket that keeps tripping the breaker without landing, and had
// no reader to call: this is that reader's other half, the write side.
//
// Recorded at settleTripResidue (internal/work/residue.go), the one place
// that already resolves both a tripped session AND the ticket key it belongs
// to -- internal/hook itself never sees a ticket key, so the count cannot be
// kept there.

// TripState is one ticket's trip history.
type TripState struct {
	Key   string `json:"key"`
	Trips []Trip `json:"trips"`
}

// Trip is one breaker trip attributed to a ticket.
type Trip struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
}

// Count is how many trips are on record.
func (s TripState) Count() int { return len(s.Trips) }

func tripsPath(wsDir string) string {
	return filepath.Join(wsDir, ".orion", "breaker-trips.json")
}

type tripsFile struct {
	Version int                  `json:"version"`
	States  map[string]TripState `json:"states"`
}

func loadTrips(wsDir string) tripsFile {
	f := tripsFile{Version: 1, States: map[string]TripState{}}
	b, err := os.ReadFile(tripsPath(wsDir))
	if err != nil {
		return f
	}
	if json.Unmarshal(b, &f) != nil || f.States == nil {
		return tripsFile{Version: 1, States: map[string]TripState{}}
	}
	return f
}

func writeTrips(wsDir string, f tripsFile) error {
	p := tripsPath(wsDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// RecordTrip appends one breaker trip to a ticket's history.
//
// Called once per settled run that had a trip on record -- settleTripResidue
// already reads kind/detail off the session that tripped, so this simply
// carries that same evidence into a record that survives the worktree.
func RecordTrip(wsDir, key, kind, detail string) error {
	f := loadTrips(wsDir)
	s := f.States[key]
	s.Key = key
	s.Trips = append(s.Trips, Trip{At: time.Now().UTC(), Kind: kind, Detail: detail})
	f.States[key] = s
	return writeTrips(wsDir, f)
}

// Trips reports how many times the breaker has tripped for a ticket, and
// whether that could be read at all.
//
// Exported for the queue manager (OR-243, OR-458), matching FixRounds's
// contract exactly: a workspace with no breaker-trips.json is UNKNOWN, not
// zero. A cleaned-up workspace says nothing about how many times a ticket
// tripped the breaker, and reading that silence as "never" is how a ticket
// that has already tripped it twice gets evicted and re-admitted with no
// memory of why.
func Trips(wsDir, key string) (n int, known bool) {
	if _, err := os.Stat(tripsPath(wsDir)); err != nil {
		return 0, false
	}
	s, ok := loadTrips(wsDir).States[key]
	if !ok {
		return 0, true
	}
	return s.Count(), true
}

// clearTrips forgets a ticket's trip history, once it has merged or been
// given up on -- the same reason clearFixes exists in fixes.go: a ticket
// reopened months later must not start pre-evicted on trips from a previous
// life of the same key.
func clearTrips(wsDir, key string) error {
	f := loadTrips(wsDir)
	if _, ok := f.States[key]; !ok {
		return nil
	}
	delete(f.States, key)
	return writeTrips(wsDir, f)
}
