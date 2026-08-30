package supervisor

// Recognising the one failure that is neither the agent's fault nor a quota
// wall: the CLI is not signed in.
//
// The CLI says so precisely. Every one of the three runs that died within five
// seconds on 2026-08-30 carried this result frame:
//
//	{"is_error":true, "terminal_reason":"api_error", "duration_api_ms":0,
//	 "num_turns":1, "total_cost_usd":0, "model":"<synthetic>",
//	 "result":"Anthropic profile login expired - Re-authenticate your Anthropic profile"}
//
// The cause and the remedy are one sentence, written by the CLI, in a field
// Orion already parses. It was thrown away and replaced with "claude exited 1"
// (OR-212), which an operator cannot tell from a crash, a bad prompt, a sandbox
// denial or a broken branch -- and every one of those wants a different
// response, while this one wants one command and thirty seconds.
//
// STRUCTURE FIRST, WORDING SECOND, and both are required. A terminal_reason of
// api_error with no turn taken and nothing spent is structurally distinct from
// a run that did work and failed; the text is what separates an expired login
// from every OTHER api_error. Matching on either alone would misreport the
// other -- an overloaded upstream as a login problem, or a real login problem
// as an ordinary failure.
//
// The patterns are matched against the CLI's own result field only, never the
// agent's prose, for the reason internal/quota's errorText spells out at
// length: an agent working a ticket about authentication says "re-authenticate"
// constantly, and that is data, not a statement about Orion's runtime.

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/budget"
)

// loginExpired are the shapes the CLI uses to say it has no usable credential.
// Kept as data, like quota's patterns, so a new wording is a one-line addition
// from evidence rather than a rewrite.
var loginExpired = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\blogin\b.{0,20}\bexpired\b`),
	regexp.MustCompile(`(?i)re-?authenticate`),
	regexp.MustCompile(`(?i)\bnot authenticated\b`),
	regexp.MustCompile(`(?i)\bauthentication_error\b`),
	regexp.MustCompile(`(?i)\binvalid (api key|bearer token)\b`),
	regexp.MustCompile(`(?i)\b(run|use) /?login\b`),
}

// AuthFailure reports whether a run died because the CLI is not authenticated,
// and the sentence to tell the operator when it did.
//
// out is the run's captured output; the verdict is read from its own result
// frame, which is the only place the CLI states this.
func AuthFailure(out string) (string, bool) {
	line, found := budget.ResultLine(out)
	if !found {
		return "", false
	}
	var d struct {
		IsError        bool    `json:"is_error"`
		TerminalReason string  `json:"terminal_reason"`
		NumTurns       int     `json:"num_turns"`
		CostUSD        float64 `json:"total_cost_usd"`
		Result         string  `json:"result"`
	}
	if json.Unmarshal([]byte(line), &d) != nil {
		return "", false
	}
	// One turn, because the refusal itself is reported as a turn. More than
	// that is a session that got somewhere.
	if !d.IsError || d.TerminalReason != "api_error" || d.NumTurns > 1 || d.CostUSD > 0 {
		return "", false
	}
	// A run that spent anything did work, whatever it says afterwards. Asked of
	// the ledger's own parser rather than re-summed here: it already knows the
	// three input counts are disjoint, and a second opinion about that would
	// drift from the first.
	if _, spent := budget.FromResultJSON(out); spent {
		return "", false
	}

	cause := strings.TrimRight(strings.TrimSpace(d.Result), " .")
	if cause == "" {
		return "", false
	}
	for _, re := range loginExpired {
		if re.MatchString(cause) {
			return authMessage(cause), true
		}
	}
	return "", false
}

// authMessage names the cause and the fix, in that order.
//
// The CLI's own sentence is carried verbatim rather than paraphrased. This is
// the standard OR-11 set for Jira -- doctor names WHICH credential source
// failed rather than saying auth failed -- applied to the other credential
// Orion depends on.
func authMessage(cause string) string {
	return "claude is not authenticated: " + cause +
		". Run: claude, sign in, then restart the watcher."
}
