package watch

import (
	"fmt"
	"strings"
	"time"
)

// noProgress tracks how long the watcher has been cycling without achieving
// anything, and trips when that passes the configured window.
//
// WHY A BREAKER AND NOT A WARNING. On the night OR-428 was written the batch
// integrator ran 91 CI runs over 13.5 hours and merged nothing: five branches
// assembled, went red, re-isolated, and assembled again every twenty minutes
// until morning. Every existing guard was blind to it.
//
//   - The spend breaker watches dollars, and this loop spent none. Assembling
//     a batch and reading CI back involves no LLM at all, so spend stayed flat
//     at $635.03 across the whole night and no checkpoint could fire.
//   - OR-261's "the batch landed nothing" warning fired more than thirty
//     times. It was right every time. Nobody was awake.
//
// A warning that repeats unheard is not a guard. This is the same shape as
// limits.MaxConsecutivePolls one level down (OR-331 -- a QA session polled a
// suite for twenty minutes because waiting was exempt from every counter):
// waiting is legitimate, waiting forever is not.
//
// WHAT COUNTS AS PROGRESS is deliberately generous, because a false trip
// stops a watcher that was working. Any of these resets the clock:
//
//   - a ticket merged, failed, closed, or was otherwise reconciled to a
//     terminal verdict -- something moved
//   - collect reported Changed: it did work, whatever the outcome
//   - a job was started or finished -- agents are running
//
// And these do NOT count, which is the entire point:
//
//   - a batch still waiting on CI. Pending is progress being made elsewhere,
//     and counting it would trip the breaker on a slow CI run, which is the
//     one failure mode that would make an operator disable this.
//   - a passing pull request waiting on a person. The watcher is behaving
//     correctly; the human is the one holding it up, and stopping the watcher
//     would strand the very approval they are about to give.
type noProgress struct {
	// window is how long nothing may happen. Zero disables the breaker.
	window time.Duration
	// since is when the current run of fruitless cycles began. Zero means
	// the watcher has not yet completed a cycle that achieved nothing.
	since time.Time
	// cycles counts them, for the message. A duration alone does not tell an
	// operator whether the watcher was spinning or idling.
	cycles int
}

// newNoProgress returns a breaker for the window. A zero or negative window
// yields a breaker that never trips -- reachable only from a test or a
// caller that bypassed config.Limits.NoProgress, never from orion config.
func newNoProgress(window time.Duration) *noProgress {
	return &noProgress{window: window}
}

// progressed resets the clock. Called for anything that counts as something
// having happened.
func (n *noProgress) progressed() {
	if n == nil {
		return
	}
	n.since = time.Time{}
	n.cycles = 0
}

// idled records a cycle that achieved nothing, and reports whether the
// breaker has now tripped.
//
// The FIRST fruitless cycle only starts the clock; it never trips. A watcher
// with nothing to do is the normal resting state, and a breaker that fired on
// one quiet tick would stop every watcher waiting for its first ticket.
func (n *noProgress) idled(now time.Time) bool {
	if n == nil || n.window <= 0 {
		return false
	}
	n.cycles++
	if n.since.IsZero() {
		n.since = now
		return false
	}
	return now.Sub(n.since) >= n.window
}

// reason is what the operator is told, and it names what did NOT happen
// rather than only that something did not.
//
// The stuck keys are the part worth reading at breakfast: "nothing landed"
// sends someone to the logs, "OR-59 OR-273 OR-274 never landed" sends them
// to the tickets.
func (n *noProgress) reason(now time.Time, stuck []string) string {
	elapsed := now.Sub(n.since).Round(time.Minute)
	s := fmt.Sprintf("stopping: %d cycle(s) over %s achieved nothing",
		n.cycles, elapsed)
	if len(stuck) > 0 {
		s += fmt.Sprintf("; %s never landed", joinKeys(stuck))
	}
	return s + ". Nothing was changed -- the branches and labels are as they were."
}

// joinKeys renders the stuck set. Bounded, because a wide queue would put
// forty keys in a Slack notification nobody can read.
func joinKeys(keys []string) string {
	const max = 8
	if len(keys) <= max {
		return strings.Join(keys, " ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(keys[:max], " "), len(keys)-max)
}
