package work

// Telling a failure of the WORK from a failure of the MACHINE.
//
// collect.go marks a ticket orion-failed rather than requeuing it on purpose,
// and that decision is correct: a run that attempted the work and failed needs
// a person to judge WHY before it runs again, or Orion loops on a broken
// ticket at full price. Nothing here weakens it.
//
// An environmental fault is a different thing entirely. Nothing was judged,
// because nothing ran: zero turns, zero tokens, zero cost, no branch work. The
// ticket did not fail; the machine did. There is no judgement about the WORK to
// make, only a question about the MACHINE, and that question has a yes-or-no
// answer a person can give in one reaction.
//
// So the rule this file exists to enforce: RETRY ONLY WHAT NEVER STARTED. Every
// classifier below is gated on evidence that no turn was spent, and where that
// evidence is not available the answer is "not a fault" -- which lands the
// ticket in orion-failed exactly as before. Being wrong in that direction costs
// somebody a label; being wrong in the other direction re-runs work whose
// failure nobody has looked at.

import (
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/supervisor"
)

// FaultKind names one environmental fault. Every one of them is a machine
// problem with a human fix, and the fix is what the message has to carry:
// "Orion stopped" tells the reader nothing they can act on.
type FaultKind string

const (
	// FaultClaudeAuth: the CLI has no usable login (OR-212).
	FaultClaudeAuth FaultKind = "claude-auth"
	// FaultQuota: the plan limit is reached AND the provider did not say when
	// it clears, so OR-192's wait has no time to wait until. The parseable
	// case is already handled by that wait and never reaches here.
	FaultQuota FaultKind = "quota"
	// FaultTracker: Jira could not be reached.
	FaultTracker FaultKind = "tracker"
	// FaultForge: gh could not be reached.
	FaultForge FaultKind = "forge"
	// FaultNJAgents: the delegated toolkit is missing or incomplete, which
	// doctor grades FAIL.
	FaultNJAgents FaultKind = "nj-agents"
)

// Fault is what broke and how to fix it.
type Fault struct {
	Kind FaultKind
	// Cause is the failing component's own words. Carried verbatim rather
	// than paraphrased: the CLI, gh and Jira all state their own problem more
	// precisely than a wrapper can restate it.
	Cause string
	// Fix is the exact remedy. One command where there is one.
	Fix string
}

// Describe is the single sentence that reaches the log, the terminal and
// Slack. One wording everywhere, so a person who reads two of them does not
// have to work out whether they are the same event.
func (f Fault) Describe() string {
	cause := strings.TrimRight(strings.TrimSpace(f.Cause), " .")
	if cause == "" {
		cause = string(f.Kind) + " is not usable"
	}
	if f.Fix == "" {
		return cause
	}
	return cause + ". " + f.Fix
}

// Fix lines. Kept together so the remedy for a fault is edited in one place
// rather than in whichever message happened to be written first.
const (
	fixClaudeAuth = "Run: claude, sign in, then react below."
	fixQuota      = "Wait for the plan limit to reset, then react below. " +
		"The provider did not say when that is, so Orion cannot wait for it itself."
	fixTracker   = "Check the tracker is reachable and ORION_JIRA_* is current, then react below."
	fixForge     = "Run: gh auth status -- fix what it reports, then react below."
	fixNJAgents  = "Run: orion doctor --fix (or clone nj-agents by hand), then react below."
	fixUnchecked = "Fix it, then react below."
)

// NewFault builds a fault from a kind and the failing component's own words,
// attaching the remedy that goes with it.
//
// Exported so a detector outside this package -- the preflight check, which
// wraps doctor and therefore cannot live here -- states the fault without also
// having to know its fix. One remedy per kind, edited in one place.
func NewFault(k FaultKind, cause string) Fault {
	return Fault{Kind: k, Cause: cause, Fix: fixFor(k)}
}

// fixFor is the remedy for a kind, used when a fault is rebuilt from a
// persisted hold rather than from the failure that created it.
func fixFor(k FaultKind) string {
	switch k {
	case FaultClaudeAuth:
		return fixClaudeAuth
	case FaultQuota:
		return fixQuota
	case FaultTracker:
		return fixTracker
	case FaultForge:
		return fixForge
	case FaultNJAgents:
		return fixNJAgents
	}
	return fixUnchecked
}

// faultOf classifies a finished run.
//
// Both arms rest on evidence from the run's own result frame that no turn was
// taken. Unauthenticated is set only after supervisor.AuthFailure has checked
// num_turns, total_cost_usd and the ledger's own view of what was spent;
// QuotaUnwaitable is paired here with Started, because a quota wall reached
// mid-run is a run that DID work and must stay failed.
func faultOf(r *supervisor.Result) (Fault, bool) {
	switch {
	case r == nil:
		return Fault{}, false
	case r.Unauthenticated:
		return Fault{Kind: FaultClaudeAuth, Cause: r.Reason, Fix: fixClaudeAuth}, true
	case r.QuotaUnwaitable && !r.Started:
		return Fault{Kind: FaultQuota, Cause: r.Reason, Fix: fixQuota}, true
	}
	return Fault{}, false
}

// unreachableFault classifies an error from a service Orion depends on.
//
// Only a CONNECTIVITY failure qualifies. A 403 from Jira, a rejected label, a
// repository that does not exist -- those are configuration or data, and they
// will fail identically after any number of retries, so holding the ticket
// would replace a failure a person can read with a queue that never moves.
//
// Every caller is on a path that runs BEFORE the agent does, which is what
// makes the zero-turn rule hold here by construction rather than by check.
func unreachableFault(k FaultKind, err error) (Fault, bool) {
	if err == nil || !looksUnreachable(err.Error()) {
		return Fault{}, false
	}
	return Fault{Kind: k, Cause: err.Error(), Fix: fixFor(k)}, true
}

// connectivity are the shapes a machine uses to say it could not reach
// another one. Kept as data, like quota's patterns and supervisor's login
// patterns, so a new wording is a one-line addition from evidence.
//
// Deliberately narrow. Anything not on this list is treated as the service
// answering and saying no, which is a failure about the work's configuration
// and belongs to a person.
var connectivity = []string{
	"no such host",
	"connection refused",
	"connection reset",
	"network is unreachable",
	"host is unreachable",
	"i/o timeout",
	"tls handshake timeout",
	"context deadline exceeded",
	"temporary failure in name resolution",
	"could not resolve host",
	"502 bad gateway",
	"503 service unavailable",
	"504 gateway",
}

// bareGateway matches a gateway status code standing on its own.
//
// The table above requires the reason phrase, which is how a bare code went
// unclassified: a real error reads "fetching KEY: <code> <body>", and a
// gateway that returns an empty body leaves "fetching KEY: 502 " with nothing
// after the number to match on. That is the exact shape of the outage this
// whole path exists for, so it has to classify.
//
// Bounded on both sides rather than a substring search, because a naked
// strings.Contains("502") also fires on an issue key (FCIA-502), a token
// count, and a duration in milliseconds -- turning a configuration error the
// operator must see into a silent hold that waits for a network to recover
// that was never down. The left boundary allows a colon so "KEY: 502" matches
// without also matching "-502".
var bareGateway = regexp.MustCompile(`(?:^|[\s:])50[234](?:\s|$)`)

func looksUnreachable(msg string) bool {
	msg = strings.ToLower(msg)
	for _, p := range connectivity {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return bareGateway.MatchString(msg)
}
