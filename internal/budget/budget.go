// Package budget accounts for what Orion spends and stops it at thresholds.
//
// WHAT THIS IS NOT, stated first because the distinction matters: this is not
// your Anthropic subscription's weekly limit. There is no interface for
// reading that. `claude --help` has no usage command, and a run's JSON result
// reports what THAT run consumed, never what remains on the plan. Any
// percentage Orion showed against the provider's real quota would be invented.
//
// So this tracks a budget YOU set, over a rolling seven days, from figures
// each run genuinely reports:
//
//	usage.input_tokens, usage.output_tokens, cache read/creation
//	total_cost_usd
//	modelUsage[model].contextWindow
//
// Crossing a threshold stops the run and waits for a human. The thresholds
// exist because the failure being prevented is unattended spend, and an
// unattended process cannot be trusted to judge its own budget.
package budget

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/orion-sdlc/orion/internal/procsafe"
)

// Window is the accounting period. Seven days to match how subscription
// limits are usually described, so a budget set from a weekly allowance is
// compared against a like period.
const Window = 7 * 24 * time.Hour

// Thresholds are the fractions at which Orion stops for confirmation.
// Ascending, and each fires once per window.
var Thresholds = []int{50, 75, 90, 95}

// Run is one supervised invocation's cost.
type Run struct {
	At        time.Time `json:"at"`
	Workspace string    `json:"workspace,omitempty"`
	Stage     string    `json:"stage,omitempty"`
	CostUSD   float64   `json:"cost_usd"`
	// InputTokens includes cache reads and creation, because they are billed
	// and they dominate: a trivial prompt still carries roughly 30k input
	// tokens of system prompt and cached context. Counting only "real" input
	// would understate a chain of small stages by an order of magnitude.
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	ContextWindow int    `json:"context_window,omitempty"`
	Model         string `json:"model,omitempty"`
	// The three input counts kept apart, alongside the billed sum above.
	//
	// The ledger only ever needs the sum -- what a week cost is one number --
	// but a per-ticket report cannot use it: cache reads are billed at a
	// fraction of fresh input, so a row that lumps them together misstates
	// both the usage and the price of the same run. Reported separately here
	// so the sum stays the thing the budget gates on and the report can still
	// say which kind of token was spent.
	PromptTokens      int `json:"prompt_tokens,omitempty"`       // input_tokens, cache excluded
	CacheCreateTokens int `json:"cache_create_tokens,omitempty"` // written to cache
	CacheReadTokens   int `json:"cache_read_tokens,omitempty"`   // served from cache
	// Turns is num_turns, what the run reported about its own length.
	Turns int `json:"turns,omitempty"`
}

// Ledger is the durable record plus which thresholds have been acknowledged.
type Ledger struct {
	Runs []Run `json:"runs"`
	// Acked records thresholds a human has confirmed, with the window start
	// they were confirmed for. Storing the window start makes acks expire
	// naturally rather than needing a reset.
	Acked map[string]time.Time `json:"acked,omitempty"`
}

// Limits are what the user set. Zero means unlimited for that dimension,
// which is a deliberate choice here and the opposite of the circuit
// breaker's convention: a budget nobody set should not invent one.
type Limits struct {
	WeeklyUSD    float64
	WeeklyTokens int
}

func (l Limits) Set() bool { return l.WeeklyUSD > 0 || l.WeeklyTokens > 0 }

// Status is the answer to "where am I".
type Status struct {
	SpentUSD    float64
	Tokens      int
	Runs        int
	PercentUSD  int
	PercentTok  int
	Percent     int // the higher of the two, which is what gates
	Limits      Limits
	WindowStart time.Time
	// Crossed is the highest unacknowledged threshold, or 0.
	Crossed int
}

func path(home string) string { return filepath.Join(home, "usage.json") }

// Load reads the ledger, pruning anything outside the window. Pruning on
// read keeps the file bounded without a separate sweep.
func Load(home string) (*Ledger, error) {
	l := &Ledger{Acked: map[string]time.Time{}}
	b, err := os.ReadFile(path(home))
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return l, err
	}
	if err := json.Unmarshal(b, l); err != nil {
		// A corrupt ledger must not disable accounting. Start clean and say
		// so at the call site rather than refusing to run.
		return &Ledger{Acked: map[string]time.Time{}}, fmt.Errorf("usage ledger unreadable, starting fresh: %w", err)
	}
	if l.Acked == nil {
		l.Acked = map[string]time.Time{}
	}
	cutoff := time.Now().Add(-Window)
	kept := l.Runs[:0]
	for _, r := range l.Runs {
		if r.At.After(cutoff) {
			kept = append(kept, r)
		}
	}
	l.Runs = kept
	return l, nil
}

func lockPath(home string) string { return filepath.Join(home, "usage.lock") }

func (l *Ledger) Save(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	// Spend, token counts, workspace ids and the ideas behind them, so
	// owner-only. The temp file is per-process: see procsafe.WriteFile.
	return procsafe.WriteFile(path(home), b, 0o600)
}

