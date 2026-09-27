package watch

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// Retrying a failed ticket without a person (OR-543).
//
// On log-triage-agent, 2026-09-26, every ticket that failed for a reason
// outside its own code -- CI with no Python, a secret scan reading another
// branch, a module a sibling ticket had not landed yet, a merge conflict with
// work that landed after it started -- sat in orion-failed until someone
// relabelled it by hand, and the watcher stopped behind them. What all of
// those had in common is that the fix arrived as a change to the work
// branch. So that is the signal: once the work branch has moved since a
// ticket failed, it is worth one more attempt against the new base.
//
// Capped, because a real defect also fails again: maxFailedRetries attempts,
// the same two the eviction ledger and the fix-round ceiling use, and then the
// ticket stays failed and is named to a person (OR-423).
//
// A missing prerequisite is the case this makes autonomous by construction:
// the ticket is HELD in orion-failed until something lands, then retried --
// it is never retried against the base it already failed on.

// maxFailedRetries is how many times one ticket is requeued automatically.
const maxFailedRetries = 2

// retryEntry is one failed ticket's record.
type retryEntry struct {
	// Count is how many automatic requeues it has had, ever. Not reset when it
	// fails again: the cap is per ticket, so a ticket that keeps failing
	// reaches a person instead of cycling.
	Count int `json:"count"`
	// Base is the work-branch head when it was last seen failed. A retry waits
	// for the head to move past it.
	Base string    `json:"base"`
	Seen time.Time `json:"seen"`
}

func retryPath(home string) string { return filepath.Join(home, "state", "failed-retries.json") }

func loadRetries(home string) map[string]retryEntry {
	m := map[string]retryEntry{}
	b, err := os.ReadFile(retryPath(home))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveRetries(home string, m map[string]retryEntry) error {
	if err := os.MkdirAll(filepath.Dir(retryPath(home)), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(retryPath(home), append(b, '\n'), 0o644)
}

// retryFailed requeues each orion-failed ticket whose work branch has moved
// since it failed, within the cap. It returns the keys still failed and still
// retryable (waiting for the branch to move), the keys out of retries, and
// whether anything was requeued.
func retryFailed(opts Options, deps Deps, w io.Writer, rows []tracker.Issue) (waiting, exhausted []string, moved bool) {
	failed := failedKeys(rows)
	if len(failed) == 0 {
		return nil, nil, false
	}
	if deps.Requeue == nil || deps.BaseHead == nil {
		return nil, failed, false
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	ledger := loadRetries(opts.Home)
	changed := false
	for _, key := range failed {
		e, known := ledger[key]
		if e.Count >= maxFailedRetries {
			exhausted = append(exhausted, key)
			continue
		}
		head := deps.BaseHead(opts.Home, registry.ProjectOf(key))
		if head == "" {
			// Cannot tell whether anything changed, so nothing is retried: a
			// retry against the same base spends a run to fail the same way.
			waiting = append(waiting, key)
			continue
		}
		if !known {
			ledger[key] = retryEntry{Base: head, Seen: now()}
			changed = true
			waiting = append(waiting, key)
			continue
		}
		if head == e.Base {
			waiting = append(waiting, key)
			continue
		}
		if opts.DryRun {
			ui.Say(w, key, events.ActorOrion, ui.VerbWorking,
				"would requeue: the work branch moved since it failed (retry %d of %d)", e.Count+1, maxFailedRetries)
			continue
		}
		if err := deps.Requeue(key, opts.QueueLabel); err != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "could not requeue: %v", err)
			waiting = append(waiting, key)
			continue
		}
		e.Count++
		e.Base, e.Seen = head, now()
		ledger[key] = e
		changed, moved = true, true
		ui.Say(w, key, events.ActorOrion, ui.VerbOK,
			"requeued: the work branch moved since it failed, so it runs again on the new base (retry %d of %d)",
			e.Count, maxFailedRetries)
	}
	if changed {
		if err := saveRetries(opts.Home, ledger); err != nil {
			ui.Say(w, "", events.ActorOrion, ui.VerbWarn,
				"could not record retries: %v -- a restart may retry a ticket past its cap", err)
		}
	}
	return waiting, exhausted, moved
}

// waitingHint ends the line for failed tickets that will be retried.
// Constant, so the line groups and collapses.
var waitingHint = fmt.Sprintf("orion-failed; each is retried automatically once the work branch moves (up to %d times)",
	maxFailedRetries)
