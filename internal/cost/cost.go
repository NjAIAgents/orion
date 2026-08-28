// Package cost answers "what did this ticket cost" from the event log.
//
// The only cost signal Orion used to produce was a line in a log file,
// discovered after the fact. The ticket -- the artifact a person actually
// revisits -- said nothing about what its automation spent, so deciding
// whether to keep handing a class of work to agents meant reconstructing a
// number by hand from run logs.
//
// So every supervised run writes what it consumed into the event log, keyed
// by the actor id, and this aggregates those lines per ticket. The event log
// is the right home for two reasons: it is already per-workspace, append-only
// and rotated, and the actor ids in it are the PERSISTED ones -- a renamed
// agent still attributes correctly, because the display name is applied here
// at render time rather than stored.
//
// Three rules the aggregation exists to keep:
//
//   - EVERY run counts. The implementation run, each fix-loop re-entry, and
//     any later reviewer or exchange run. A report that quietly counts only
//     the last run is a report that says a ticket was cheap.
//   - A run that DIED still spent tokens. Max-turns, breaker, quota: the
//     usage is part of the ticket's true cost and is counted, and marked.
//   - Usage that is MISSING is said out loud. An older event log, or a run
//     that crashed before its result JSON, means the total is a floor. A
//     lowball number presented as complete is worse than an honest gap.
package cost

import (
	"sort"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/events"
)

// Detail keys on the usage event. Named here, next to the code that reads
// them back, so the writer and the reader cannot drift apart.
const (
	keyTurns    = "turns"
	keyPrompt   = "in"
	keyOutput   = "out"
	keyCacheW   = "cache_create"
	keyCacheR   = "cache_read"
	keyCostUSD  = "cost_usd"
	keySeconds  = "seconds"
	keyExitCode = "exit"
	keyReason   = "reason"
	keyHaveUse  = "usage_reported"
)

// Run is one agent invocation's consumption, as recorded.
type Run struct {
	Actor   string
	Turns   int
	Prompt  int // input tokens, cache excluded
	Output  int
	CacheW  int // cache creation
	CacheR  int // cache read
	CostUSD float64
	Seconds float64
	// Failed marks a run that exited non-zero. It is still counted: it spent
	// what it spent before it died.
	Failed bool
	Reason string
	// HaveUsage is false when the run ended without reporting usage at all.
	// Such a run contributes a row's run count and nothing to its totals,
	// which is exactly why the report has to say so.
	HaveUsage bool
}

// Record writes one run's consumption to the event log.
//
// Called by the supervisor for every run it finishes, successful or not, and
// once per quota-retry attempt: an attempt that hit a wall still paid for the
// context it sent before it did.
func Record(log *events.Log, actor, key string, r Run) {
	if log == nil {
		return
	}
	log.Emit(events.Event{
		Kind: events.KindUsage, Actor: actor, Key: key,
		Msg: msgFor(r),
		Detail: map[string]any{
			keyTurns: r.Turns, keyPrompt: r.Prompt, keyOutput: r.Output,
			keyCacheW: r.CacheW, keyCacheR: r.CacheR,
			keyCostUSD: r.CostUSD, keySeconds: r.Seconds,
			keyExitCode: boolInt(r.Failed), keyReason: r.Reason,
			keyHaveUse: r.HaveUsage,
		},
	})
}

// FromBudgetRun converts what the result JSON reported into a recordable run.
// Keeping the conversion here means the supervisor does not have to know the
// detail-key vocabulary.
func FromBudgetRun(b budget.Run, ok bool, failed bool, reason string, d time.Duration) Run {
	return Run{
		Turns: b.Turns, Prompt: b.PromptTokens, Output: b.OutputTokens,
		CacheW: b.CacheCreateTokens, CacheR: b.CacheReadTokens,
		CostUSD: b.CostUSD, Seconds: d.Seconds(),
		Failed: failed, Reason: reason, HaveUsage: ok,
	}
}

