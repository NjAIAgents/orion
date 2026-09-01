package supervisor

// Naming a non-zero exit, and what it cost (OR-245).
//
// classify() knows only the exit code, so every failure that is not a timeout,
// an interrupt or a tripped breaker leaves as "claude exited 1". An operator
// reading that line cannot tell a crashed CLI from a lost network from an
// expired login from a session that simply grew too large -- and each of those
// wants a different response. OR-212 fixed exactly this for the login; this is
// the same fix arriving from the other three directions.
//
// The evidence:
//
//	OR-224, 2026-08-31 01:27, implementer on opus:
//	  turns 121, seconds 1763, cost_usd 17.23, cache_read 24,232,907, exit 1,
//	  reason "claude exited 1"
//
// $17.23 spent to produce nothing, and the number was discoverable only by
// reading the event log.
//
// STRUCTURE FIRST, WORDING SECOND, as auth.go puts it, and here the structure
// is stronger than anything a pattern could give. A run that fills its context
// window says so in NUMBERS Orion already measures per turn: PeakContext
// against ContextWindow. That is a measurement, not a reading of the agent's
// prose -- which is the rule OR-192 set and internal/quota's errorText
// explains at length. The one place text is consulted at all is the network
// case, and there it is the CLI's own result field only, never agent output.
//
// WHAT THIS DELIBERATELY WILL NOT DO. It will not call a failure a context
// exhaustion because the run read a lot of tokens. A large cache_read is what
// a long healthy run looks like too: cache reads are re-sent context, so they
// grow with turns whether or not the window ever filled. Guessing from them
// would put a confident wrong sentence in front of an operator, which is worse
// than the bare exit code it replaced. Without the window measurement the
// failure is reported as unclassified -- with its cost, which is the part that
// was missing either way.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/budget"
)

// exhaustionShare is the fraction of the context window a peak turn has to
// reach before the window is called the cause.
//
// 0.9 rather than 1.0 because the last turn that would have overflowed is
// never sent: the CLI refuses it, so the highest prompt actually measured sits
// just below the ceiling. Rather than 0.75, because a run can legitimately
// peak high mid-session, drop back and fail for an unrelated reason.
const exhaustionShare = 0.9

// networkFault are the shapes the CLI uses to say it could not reach the API.
// Data, like auth.go's loginExpired, so a new wording is a one-line addition
// from evidence.
var networkFault = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bconnection (refused|reset|closed|error)\b`),
	regexp.MustCompile(`(?i)\b(network|socket) (error|failure|unreachable)\b`),
	regexp.MustCompile(`(?i)\b(dns|getaddrinfo|enotfound|econnreset|etimedout)\b`),
	regexp.MustCompile(`(?i)\bfetch failed\b`),
	regexp.MustCompile(`(?i)\bunable to (reach|connect to)\b`),
}

// FailureReason names the likely cause of a non-zero exit, with what the run
// spent, or reports that it cannot name one.
//
// out is the run's captured output; res is the result as classify() and the
// stream left it. Called after AuthFailure, which owns the run that never got
// a turn in: the two are structurally disjoint -- that one spent nothing, and
// every case here did work first -- but order keeps it that way by construction
// rather than by argument.
func FailureReason(out string, res *Result) (string, bool) {
	if res == nil || res.ExitCode == 0 || res.Killed || res.Unauthenticated {
		return "", false
	}
	// A run that never emitted a frame is the existing mid-run-cutoff path's,
	// and it already says something truer than anything derivable here.
	if !res.Started {
		return "", false
	}
	run, spent := budget.FromResultJSON(out)
	if !spent {
		return "", false
	}

	if cause, ok := filledTheWindow(res, run); ok {
		return cause + costOf(run) + ". Give it a smaller ticket, or keep the " +
			"repository reads out of the parent by exploring through subagents.", true
	}
	if cause, ok := lostTheNetwork(out); ok {
		return "claude could not reach the API: " + cause + costOf(run) +
			". Nothing is wrong with the ticket; retry when the connection is back.", true
	}
	return fmt.Sprintf("claude exited %d with no cause reported", res.ExitCode) +
		costOf(run) + ". The run's log is the only account of what happened.", true
}

// filledTheWindow reports the measured exhaustion.
//
// The window comes from the stream when the stream said, and from the result
// JSON otherwise -- the same two-source arrangement Result.ContextWindow
// documents, resolved here because this is the first caller that needs one
// number rather than both.
func filledTheWindow(res *Result, run budget.Run) (string, bool) {
	window := res.ContextWindow
	if window == 0 {
		window = run.ContextWindow
	}
	if window <= 0 || res.PeakContext <= 0 {
		return "", false
	}
	if float64(res.PeakContext) < exhaustionShare*float64(window) {
		return "", false
	}
	return fmt.Sprintf(
		"the session filled its context window: one turn sent %s tokens of a %s window",
		commas(res.PeakContext), commas(window)), true
}

// lostTheNetwork reads the CLI's own result field, never the agent's prose.
//
// The discipline is auth.go's and internal/quota's, and it matters more here
// than there: an agent working a ticket about retries and timeouts writes
// "connection reset" in its own sentences all day, and that is data about the
// ticket, not about Orion's runtime.
func lostTheNetwork(out string) (string, bool) {
	line, found := budget.ResultLine(out)
	if !found {
		return "", false
	}
	var d struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if json.Unmarshal([]byte(line), &d) != nil || !d.IsError {
		return "", false
	}
	cause := strings.TrimRight(strings.TrimSpace(d.Result), " .")
	if cause == "" {
		return "", false
	}
	for _, re := range networkFault {
		if re.MatchString(cause) {
			return cause, true
		}
	}
	return "", false
}

// costOf is the clause every branch above ends with.
//
// PROMINENT BY CONSTRUCTION. The failed OR-224 run cost $17.23 and produced
// nothing, and finding that out meant reading events.jsonl. Appending it to
// the reason puts it in the terminal, the Jira comment and the Slack message
// at once, because all three render the same sentence (work/fault.go's
// Describe makes that a rule).
func costOf(run budget.Run) string {
	switch {
	case run.Turns > 0 && run.CostUSD > 0:
		return fmt.Sprintf(", after %d turns and $%.2f", run.Turns, run.CostUSD)
	case run.CostUSD > 0:
		return fmt.Sprintf(", after $%.2f", run.CostUSD)
	case run.Turns > 0:
		return fmt.Sprintf(", after %d turns", run.Turns)
	}
	return ""
}

// commas groups a token count, because 24232907 and 2423290 look alike at a
// glance and the whole point of the line is that a person reads it.
func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
