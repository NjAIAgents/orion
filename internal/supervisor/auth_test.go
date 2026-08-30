package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The frame observed on 2026-08-30, verbatim, on all three of OR-168, OR-148
// and OR-149.
const expiredLoginResult = `{"type":"result","subtype":"error","is_error":true,` +
	`"terminal_reason":"api_error","duration_api_ms":0,"num_turns":1,` +
	`"total_cost_usd":0,"model":"<synthetic>","session_id":"s",` +
	`"result":"Anthropic profile login expired - Re-authenticate your Anthropic profile"}`

// The whole point of the ticket: the cause and the remedy were in the payload
// Orion already parses, and were replaced by the exit code.
func TestAnExpiredLoginIsNamedWithItsFix(t *testing.T) {
	msg, no := AuthFailure(expiredLoginResult)
	if !no {
		t.Fatalf("an expired login was not recognised: %q", msg)
	}
	for _, want := range []string{
		"claude is not authenticated",
		"login expired",
		"Run: claude, sign in",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "exited 1") {
		t.Errorf("the exit code is not the diagnosis:\n%s", msg)
	}
}

// The structural half of the test. A run that took turns and spent tokens did
// work; whatever it says on the way out, it is a work failure and must still
// report as one.
func TestAWorkFailureIsNotReportedAsALoginProblem(t *testing.T) {
	spent := `{"type":"result","is_error":true,"terminal_reason":"api_error",` +
		`"num_turns":37,"total_cost_usd":1.4,` +
		`"usage":{"input_tokens":900,"output_tokens":400,"cache_read_input_tokens":10},` +
		`"result":"the tests would not pass; please re-authenticate the staging client"}`
	if msg, no := AuthFailure(spent); no {
		t.Fatalf("a run with 37 turns and $1.40 spent was called a login problem: %s", msg)
	}
}

// A run that spent nothing and took no turn is still not a login problem
// unless the CLI SAID so. Both halves are required, or the next overloaded
// upstream is reported as an expired login and the operator signs in to fix
// something that was never broken.
func TestADifferentAPIErrorIsNotReportedAsALoginProblem(t *testing.T) {
	other := `{"type":"result","is_error":true,"terminal_reason":"api_error",` +
		`"num_turns":1,"total_cost_usd":0,"model":"<synthetic>",` +
		`"result":"Overloaded - the upstream service is at capacity"}`
	if msg, no := AuthFailure(other); no {
		t.Fatalf("an unrelated api_error was reported as a login problem: %s", msg)
	}
}

