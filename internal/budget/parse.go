package budget

import (
	"encoding/json"
	"strings"
)

// FromResultJSON extracts a Run from the JSON a `claude -p --output-format
// json` invocation prints.
//
// The shape was read off a real run rather than assumed:
//
//	{"total_cost_usd":0.19,
//	 "usage":{"input_tokens":34448,"output_tokens":4,
//	          "cache_read_input_tokens":28856,"cache_creation_input_tokens":0},
//	 "modelUsage":{"claude-opus-4-8[1m]":{"contextWindow":1000000,...}}}
//
// The three input counts are DISJOINT, per the Anthropic API: input_tokens
// excludes anything served from cache or written to it. A transcript line
// reading input_tokens=2 beside cache_creation=67399 settles it. An earlier
// version of this parser assumed input_tokens was a superset and added only
// cache creation, which undercounts a cache-heavy turn by orders of
// magnitude.
//
// A trivial prompt still costs roughly 30k tokens once the three are summed.
// That is the system prompt and tool list, and it is a floor paid on every
// invocation: nine small stages pay it nine times before any work happens.
func FromResultJSON(out string) (Run, bool) {
	line := lastJSONObject(out)
	if line == "" {
		return Run{}, false
	}
	var d struct {
		CostUSD  float64 `json:"total_cost_usd"`
		NumTurns int     `json:"num_turns"`
		Usage    struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			CacheRead    int `json:"cache_read_input_tokens"`
			CacheCreate  int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		ModelUsage map[string]struct {
			ContextWindow int `json:"contextWindow"`
			InputTokens   int `json:"inputTokens"`
			OutputTokens  int `json:"outputTokens"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal([]byte(line), &d) != nil {
		return Run{}, false
	}
	r := Run{
		CostUSD:           d.CostUSD,
		OutputTokens:      d.Usage.OutputTokens,
		PromptTokens:      d.Usage.InputTokens,
		CacheCreateTokens: d.Usage.CacheCreate,
		CacheReadTokens:   d.Usage.CacheRead,
		Turns:             d.NumTurns,
	}
	// Sum all three: they do not overlap.
	r.InputTokens = d.Usage.InputTokens + d.Usage.CacheCreate + d.Usage.CacheRead

	for model, mu := range d.ModelUsage {
		r.Model = model
		r.ContextWindow = mu.ContextWindow
		break // one model per run in practice; first is representative
	}
	// A run that reported nothing usable is not worth recording: a zero row
	// would dilute nothing but would imply accounting happened when it did not.
	if r.CostUSD == 0 && r.InputTokens == 0 && r.OutputTokens == 0 {
		return Run{}, false
	}
	return r, true
}

// lastJSONObject finds the final line that looks like a JSON object.
// `claude -p --output-format json` prints one result object, but warnings can
// precede it on the same stream, so scanning from the end is more robust than
// assuming the whole output parses.
func lastJSONObject(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			return s
		}
	}
	return ""
}

// ContextPressure was removed, deliberately, rather than fixed in place.
//
// It divided Run.InputTokens by the context window and called the answer
// "how full the context got". Run.InputTokens is the CUMULATIVE prompt total
// for the whole session -- input plus cache-creation plus cache-read, summed
// over every turn -- and cache_read re-counts the entire cached prefix on
// every single turn. A long run therefore reports several times the window
// size no matter how small its actual context was: an observed run printed
// "context reached 656% of the 1M window".
//
// That is not a fixable coefficient. Nothing in the final result JSON records
// peak occupancy, so the number could not be computed from this data at all.
// Supervisor.reportContextPressure measures it off the stream instead, as a
// maximum over turns, where the information actually exists.
//
// Left as a comment because an exported function whose name promises one
// thing and computes another is worse than an absent one: it reads correct
// at every call site.
