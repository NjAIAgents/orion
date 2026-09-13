package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Tracking stranded worktrees per ticket (OR-458).
//
// "Stranded" here matches settleTripResidue's own meaning (internal/work/
// residue.go): a run ended holding uncommitted work that the snapshot commit
// itself failed to preserve, so it was KEPT, uncommitted, in the worktree --
// the case that prints "orion settle <key>" and blocks the next rebase.
// Nothing before this counted how many PASSES in a row that has been true for
// a given ticket; queue.Facts.Stranded (internal/queue/plan.go) exists to
// evict a ticket whose worktree could not be settled after N passes, and had
// no reader to call.
//
// A COUNT OF CONSECUTIVE passes, not a lifetime total like Trips: the
// doc comment on queue.Facts.Stranded says "could not be settled after N
// passes", and a ticket settled cleanly once should not carry an old
// stranding into a new one. Reset to zero the moment a pass ends without an
// unresolved snapshot, mirroring queue.Ledger.Passes' own "measures NEGLECT,
// not age" rule.

// StrandedState is one ticket's current streak.
type StrandedState struct {
	Key        string    `json:"key"`
	Streak     int       `json:"streak"`
	LastAt     time.Time `json:"last_at"`
	LastDetail string    `json:"last_detail,omitempty"`
}

func strandedPath(wsDir string) string {
	return filepath.Join(wsDir, ".orion", "stranded.json")
}

type strandedFile struct {
	Version int                      `json:"version"`
	States  map[string]StrandedState `json:"states"`
}

func loadStranded(wsDir string) strandedFile {
	f := strandedFile{Version: 1, States: map[string]StrandedState{}}
	b, err := os.ReadFile(strandedPath(wsDir))
	if err != nil {
		return f
	}
	if json.Unmarshal(b, &f) != nil || f.States == nil {
		return strandedFile{Version: 1, States: map[string]StrandedState{}}
	}
	return f
}

func writeStranded(wsDir string, f strandedFile) error {
	p := strandedPath(wsDir)
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

// RecordStranded marks this pass as one where the ticket's worktree could
// not be settled, incrementing its consecutive streak.
//
// Called from settleTripResidue's unresolved branch -- the commit that would
// have preserved the residue failed, so the work is kept, uncommitted, in
// the worktree, which is precisely the condition this counts.
func RecordStranded(wsDir, key, detail string) error {
	f := loadStranded(wsDir)
	s := f.States[key]
	s.Key = key
	s.Streak++
	s.LastAt = time.Now().UTC()
	s.LastDetail = detail
	f.States[key] = s
	return writeStranded(wsDir, f)
}

// ClearStranded resets a ticket's streak once a pass settles cleanly.
//
// Called wherever a ticket's worktree is next found settled -- a stranding
// three passes ago that has since been cleared by `orion settle` or a fresh
// run must not go on counting toward eviction.
func ClearStranded(wsDir, key string) error {
	f := loadStranded(wsDir)
	if _, ok := f.States[key]; !ok {
		return nil
	}
	delete(f.States, key)
	return writeStranded(wsDir, f)
}

// Stranded reports how many consecutive passes a ticket's worktree has been
// left unsettled, and whether that could be read at all.
//
// Exported for the queue manager (OR-243, OR-458), matching FixRounds's and
// Trips's contract: a workspace with no stranded.json is UNKNOWN, not zero.
func Stranded(wsDir, key string) (n int, known bool) {
	if _, err := os.Stat(strandedPath(wsDir)); err != nil {
		return 0, false
	}
	s, ok := loadStranded(wsDir).States[key]
	if !ok {
		return 0, true
	}
	return s.Streak, true
}