// The inverse of the channel argument in internal/quota: an agent working a
// ticket ABOUT authentication says these words constantly, and that is data,
// not a statement about Orion's own credentials.
func TestTheAgentTalkingAboutLoginsIsNotALoginProblem(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"text",` +
		`"text":"the login expired path should tell you to re-authenticate"}]}}` + "\n" +
		`{"type":"result","is_error":false,"terminal_reason":"success","num_turns":12,` +
		`"total_cost_usd":0.6,"usage":{"output_tokens":100},"result":"done"}`
	if msg, no := AuthFailure(stream); no {
		t.Fatalf("the agent's own prose paused the run: %s", msg)
	}
}

// End to end, against a CLI that behaves as the real one did: the result frame
// on stdout, exit 1. The run must carry the state and the sentence, not the
// exit code -- and must not retry, because waiting cannot produce a login.
func TestARunAgainstALoggedOutCLIReportsTheCauseAndDoesNotRetry(t *testing.T) {
	w := ws(t, "")

	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"echo '" + expiredLoginResult + "'\n" +
		"exit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := Run(w, Options{Stage: "intent", Prompt: "do a thing",
		MaxMinutes: 1, MaxTurns: 1})
	if err == nil || res == nil {
		t.Fatalf("a run that could not authenticate must report an error; res=%+v err=%v", res, err)
	}
	if !res.Unauthenticated {
		t.Errorf("the run was not recognised as unauthenticated: %+v", res)
	}
	if !strings.Contains(res.Reason, "claude is not authenticated") ||
		!strings.Contains(res.Reason, "Run: claude, sign in") {
		t.Errorf("reason names neither the cause nor the fix: %q", res.Reason)
	}
	if res.Attempts != 1 {
		t.Errorf("retried %d times; a missing login never clears by waiting", res.Attempts)
	}
}

// A successful run says nothing about authentication.
func TestACleanRunIsNotALoginProblem(t *testing.T) {
	clean := `{"type":"result","is_error":false,"num_turns":3,"total_cost_usd":0.2,` +
		`"usage":{"output_tokens":50},"result":"done"}`
	if _, no := AuthFailure(clean); no {
		t.Fatal("a clean run was reported as unauthenticated")
	}
	if _, no := AuthFailure(""); no {
		t.Fatal("no output at all was reported as unauthenticated")
	}
}

// Every wording the CLI is known to use for "no usable credential", each
// checked in isolation so a future removal of one pattern fails on the right
// line instead of hiding behind the others.
func TestEveryKnownLoginWordingIsRecognised(t *testing.T) {
	frame := func(result string) string {
		return `{"type":"result","is_error":true,"terminal_reason":"api_error",` +
			`"num_turns":1,"total_cost_usd":0,"result":"` + result + `"}`
	}
	for _, tc := range []struct {
		name   string
		result string
	}{
		{"not authenticated", "the client is not authenticated"},
		{"authentication_error", "received authentication_error from the API"},
		{"invalid API key", "invalid API key supplied"},
		{"invalid bearer token", "invalid bearer token"},
		{"re-authenticate, hyphenated", "please re-authenticate"},
		{"reauthenticate, no hyphen", "please reauthenticate now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, no := AuthFailure(frame(tc.result)); !no {
				t.Errorf("%q was not recognised as a login problem", tc.result)
			}
		})
	}
}

// A single turn is the refusal itself being reported; a run that took a
// second turn got somewhere, whatever the closing text says.
func TestTwoTurnsIsNotALoginProblemEvenWithLoginText(t *testing.T) {
	twoTurns := `{"type":"result","is_error":true,"terminal_reason":"api_error",` +
		`"num_turns":2,"total_cost_usd":0,"result":"Anthropic profile login expired - Re-authenticate"}`
	if msg, no := AuthFailure(twoTurns); no {
		t.Fatalf("a run with 2 turns was called a login problem: %s", msg)
	}
}

// Spend alone, independent of turns, is still work done. Isolated from
// TestAWorkFailureIsNotReportedAsALoginProblem, which also varies num_turns,
// so this proves the cost check on its own rather than the two together.
func TestNonZeroCostIsNotALoginProblemEvenWithLoginText(t *testing.T) {
	spent := `{"type":"result","is_error":true,"terminal_reason":"api_error",` +
		`"num_turns":1,"total_cost_usd":0.02,` +
		`"usage":{"input_tokens":10,"output_tokens":5},` +
		`"result":"Anthropic profile login expired - Re-authenticate your Anthropic profile"}`
	if msg, no := AuthFailure(spent); no {
		t.Fatalf("a run that spent $0.02 was called a login problem: %s", msg)
	}
}

// Zero turns and zero cost with no result line at all -- or one that will not
// parse -- is not a login problem either. Nothing said so.
func TestNoResultLineIsNotALoginProblem(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
	}{
		{"no result field at all", `{"type":"assistant","message":{"content":[]}}`},
		{"malformed json", `{"type":"result","is_error":true,"terminal_reason":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if msg, no := AuthFailure(tc.out); no {
				t.Errorf("%s: reported as a login problem: %s", tc.name, msg)
			}
		})
	}
}

// The cause carried into the message is the CLI's own sentence, verbatim --
// not a generic paraphrase -- so an operator reading two different failures
// can tell them apart.
func TestTheMessageCarriesTheCLIsCauseVerbatim(t *testing.T) {
	frame := `{"type":"result","is_error":true,"terminal_reason":"api_error",` +
		`"num_turns":1,"total_cost_usd":0,"result":"invalid bearer token supplied"}`
	msg, no := AuthFailure(frame)
	if !no {
		t.Fatalf("not recognised: %s", msg)
	}
	if !strings.Contains(msg, "invalid bearer token supplied") {
		t.Errorf("message does not carry the CLI's own cause verbatim: %q", msg)
	}
}
