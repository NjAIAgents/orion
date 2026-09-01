package supervisor

import (
	"fmt"
	"strings"
	"testing"
)

// resultFrame is the CLI's own closing line, the only channel any of this
// reads. Built rather than pasted so a test can vary one field and leave the
// rest at the shape a real run has.
func resultFrame(turns int, cost float64, window int, isErr bool, text string) string {
	return fmt.Sprintf(`{"type":"result","is_error":%t,"num_turns":%d,`+
		`"total_cost_usd":%g,"result":%q,`+
		`"usage":{"input_tokens":12,"output_tokens":83785,`+
		`"cache_read_input_tokens":24232907,"cache_creation_input_tokens":900},`+
		`"modelUsage":{"claude-opus-4":{"contextWindow":%d}}}`,
		isErr, turns, cost, text, window)
}

func TestAFilledContextWindowIsNamedAsItselfWithItsCost(t *testing.T) {
	out := resultFrame(121, 17.23, 200000, true, "")
	res := &Result{ExitCode: 1, Started: true, PeakContext: 191204, ContextWindow: 200000}

	msg, ok := FailureReason(out, res)
	if !ok {
		t.Fatal("a run that peaked at 96% of its window was not classified")
	}
	for _, want := range []string{
		"filled its context window", "191,204", "200,000", "121 turns", "$17.23", "smaller ticket",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the operator-facing line is missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "claude exited") {
		t.Errorf("the bare exit code survived the classification:\n%s", msg)
	}
}

// The measurement is the whole claim. A run that read 24M cached tokens and
// never came near the ceiling is a long healthy run that failed for some other
// reason, and saying otherwise would be a confident wrong sentence.
func TestALongRunThatNeverFilledTheWindowIsNotCalledExhausted(t *testing.T) {
	out := resultFrame(121, 17.23, 200000, true, "")
	res := &Result{ExitCode: 1, Started: true, PeakContext: 60000, ContextWindow: 200000}

	msg, ok := FailureReason(out, res)
	if !ok {
		t.Fatal("a failed run should still be reported, with its cost")
	}
	if strings.Contains(msg, "context window") {
		t.Errorf("cache reads alone were read as exhaustion:\n%s", msg)
	}
	if !strings.Contains(msg, "no cause reported") || !strings.Contains(msg, "$17.23") {
		t.Errorf("an unclassified failure must say so and name its cost:\n%s", msg)
	}
}

// The window is not always on the stream; the result JSON carries it too.
func TestTheWindowIsTakenFromTheResultJSONWhenTheStreamNeverSaid(t *testing.T) {
	out := resultFrame(40, 3.10, 200000, true, "")
	res := &Result{ExitCode: 1, Started: true, PeakContext: 190000, ContextWindow: 0}

	msg, ok := FailureReason(out, res)
	if !ok || !strings.Contains(msg, "filled its context window") {
		t.Fatalf("the result JSON's window was not used: ok=%v msg=%s", ok, msg)
	}
}

func TestALostConnectionIsDistinguishedFromACrash(t *testing.T) {
	out := resultFrame(9, 0.42, 200000, true, "Connection error: fetch failed")
	res := &Result{ExitCode: 1, Started: true, PeakContext: 30000, ContextWindow: 200000}

	msg, ok := FailureReason(out, res)
	if !ok {
		t.Fatal("a network failure was not classified")
	}
	if !strings.Contains(msg, "could not reach the API") {
		t.Errorf("the network cause was not named:\n%s", msg)
	}
	if !strings.Contains(msg, "Nothing is wrong with the ticket") {
		t.Errorf("the remedy was not named:\n%s", msg)
	}
	if !strings.Contains(msg, "$0.42") {
		t.Errorf("the cost was not reported:\n%s", msg)
	}
}

