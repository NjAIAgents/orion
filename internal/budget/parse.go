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
// Two details that matter for accounting honestly:
//
// `input_tokens` in the top-level usage block already includes the cached
// portion for billing purposes, so adding cache_read on top would double
// count. The cache fields are kept separately for reporting only.
//
// A trivial prompt still reports roughly 30k input tokens. That is the system
// prompt and cached context, and it is a floor paid on every invocation. It
// is the single most important number for anyone planning a chain of stages:
// nine small stages cost nine floors before any work happens.
func FromResultJSON(out string) (Run, bool) {
	line := lastJSONObject(out)
	if line == "" {
		return Run{}, false
	}
	var d struct {
		CostUSD float64 `json:"total_cost_usd"`
		Usage   struct {
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
		CostUSD:      d.CostUSD,
		InputTokens:  d.Usage.InputTokens,
		OutputTokens: d.Usage.OutputTokens,
	}
	// Cache creation is billed on top of input, unlike cache reads.
	r.InputTokens += d.Usage.CacheCreate

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

// ContextPressure reports how full the context got, as a percentage.
//
// Orion cannot trigger compaction: the CLI exposes no flag or setting for it.
// What it can do is report when a stage is running close to the window, which
// is the signal that the stage is doing too much and should be split.
//
// In practice Orion's design already avoids the usual accumulation problem,
// because every stage is a separate invocation that reads committed artifacts
// rather than inheriting a transcript. Context resets at each stage boundary
// by construction, which is why the artifact chain is also the compaction
// strategy.
func ContextPressure(r Run) int {
	if r.ContextWindow <= 0 {
		return 0
	}
	return int(float64(r.InputTokens) / float64(r.ContextWindow) * 100)
}