// Update applies fn to the ledger under a cross-process lock and saves the
// result. This is the ONLY correct way to modify the ledger when more than
// one Orion process may be running (OR-138).
//
// Save alone is not enough. The lost update happens between the read and the
// write: two watchers each load the same ledger, each append their own run,
// and the second rename discards the first one's spend. Locking only the
// write would leave that untouched. So the lock spans load, mutate and save.
//
// A lock timeout does not fail the update. Losing an accounting row is bad;
// stalling a watcher because another watcher is mid-write is worse. The
// update proceeds unserialized and procsafe.ErrLockTimeout is returned
// alongside a successful write, so the caller can report the degradation
// without treating it as a failure. Callers should check errors.Is.
func Update(home string, fn func(*Ledger)) error {
	release, lockErr := procsafe.Lock(lockPath(home))
	defer release()

	l, loadErr := Load(home)
	fn(l)
	if err := l.Save(home); err != nil {
		return err
	}
	if lockErr != nil {
		return lockErr
	}
	// A corrupt ledger is reported by Load and started fresh rather than
	// refused; surface that here rather than swallowing it.
	return loadErr
}

// Record appends a run.
func (l *Ledger) Record(r Run) {
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	l.Runs = append(l.Runs, r)
}

// Status totals the window and reports the highest unacknowledged threshold.
func (l *Ledger) Status(lim Limits) Status { return l.StatusWith(lim, Run{}) }

// StatusWith is Status plus spend that has not been recorded yet -- what the
// runs currently in flight are expected to cost.
//
// This is the difference between a budget check and admission control. Read
// the ledger and start, and serially that is correct because there is nothing
// in flight to miss. Run several tickets at once and all of them pass the same
// check and then all of them spend, so a 95% checkpoint is sailed straight
// past by the runs already going. Counting the outstanding reservations is
// what makes the checkpoint hold for the second concurrent run as well as the
// first.
func (l *Ledger) StatusWith(lim Limits, pending Run) Status {
	now := time.Now()
	s := Status{Limits: lim, WindowStart: now.Add(-Window)}
	for _, r := range l.Runs {
		if r.At.Before(s.WindowStart) {
			continue
		}
		s.SpentUSD += r.CostUSD
		s.Tokens += r.InputTokens + r.OutputTokens
		s.Runs++
	}
	s.SpentUSD += pending.CostUSD
	s.Tokens += pending.InputTokens + pending.OutputTokens
	if lim.WeeklyUSD > 0 {
		s.PercentUSD = int(s.SpentUSD / lim.WeeklyUSD * 100)
	}
	if lim.WeeklyTokens > 0 {
		s.PercentTok = int(float64(s.Tokens) / float64(lim.WeeklyTokens) * 100)
	}
	// Gate on whichever dimension is further along. Someone who set both
	// meant both to hold, so the binding one is the stricter.
	s.Percent = s.PercentUSD
	if s.PercentTok > s.Percent {
		s.Percent = s.PercentTok
	}

	if !lim.Set() {
		return s
	}
	for _, t := range Thresholds {
		if s.Percent >= t && !l.isAcked(t, s.WindowStart) {
			s.Crossed = t // keep ascending so the highest wins
		}
	}
	return s
}

func (l *Ledger) isAcked(t int, windowStart time.Time) bool {
	at, ok := l.Acked[fmt.Sprint(t)]
	// An ack made before this window started has expired with it.
	return ok && at.After(windowStart)
}

// Ack confirms a threshold for the current window.
func (l *Ledger) Ack(t int) {
	if l.Acked == nil {
		l.Acked = map[string]time.Time{}
	}
	l.Acked[fmt.Sprint(t)] = time.Now().UTC()
}

// AckAll confirms every threshold at or below the crossed one, so a user who
// returns after a long absence is not stopped four times in a row.
func (l *Ledger) AckAll(upTo int) {
	for _, t := range Thresholds {
		if t <= upTo {
			l.Ack(t)
		}
	}
}

// Message renders the stop notice. It names the limit as the user's own, so
// nobody reads it as the provider's remaining quota.
func (s Status) Message() string {
	var b string
	b += fmt.Sprintf("BUDGET CHECKPOINT: %d%% of your weekly Orion budget is used.\n", s.Percent)
	if s.Limits.WeeklyUSD > 0 {
		b += fmt.Sprintf("  spend    $%.2f of $%.2f (%d%%)\n", s.SpentUSD, s.Limits.WeeklyUSD, s.PercentUSD)
	}
	if s.Limits.WeeklyTokens > 0 {
		b += fmt.Sprintf("  tokens   %s of %s (%d%%)\n",
			humanInt(s.Tokens), humanInt(s.Limits.WeeklyTokens), s.PercentTok)
	}
	b += fmt.Sprintf("  runs     %d in the last 7 days\n", s.Runs)
	b += "\n  This is the budget you configured, not your Anthropic plan's weekly limit.\n"
	b += "  Orion cannot read that; no interface reports it.\n"
	b += fmt.Sprintf("\n  Continue:  orion budget ack %d\n", s.Crossed)
	b += "  Review:    orion budget status\n"
	return b
}

func humanInt(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// Recent returns the newest runs first, for reporting.
func (l *Ledger) Recent(n int) []Run {
	out := append([]Run(nil), l.Runs...)
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
