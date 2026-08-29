package quota

import (
	"strings"
	"testing"
	"time"
)

// The exact shape that paused a healthy run on 2026-08-29. The agent was
// working OR-184, whose description is largely about rate limits, ceilings
// and concurrent sessions, so it wrote those words constantly -- and the
// detector matched the agent quoting its own ticket (OR-192).
//
// Kept verbatim rather than reduced to the phrase alone: the point is that
// this is a NORMAL assistant message, indistinguishable from every other one
// except by its type, which is why no amount of pattern tightening helps.
const assistantTalkingAboutRateLimits = `{"type":"system","subtype":"init","session_id":"abc"}
{"type":"assistant","message":{"model":"claude-opus-5","id":"msg_011CeXZWBBE3ZuFok2FThuH1","type":"message","role":"assistant","content":[{"type":"text","text":"OR-184 says rate-limit handling assumes one run. With N concurrent sessions we would hit the rate limit faster and all N would see too many requests at once."}]}}
{"type":"result","subtype":"success","is_error":false}`

func TestAgentProseAboutRateLimitsIsNotExhaustion(t *testing.T) {
	v := Inspect(assistantTalkingAboutRateLimits, 1, time.Now())
	if v.Exhausted {
		t.Fatalf("the agent WRITING about rate limits was read as BEING rate limited; "+
			"a healthy run would pause and back off. kind=%q raw=%q", v.Kind, v.Raw)
	}
}

// A tool result is content the agent fetched, not Orion's runtime reporting a
// limit. An agent that reads a page mentioning 429 must not pause the run.
func TestToolResultMentioningLimitsIsNotExhaustion(t *testing.T) {
	out := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"HTTP/1.1 429 Too Many Requests\nretry-after: 30"}]}}`
	if v := Inspect(out, 1, time.Now()); v.Exhausted {
		t.Fatalf("a tool result quoting a 429 paused the run; kind=%q raw=%q", v.Kind, v.Raw)
	}
}

// The narrowing must not cost a real detection. A genuine error arrives on a
// line that is not an assistant or user message, and must still be seen.
func TestRealErrorsAreStillDetected(t *testing.T) {
	cases := map[string]string{
		"structured error object": `{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`,
		"result envelope":         `{"type":"result","subtype":"error","is_error":true,"result":"quota exceeded"}`,
		"bare stderr line":        `Error: 429 too many requests, retry-after: 30`,
		"malformed json":          `{"type":"error","error":{"message":"rate_limit_error"`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			if v := Inspect(out, 1, time.Now()); !v.Exhausted {
				t.Fatalf("a real limit went undetected, so the run burns its retries "+
					"against a wall instead of waiting: %s", out)
			}
		})
	}
}

// The dangerous mixture: the agent talking about limits WHILE a real one
// fires. The real error must win, and the reported raw text must be the
// provider's line rather than the agent's sentence -- otherwise the operator
// reads a quote from the model and concludes the detector is broken again.
func TestRealErrorWinsOverAgentProse(t *testing.T) {
	out := assistantTalkingAboutRateLimits + "\n" +
		`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded, retry-after: 45"}}`
	v := Inspect(out, 1, time.Now())
	if !v.Exhausted {
		t.Fatal("a real rate limit was missed because agent prose was in the same stream")
	}
	if strings.Contains(v.Raw, "OR-184 says") {
		t.Errorf("reported the AGENT's sentence as what the provider said: %q", v.Raw)
	}
}

// A reset time mentioned in the agent's prose is not the provider stating
// when the limit clears. Parsing it would produce a confident wait built on
// something the model made up.
func TestResetTimeIsNotReadFromAgentProse(t *testing.T) {
	now := time.Now()
	out := `{"type":"assistant","message":{"content":[{"type":"text","text":"the limit resets at 23:59 tonight"}]}}` + "\n" +
		`{"type":"error","error":{"message":"rate_limit_error"}}`
	v := Inspect(out, 1, now)
	if !v.Exhausted {
		t.Fatal("the real error was not detected")
	}
	if v.Parsed {
		t.Errorf("took a reset time from the agent's prose and presented it as stated fact: %v", v.ResetAt)
	}
}