// Channel discipline, the rule auth.go and internal/quota both keep: an agent
// working a ticket about retries writes "connection reset" all day, and that
// is data about the ticket, not about Orion's runtime.
func TestTheAgentTalkingAboutConnectionsIsNotANetworkFault(t *testing.T) {
	out := `{"type":"assistant","message":{"content":[{"type":"text",` +
		`"text":"the retry path should handle a connection reset"}]}}` + "\n" +
		resultFrame(30, 2.00, 200000, true, "the tests failed")
	res := &Result{ExitCode: 1, Started: true, PeakContext: 30000, ContextWindow: 200000}

	msg, _ := FailureReason(out, res)
	if strings.Contains(msg, "could not reach the API") {
		t.Errorf("agent prose was read as a runtime fault:\n%s", msg)
	}
}

// The four cases the ticket asks an operator to be able to tell apart must
// actually produce four different sentences.
func TestTheFourFailureShapesReadDifferently(t *testing.T) {
	seen := map[string]string{}
	add := func(name, msg string) {
		for other, m := range seen {
			if m == msg {
				t.Errorf("%s and %s are indistinguishable:\n%s", name, other, msg)
			}
		}
		seen[name] = msg
	}

	exhausted, _ := FailureReason(
		resultFrame(121, 17.23, 200000, true, ""),
		&Result{ExitCode: 1, Started: true, PeakContext: 191204, ContextWindow: 200000})
	add("exhaustion", exhausted)

	network, _ := FailureReason(
		resultFrame(9, 0.42, 200000, true, "Connection error: fetch failed"),
		&Result{ExitCode: 1, Started: true, PeakContext: 30000, ContextWindow: 200000})
	add("network", network)

	crash, _ := FailureReason(
		resultFrame(30, 2.00, 200000, true, "the tests failed"),
		&Result{ExitCode: 1, Started: true, PeakContext: 30000, ContextWindow: 200000})
	add("crash", crash)

	// The login case is auth.go's, and is checked here only for the property
	// this file must not break: it stays out of the way.
	loggedOut := `{"type":"result","is_error":true,"terminal_reason":"api_error",` +
		`"num_turns":1,"total_cost_usd":0,` +
		`"result":"Anthropic profile login expired - Re-authenticate your Anthropic profile"}`
	if _, taken := FailureReason(loggedOut, &Result{ExitCode: 1, Started: true}); taken {
		t.Error("an expired login was claimed by the generic classifier")
	}
	if msg, ok := AuthFailure(loggedOut); !ok {
		t.Error("the login case stopped being recognised")
	} else {
		add("auth", msg)
	}
}

func TestACleanExitIsNotClassified(t *testing.T) {
	if _, ok := FailureReason(resultFrame(4, 0.10, 200000, false, ""),
		&Result{ExitCode: 0, Started: true, PeakContext: 199000, ContextWindow: 200000}); ok {
		t.Error("a successful run was given a failure reason")
	}
}

// A timeout and a run that never started both already say something truer
// than anything derivable from usage, so neither is overwritten.
func TestTheReadingsThatAlreadyKnowMoreAreNotOverwritten(t *testing.T) {
	out := resultFrame(121, 17.23, 200000, true, "")
	for _, tc := range []struct {
		name string
		res  *Result
	}{
		{"killed on the wall clock", &Result{ExitCode: 124, Started: true, Killed: true,
			PeakContext: 191204, ContextWindow: 200000}},
		{"never started", &Result{ExitCode: 1, Started: false,
			PeakContext: 191204, ContextWindow: 200000}},
		{"already named as a login", &Result{ExitCode: 1, Started: true, Unauthenticated: true,
			PeakContext: 191204, ContextWindow: 200000}},
	} {
		if _, ok := FailureReason(out, tc.res); ok {
			t.Errorf("%s was reclassified", tc.name)
		}
	}
}

func TestCommasGroupTokenCounts(t *testing.T) {
	for in, want := range map[int]string{
		0: "0", 999: "999", 1000: "1,000", 24232907: "24,232,907", 191204: "191,204",
	} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}
