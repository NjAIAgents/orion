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
	line, found := ResultLine(out)
	if !found {
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

// ResultLine finds the run's OWN closing result object in a stream capture.
//
// It is not "the last JSON object", which is what this used to look for, and
// that is the whole bug: with --output-format stream-json the CLI keeps
// talking after the result frame. A run that used background tasks ends with
//
//	{"type":"result",...,"num_turns":108,"total_cost_usd":13.39,"usage":{...}}
//	{"type":"system","subtype":"background_tasks_changed",...}
//	{"type":"system","subtype":"task_updated",...}
//	{"type":"system","subtype":"task_notification",...}
//
// Every one of those trailing lines is a well-formed JSON object, so taking
// the last one handed the parser a frame with no usage on it, which reported
// as "this run consumed nothing". It hit the LONG runs by definition -- a run
// long enough to spawn background work is the one with background frames
// after its result -- so the report lost its single largest contributor while
// short runs kept reporting correctly (OR-219). The observed OR-168 report
// said $1.03; the implementer run it dropped had cost $13.40 on its own.
//
// So the result frame is identified by what it SAYS it is. Scanning from the
// end still, because the capture is a bounded tail that may begin mid-object
// and because warnings can precede the stream.
//
// The fallback covers --output-format json, whose single object predates the
// type field: any trailing object that carries the result's own vocabulary
// (num_turns, total_cost_usd, is_error) and claims no other type.
func ResultLine(out string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var fallback string
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
			continue
		}
		// Pointers so "absent" and "present and zero" stay distinguishable:
		// a genuine result reporting num_turns 0 is a result, and a system
		// frame that happens to carry none is not.
		var probe struct {
			Type     string   `json:"type"`
			NumTurns *int     `json:"num_turns"`
			CostUSD  *float64 `json:"total_cost_usd"`
			IsError  *bool    `json:"is_error"`
		}
		if json.Unmarshal([]byte(s), &probe) != nil {
			continue
		}
		if probe.Type == "result" {
			return s, true
		}
		if fallback == "" && probe.Type == "" &&
			(probe.NumTurns != nil || probe.CostUSD != nil || probe.IsError != nil) {
			fallback = s
		}
	}
	return fallback, fallback != ""
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