func msgFor(r Run) string {
	switch {
	case !r.HaveUsage:
		return "run ended without reporting usage"
	case r.Failed:
		return "failed run"
	default:
		return "run complete"
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Row is one actor's share of a ticket, or the total.
type Row struct {
	// ID is the persisted actor id; Actor is its display form at render time.
	ID     string
	Actor  string
	Runs   int
	Failed int
	// Missing counts runs that reported no usage. Those runs are in Runs and
	// contribute nothing to the token or cost columns.
	Missing int
	Turns   int
	Prompt  int
	Output  int
	CacheW  int
	CacheR  int
	CostUSD float64
	Seconds float64
}

// Report is a ticket's whole lifecycle, ready to render.
type Report struct {
	Key   string
	Rows  []Row
	Total Row
	// Runs is every run in order, so the report can show turns and wall time
	// per run rather than only the per-actor sums.
	Runs []Run
}

// Empty reports whether nothing at all was recorded for this ticket. True for
// a ticket worked by a build that predates usage recording, which the report
// then says rather than presenting a $0.00 table.
func (r Report) Empty() bool { return len(r.Runs) == 0 }

// Aggregate sums every usage event for one ticket key.
func Aggregate(evs []events.Event, key string) Report {
	rep := Report{Key: key}
	byActor := map[string]*Row{}

	for _, e := range evs {
		if e.Kind != events.KindUsage || !strings.EqualFold(e.Key, key) {
			continue
		}
		run := runFrom(e)
		rep.Runs = append(rep.Runs, run)

		row := byActor[run.Actor]
		if row == nil {
			row = &Row{ID: run.Actor, Actor: actors.Display(run.Actor)}
			byActor[run.Actor] = row
		}
		row.add(run)
		rep.Total.add(run)
	}

	for _, row := range byActor {
		rep.Rows = append(rep.Rows, *row)
	}
	// Most expensive first: the row anybody reading a cost report is looking
	// for is the one that spent the money. Ties break on the id so the order
	// is stable rather than map-random.
	sort.Slice(rep.Rows, func(i, j int) bool {
		if rep.Rows[i].CostUSD != rep.Rows[j].CostUSD {
			return rep.Rows[i].CostUSD > rep.Rows[j].CostUSD
		}
		return rep.Rows[i].ID < rep.Rows[j].ID
	})
	rep.Total.Actor = "total"
	return rep
}

func (r *Row) add(run Run) {
	r.Runs++
	if run.Failed {
		r.Failed++
	}
	if !run.HaveUsage {
		r.Missing++
	}
	r.Turns += run.Turns
	r.Prompt += run.Prompt
	r.Output += run.Output
	r.CacheW += run.CacheW
	r.CacheR += run.CacheR
	r.CostUSD += run.CostUSD
	r.Seconds += run.Seconds
}

func runFrom(e events.Event) Run {
	return Run{
		Actor:     e.Actor,
		Turns:     intOf(e.Detail, keyTurns),
		Prompt:    intOf(e.Detail, keyPrompt),
		Output:    intOf(e.Detail, keyOutput),
		CacheW:    intOf(e.Detail, keyCacheW),
		CacheR:    intOf(e.Detail, keyCacheR),
		CostUSD:   floatOf(e.Detail, keyCostUSD),
		Seconds:   floatOf(e.Detail, keySeconds),
		Failed:    intOf(e.Detail, keyExitCode) != 0,
		Reason:    stringOf(e.Detail, keyReason),
		HaveUsage: boolOf(e.Detail, keyHaveUse),
	}
}

// Detail values arrive as float64 after a JSON round trip, so every reader
// accepts both shapes rather than assuming the in-process one.
func floatOf(d map[string]any, k string) float64 {
	switch v := d[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

func intOf(d map[string]any, k string) int { return int(floatOf(d, k)) }

func stringOf(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return s
}

func boolOf(d map[string]any, k string) bool {
	b, _ := d[k].(bool)
	return b
}
