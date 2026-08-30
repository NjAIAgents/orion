// The durable half of the usage record.
//
// The usage event in the workspace event log is the run's narrative -- it is
// what `orion cost` aggregates, and it belongs next to the claim, the branch
// and the pull request that surround it. But that log is tuned for a live
// activity trace: 2 MiB per file, five generations, and the oldest rows go
// first (see events.MaxBytes). A benchmark wants the exact opposite -- the
// OLDEST rows are the ones that answer "was opus worth it for this class of
// ticket", and rotation deletes them silently.
//
// Two incompatible retention policies over one fact, so the fact is written
// twice from ONE function: Record emits the event and appends the row here.
// Two hand-rolled copies of one fact is what OR-176 was, and the detail keys
// in cost.go already live in one place so the writer and reader cannot drift;
// this is the same discipline one level up.
//
// GLOBAL rather than per-workspace. Events are per-workspace because a run's
// narrative belongs to its project, but the whole point of this file is
// comparing across projects, and that wants one file with a project column
// rather than a glob and a merge. ~/.orion already holds global mutable state
// (usage.json, repos.json) and, per ADR 0004, anything shared there goes
// through internal/procsafe: two `orion watch` processes append at the same
// moment, and the unlocked version of exactly this pattern dropped one run in
// twelve (OR-138).
//
// JSONL rather than a database, also per ADR 0004: greppable, jq-able, and
// DuckDB reads it in place, so full SQL is available without Orion taking a
// cgo dependency that would break the CGO_ENABLED=0 cross-compile.

package cost

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/orion-sdlc/orion/internal/procsafe"
)

// HistoryPath is the never-rotated usage history under ORION_HOME.
func HistoryPath(home string) string { return filepath.Join(home, "usage-history.jsonl") }

func historyLockPath(home string) string { return filepath.Join(home, "usage-history.lock") }

// HistoryRow is one run, as a benchmark needs it.
//
// Everything needed to relate a row to something else is on the row itself:
// the ticket key joins it to the tracker and to the event log, the project
// column is what makes one global file usable, and model and effort are
// recorded AS DISPATCHED. Resolving those later would read today's roster --
// and the roster is mutable (OR-197), so moving the implementer from opus to
// sonnet would silently relabel every run that came before.
//
// The JSON names deliberately mirror the detail keys in cost.go. One
// vocabulary for one fact, so a query written against the event log reads the
// history file unchanged.
type HistoryRow struct {
	Key     string `json:"key,omitempty"`
	Project string `json:"project,omitempty"`
	Actor   string `json:"actor"`
	// Model and Effort are empty when the run was dispatched without an
	// override, meaning the CLI's own defaults were in force. Empty is the
	// honest answer there; a lookup would be a guess.
	Model   string    `json:"model,omitempty"`
	Effort  string    `json:"effort,omitempty"`
	Stage   string    `json:"stage,omitempty"`
	Session string    `json:"session,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`

	Turns      int     `json:"turns"`
	Prompt     int     `json:"in"`
	Output     int     `json:"out"`
	CacheW     int     `json:"cache_create"`
	CacheR     int     `json:"cache_read"`
	CostUSD    float64 `json:"cost_usd"`
	Seconds    float64 `json:"seconds"`
	Exit       int     `json:"exit"`
	Reason     string  `json:"reason,omitempty"`
	UsageKnown bool    `json:"usage_reported"`
	// NeverStarted separates an environmental fault from a failed run, and is
	// omitted when false so the overwhelmingly common case adds no column and
	// rows written before OR-219 stay byte-comparable with rows written after.
	NeverStarted bool `json:"never_started,omitempty"`
}

func historyRow(actor, key string, r Run) HistoryRow {
	return HistoryRow{
		Key: key, Project: r.Project, Actor: actor,
		Model: r.Model, Effort: r.Effort, Stage: r.Stage, Session: r.Session,
		Started: r.StartedAt, Ended: r.EndedAt,
		Turns: r.Turns, Prompt: r.Prompt, Output: r.Output,
		CacheW: r.CacheW, CacheR: r.CacheR,
		CostUSD: r.CostUSD, Seconds: r.Seconds,
		Exit: boolInt(r.Failed), Reason: r.Reason, UsageKnown: r.HaveUsage,
		NeverStarted: r.NeverStarted,
	}
}

// appendHistory adds one row. Append-only: nothing here rewrites, truncates
// or rotates the file, which is the whole reason it exists separately.
func appendHistory(home string, row HistoryRow) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	// Same convention as budget.Update and the repo registry: a lock that
	// cannot be taken degrades to an unserialized write that is REPORTED,
	// never to a blocked watcher. The write still happens.
	release, lockErr := procsafe.Lock(historyLockPath(home))
	defer release()

	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(HistoryPath(home), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return lockErr
}

// ReadHistory loads every recorded row. For tests and for whatever reads the
// history later; nothing in the write path needs it.
func ReadHistory(home string) ([]HistoryRow, error) {
	b, err := os.ReadFile(HistoryPath(home))
	if err != nil {
		return nil, err
	}
	var rows []HistoryRow
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var row HistoryRow
		if err := dec.Decode(&row); err != nil {
			return rows, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
