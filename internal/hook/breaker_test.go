package hook

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/state"
)

// testCfg is a config with small, easily-reasoned-about limits so a test
// failure points at logic rather than arithmetic.
func testCfg() config.Config {
	c := config.Defaults()
	c.Limits.MaxToolCalls = 10
	c.Limits.MaxRepeatIdentical = 3
	c.Limits.MaxConsecutiveFailures = 2
	c.Limits.MaxSameCommandFailures = 2
	c.Limits.MaxEditsWithoutVerify = 4
	c.Limits.MaxFilesTouched = 5
	c.Paths.Plans = "plans"
	return c
}

func sess(mut func(*state.Session)) *state.Session {
	s := &state.Session{
		Repeats:      map[string]map[string]int{},
		CmdFailures:  map[string]int{},
		FilesTouched: map[string]int{},
	}
	if mut != nil {
		mut(s)
	}
	return s
}

// setRepeat records a repeat count for the actor derived from in, matching
// how breakerPost would have populated it -- so tests exercise the same
// keying verdict() reads.
func setRepeat(s *state.Session, in Input, n int) {
	if s.Repeats[actorKey(in)] == nil {
		s.Repeats[actorKey(in)] = map[string]int{}
	}
	s.Repeats[actorKey(in)][in.Signature()] = n
}

func input(tool, jsonInput string) Input {
	return Input{ToolName: tool, ToolInput: json.RawMessage(jsonInput)}
}

func TestVerdict(t *testing.T) {
	cfg := testCfg()
	bashLs := input("Bash", `{"command":"ls -la"}`)

	tests := []struct {
		name      string
		in        Input
		s         *state.Session
		wantBlock bool
		wantKind  string
		// wantIn asserts the block message actually tells the model what to
		// do. A block that only says "no" gets worked around.
		wantIn string
	}{
		{
			name: "clean session allows",
			in:   bashLs,
			s:    sess(nil),
		},
		{
			name: "identical call under threshold allows",
			in:   bashLs,
			s: sess(func(s *state.Session) {
				setRepeat(s, bashLs, 2)
			}),
		},
		{
			name: "identical call at threshold blocks as loop",
			in:   bashLs,
			s: sess(func(s *state.Session) {
				setRepeat(s, bashLs, 3)
			}),
			wantBlock: true,
			wantKind:  "breaker/loop",
			wantIn:    "Do not retry",
		},
		{
			name: "same command failing repeatedly blocks",
			in:   bashLs,
			s: sess(func(s *state.Session) {
				s.CmdFailures["ls -la"] = 2
			}),
			wantBlock: true,
			wantKind:  "breaker/command-failures",
			wantIn:    "not going to start working",
		},
		{
			name: "consecutive failures block",
			in:   bashLs,
			s: sess(func(s *state.Session) {
				s.ConsecFailures = 2
			}),
			wantBlock: true,
			wantKind:  "breaker/consecutive-failures",
		},
		{
			name: "tool budget exhausted blocks",
			in:   bashLs,
			s: sess(func(s *state.Session) {
				s.ToolCalls = 10
			}),
			wantBlock: true,
			wantKind:  "breaker/tool-budget",
			wantIn:    "BLOCKED.md",
		},
		{
			name: "too many edits without verification blocks",
			in:   input("Edit", `{"file_path":"a.go"}`),
			s: sess(func(s *state.Session) {
				s.EditsSinceCheck = 4
			}),
			wantBlock: true,
			wantKind:  "breaker/unverified-edits",
		},
		{
			name: "blast radius blocks",
			in:   input("Edit", `{"file_path":"a.go"}`),
			s: sess(func(s *state.Session) {
				for _, f := range []string{"a", "b", "c", "d", "e"} {
					s.FilesTouched[f] = 1
				}
			}),
			wantBlock: true,
			wantKind:  "breaker/blast-radius",
		},
		{
			// Loop detection must win over the tool budget: "you repeated the
			// same call" is actionable, "you used 400 calls" is not.
			name: "loop reported before budget when both tripped",
			in:   bashLs,
			s: sess(func(s *state.Session) {
				setRepeat(s, bashLs, 5)
				s.ToolCalls = 99
			}),
			wantBlock: true,
			wantKind:  "breaker/loop",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := verdict(tc.in, cfg, tc.s)
			if got.Blocked() != tc.wantBlock {
				t.Fatalf("blocked = %v, want %v (msg: %s)", got.Blocked(), tc.wantBlock, got.Msg)
			}
			if tc.wantKind != "" && got.trippedKind != tc.wantKind {
				t.Errorf("kind = %q, want %q", got.trippedKind, tc.wantKind)
			}
			if tc.wantIn != "" && !strings.Contains(got.Msg, tc.wantIn) {
				t.Errorf("message missing %q, got:\n%s", tc.wantIn, got.Msg)
			}
			if tc.wantBlock && got.trippedDetail == "" {
				t.Error("a block must record a detail for the resume message")
			}
		})
	}
}

func TestSignatureStability(t *testing.T) {
	a := input("Bash", `{"command":"go test ./..."}`)
	b := input("Bash", `{"command":"go test ./..."}`)
	if a.Signature() != b.Signature() {
		t.Error("identical inputs must share a signature or loop detection never fires")
	}

	// Whitespace-only reformatting is the commonest way a retry looks
	// different while being the same call.
	c := input("Bash", `{"command":"go test ./..."}`)
	d := input("Bash", "{\"command\":\"go test ./...\"}\n  ")
	if c.Signature() != d.Signature() {
		t.Error("whitespace differences must not defeat loop detection")
	}

	e := input("Bash", `{"command":"go build ./..."}`)
	if a.Signature() == e.Signature() {
		t.Error("different commands must not collide")
	}
	f := input("Read", `{"command":"go test ./..."}`)
	if a.Signature() == f.Signature() {
		t.Error("tool name must be part of the signature")
	}
}

func TestFailedDetection(t *testing.T) {
	tests := []struct {
		name string
		resp string
		want bool
	}{
		{"absent response is not a failure", ``, false},
		{"explicit success false", `{"success":false}`, true},
		{"explicit success true", `{"success":true}`, false},
		{"is_error true", `{"is_error":true}`, true},
		{"non-zero exit", `{"exit_code":1}`, true},
		{"zero exit", `{"exit_code":0}`, false},
		{"error string present", `{"error":"boom"}`, true},
		// Stderr alone is not failure: plenty of tools write progress there.
		{"stderr alone is not failure", `{"stderr":"warning: deprecated"}`, false},
		// A shape we do not recognize must not be counted against the
		// failure budget, or an unfamiliar tool trips the breaker for free.
		{"unrecognized shape is not a failure", `"just a string"`, false},
		{"empty object is not a failure", `{}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{ToolResponse: json.RawMessage(tc.resp)}
			if got := in.Failed(); got != tc.want {
				t.Errorf("Failed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLooksLikeVerification(t *testing.T) {
	yes := []string{
		"make test", "npm test", "go test ./...", "pytest -q",
		"cargo test --all", "./gradlew test", "make build && make lint",
	}
	for _, c := range yes {
		if !looksLikeVerification(c) {
			t.Errorf("%q should count as verification", c)
		}
	}
	// The edit budget resets only on real verification. If `ls` counted,
	// the control would be trivially bypassable.
	no := []string{"ls", "cat file.go", "git status", "echo test", "cd tests", ""}
	for _, c := range no {
		if looksLikeVerification(c) {
			t.Errorf("%q must not count as verification", c)
		}
	}
}

func TestBreakerPreBlocksAfterTrip(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg()

	_, err := store.Update("s1", func(s *state.Session) {
		s.Tripped = "breaker/loop"
		s.TrippedDetail = "Bash repeated 4 times"
	})
	if err != nil {
		t.Fatal(err)
	}

	d := Breaker(Input{HookEventName: "PreToolUse", SessionID: "s1"}, cfg, store)
	if !d.Blocked() {
		t.Fatal("a tripped session must refuse the next call; otherwise the breaker reports but never stops")
	}
	if !strings.Contains(d.Msg, "orion reset --session") {
		t.Error("block message must tell the human how to resume")
	}
}

// The block message must describe the recovery THIS trip actually has.
//
// It used to print "if the trip is unverified-edits, running the tests or
// build is still allowed" on every kind. Two agents in a row read that on a
// LOOP trip as "Bash is open", tried Bash, were refused, and reported the
// breaker as contradicting itself (OR-143, OR-156). Conditionally true and
// reliably misread is, for a message whose only job is to be obeyed, the
// same as wrong.
func TestTheBlockMessageOnlyOffersRecoveryThatExists(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg()

	msgFor := func(kind string) string {
		if _, err := store.Update(kind, func(s *state.Session) {
			s.Tripped, s.TrippedDetail = kind, "test"
		}); err != nil {
			t.Fatal(err)
		}
		return Breaker(Input{
			HookEventName: "PreToolUse", SessionID: kind,
			ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"x.go"}`),
		}, cfg, store).Msg
	}

	loop := msgFor("breaker/loop")
	if strings.Contains(loop, "tests or the build IS still allowed") {
		t.Errorf("a loop trip has no verify escape hatch; the message must not imply one:\n%s", loop)
	}
	if !strings.Contains(loop, "no self-service recovery") {
		t.Errorf("a sealed trip must say so plainly:\n%s", loop)
	}

	unverified := msgFor("breaker/unverified-edits")
	if !strings.Contains(unverified, "tests or the build IS still allowed") {
		t.Errorf("unverified-edits DOES have a way out and must say so:\n%s", unverified)
	}

	// Both still point at the stop-note and the human reset, whatever the kind.
	for kind, msg := range map[string]string{"loop": loop, "unverified-edits": unverified} {
		if !strings.Contains(msg, "BLOCKED.md") {
			t.Errorf("%s: every trip must name the stop-note", kind)
		}
		if !strings.Contains(msg, "orion reset --session") {
			t.Errorf("%s: every trip must tell the human how to resume", kind)
		}
	}
}

// The deadlock found on OR-117's runs: an unverified-edits trip blocked the
// go build that would clear it AND the BLOCKED.md write the trip message
// demands. Both recovery paths must stay open; everything else stays sealed.
func TestTrippedSessionStillPermitsItsOwnRecovery(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg()

	tripAs := func(kind string) {
		_, err := store.Update("s1", func(s *state.Session) {
			s.Tripped = kind
			s.TrippedDetail = "test"
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	pre := func(tool, jsonInput string) Decision {
		return Breaker(Input{
			HookEventName: "PreToolUse", SessionID: "s1",
			ToolName: tool, ToolInput: json.RawMessage(jsonInput),
		}, cfg, store)
	}

	tripAs("breaker/unverified-edits")

	if d := pre("Bash", `{"command":"go build ./..."}`); d.Blocked() {
		t.Error("the verify that clears an unverified-edits trip must be allowed through")
	}
	if d := pre("Write", `{"file_path":"plans/BLOCKED.md","content":"x"}`); d.Blocked() {
		t.Error("the stop-note the trip message demands must be writable")
	}
	if d := pre("Edit", `{"file_path":"internal/work/work.go"}`); !d.Blocked() {
		t.Error("a code edit must stay blocked while tripped")
	}
	if d := pre("Bash", `{"command":"git push origin main"}`); !d.Blocked() {
		t.Error("a non-verify command must stay blocked while tripped")
	}

	// Other trip kinds have no self-service recovery: verify stays blocked.
	tripAs("breaker/loop")
	if d := pre("Bash", `{"command":"go test ./..."}`); !d.Blocked() {
		t.Error("a loop trip is not cleared by verifying; only the stop-note is allowed")
	}
	if d := pre("Write", `{"file_path":"plans/BLOCKED.md","content":"x"}`); d.Blocked() {
		t.Error("the stop-note must be writable whatever the trip kind")
	}
}

// A passing verify does not merely reset the counter; it clears the trip the
// counter caused, so the session that was let through to verify can go on.
func TestPassingVerifyClearsUnverifiedEditsTrip(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg()

	if _, err := store.Update("s1", func(s *state.Session) {
		s.Tripped = "breaker/unverified-edits"
		s.EditsSinceCheck = 9
	}); err != nil {
		t.Fatal(err)
	}

	Breaker(Input{
		HookEventName: "PostToolUse", SessionID: "s1",
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"go build ./..."}`),
	}, cfg, store)

	s := store.Read("s1")
	if s.Tripped != "" {
		t.Fatalf("passing verify should clear the trip, still tripped as %q", s.Tripped)
	}
	if s.EditsSinceCheck != 0 {
		t.Fatalf("edit counter should reset, got %d", s.EditsSinceCheck)
	}

	// A FAILING verify clears nothing: the point was a passing check.
	if _, err := store.Update("s2", func(s *state.Session) {
		s.Tripped = "breaker/unverified-edits"
	}); err != nil {
		t.Fatal(err)
	}
	Breaker(Input{
		HookEventName: "PostToolUse", SessionID: "s2",
		ToolName:     "Bash",
		ToolInput:    json.RawMessage(`{"command":"go build ./..."}`),
		ToolResponse: json.RawMessage(`{"stdout":"","stderr":"compile error","interrupted":false,"isImage":false,"is_error":true}`),
	}, cfg, store)
	if store.Read("s2").Tripped == "" {
		t.Fatal("a FAILING verify must not clear the trip")
	}
}

// OR-124: the loop breaker and the unverified-edits breaker used to fight.
// Re-running a PASSING verify command is the normal edit-test cycle -- and
// exactly what unverified-edits demands after every edit -- so it must never
// trip the identical-repeat loop counter. A FAILING verify still counts, and
// a non-verify identical call counts exactly as before.
func TestPassingVerifyIsExemptFromLoopCounter(t *testing.T) {
	cfg := testCfg()               // MaxRepeatIdentical = 3
	cfg.Limits.MaxToolCalls = 1000 // isolate the loop check from the tool budget

	run := func(cmdJSON, response string, times int) Decision {
		store := state.New(t.TempDir())
		var d Decision
		for i := 0; i < times; i++ {
			d = Breaker(Input{
				HookEventName: "PostToolUse", SessionID: "s",
				ToolName:     "Bash",
				ToolInput:    json.RawMessage(cmdJSON),
				ToolResponse: json.RawMessage(response),
			}, cfg, store)
		}
		return d
	}

	pass := `{"stdout":"ok","is_error":false}`
	fail := `{"stdout":"","stderr":"FAIL","is_error":true}`
	verify := `{"command":"go test ./internal/work/"}`
	other := `{"command":"ls -la"}`

	if d := run(verify, pass, 10); d.Blocked() {
		t.Errorf("a passing verify must never trip the loop breaker, got: %s", d.Msg)
	}
	if d := run(verify, fail, 3); !d.Blocked() {
		t.Error("repeating a FAILING verify with nothing changed is a real loop and must trip")
	}
	if d := run(other, pass, 3); !d.Blocked() {
		t.Error("a non-verify identical call must trip exactly as before")
	}
	if d := run(other, pass, 2); d.Blocked() {
		t.Errorf("a non-verify call under the threshold must not trip, got: %s", d.Msg)
	}
}

// OR-170: a subagent shares its parent's SessionID but gets its own
// transcript file. Two agents that each, independently, read the same file
// twice used to sum to one shared counter and trip the loop breaker at a
// call neither of them repeated -- a false trip from parallel fan-out, not a
// real loop. Regression for the incident behind OR-143 and OR-156.
func TestParallelSubagentsDoNotShareTheLoopCounter(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg() // MaxRepeatIdentical = 3

	readSame := json.RawMessage(`{"file_path":"CONVENTIONS.md"}`)
	call := func(transcript string) Decision {
		return Breaker(Input{
			HookEventName:  "PostToolUse",
			SessionID:      "parent-session",
			TranscriptPath: transcript,
			ToolName:       "Read",
			ToolInput:      readSame,
		}, cfg, store)
	}

	// Parent and two subagents, all under the same SessionID, each read the
	// identical file twice -- six calls total, none of them a real loop.
	var last Decision
	for _, transcript := range []string{
		"main.jsonl", "subagents/agent-a.jsonl", "subagents/agent-b.jsonl",
	} {
		for i := 0; i < 2; i++ {
			last = call(transcript)
			if last.Blocked() {
				t.Fatalf("call %d for %s falsely tripped the loop breaker: %s", i+1, transcript, last.Msg)
			}
		}
	}

	// The same subagent repeating its OWN identical call a third time is a
	// real loop and must still trip -- the fix must not have gone the other
	// way and stopped detecting loops altogether.
	call("subagents/agent-a.jsonl")
	if d := call("subagents/agent-a.jsonl"); !d.Blocked() {
		t.Fatal("a single actor repeating the identical call past the threshold must still trip as a loop")
	}
}

// A payload with no transcript_path (an older harness, or a malformed
// input) must fall back to the old session-wide behavior rather than
// silently disabling loop detection.
func TestLoopCounterFallsBackToSessionWhenTranscriptPathIsEmpty(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg() // MaxRepeatIdentical = 3

	call := func() Decision {
		return Breaker(Input{
			HookEventName: "PostToolUse",
			SessionID:     "s1",
			ToolName:      "Read",
			ToolInput:     json.RawMessage(`{"file_path":"a.go"}`),
		}, cfg, store)
	}

	call()
	call()
	if d := call(); !d.Blocked() {
		t.Fatal("without a transcript_path, repeats must still be counted against the session")
	}
}

// OR-170's fix is scoped to the identical-repeat counter only. The commit
// message is explicit that every other counter -- tool budget, consecutive
// failures, same-command failures -- stays aggregated across the whole
// session. If ToolCalls were accidentally scoped per actor too, a session
// running N parallel subagents could burn N times its real tool budget
// before the ceiling ever fired.
func TestToolBudgetStaysAggregatedAcrossActors(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg() // MaxToolCalls = 10, MaxRepeatIdentical = 3

	call := func(transcript, filePath string) Decision {
		return Breaker(Input{
			HookEventName:  "PostToolUse",
			SessionID:      "parent-session",
			TranscriptPath: transcript,
			ToolName:       "Read",
			ToolInput:      json.RawMessage(`{"file_path":"` + filePath + `"}`),
		}, cfg, store)
	}

	// Two distinct actors, each reading a distinct file every time so the
	// identical-repeat counter never fires -- isolating the budget check.
	var last Decision
	for i := 0; i < 5; i++ {
		last = call("main.jsonl", fmt.Sprintf("main-%d.go", i))
		if last.Blocked() {
			t.Fatalf("main actor call %d blocked before the shared budget was exhausted: %s", i, last.Msg)
		}
		last = call("subagents/agent-a.jsonl", fmt.Sprintf("sub-%d.go", i))
		if i < 4 && last.Blocked() {
			t.Fatalf("subagent call %d blocked before the shared budget was exhausted: %s", i, last.Msg)
		}
	}
	// 10 total calls across both actors must exhaust MaxToolCalls=10.
	if !last.Blocked() {
		t.Fatal("tool budget must still be aggregated across actors in one session, not reset per actor")
	}
}

// The Tripped flag itself is session-wide, not per-actor (unlike Repeats):
// once ANY actor trips the breaker, breakerPre must refuse the NEXT call
// from every actor sharing that session, including one that never
// repeated anything itself. A trip is a session-ending event, and scoping
// it per-actor would let a tripped session's other agents keep working
// unsupervised.
func TestATripFromOneActorBlocksEveryActorInTheSession(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg() // MaxRepeatIdentical = 3

	readSame := json.RawMessage(`{"file_path":"CONVENTIONS.md"}`)
	post := func(transcript string) Decision {
		return Breaker(Input{
			HookEventName:  "PostToolUse",
			SessionID:      "shared-session",
			TranscriptPath: transcript,
			ToolName:       "Read",
			ToolInput:      readSame,
		}, cfg, store)
	}

	// agent-a loops and trips the breaker.
	post("subagents/agent-a.jsonl")
	post("subagents/agent-a.jsonl")
	if d := post("subagents/agent-a.jsonl"); !d.Blocked() {
		t.Fatal("agent-a should have tripped the loop breaker")
	}

	// agent-b, which never repeated a call, must still be refused its next
	// PreToolUse in the same tripped session.
	d := breakerPre(Input{
		HookEventName:  "PreToolUse",
		SessionID:      "shared-session",
		TranscriptPath: "subagents/agent-b.jsonl",
		ToolName:       "Bash",
		ToolInput:      json.RawMessage(`{"command":"ls"}`),
	}, cfg, store)
	if !d.Blocked() {
		t.Fatal("a trip from one actor must seal the whole session, including actors that never looped")
	}
}

// Real subagents run as concurrent goroutines/processes sharing one
// session file. Regression-guard that the per-actor Repeats counters stay
// correctly isolated under actual concurrency, not just the sequential
// calls the other OR-170 tests make -- exercising the same lock path a
// real parallel fan-out would hit.
func TestConcurrentActorsKeepIsolatedRepeatCounters(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	cfg := testCfg() // MaxRepeatIdentical = 3

	readSame := json.RawMessage(`{"file_path":"CONVENTIONS.md"}`)
	actors := []string{"subagents/agent-a.jsonl", "subagents/agent-b.jsonl", "subagents/agent-c.jsonl"}

	var wg sync.WaitGroup
	for _, transcript := range actors {
		wg.Add(1)
		go func(transcript string) {
			defer wg.Done()
			// Each actor makes 2 identical calls concurrently with the
			// others -- under the threshold of 3, so none should trip.
			for i := 0; i < 2; i++ {
				Breaker(Input{
					HookEventName:  "PostToolUse",
					SessionID:      "concurrent-session",
					TranscriptPath: transcript,
					ToolName:       "Read",
					ToolInput:      readSame,
				}, cfg, store)
			}
		}(transcript)
	}
	wg.Wait()

	sig := input("Read", string(readSame)).Signature()
	sessSnapshot := store.Read("concurrent-session")
	for _, transcript := range actors {
		if got := sessSnapshot.Repeats[transcript][sig]; got > 3 {
			// Not asserting an exact count (the lock is best-effort under
			// contention, per TestConcurrentUpdatesDoNotLoseState), only that
			// one actor's concurrent calls did not inflate ANOTHER actor's
			// counter past what it could have earned itself.
			t.Errorf("actor %s counter %d looks contaminated by another actor's calls", transcript, got)
		}
	}

	// A 4th, sequential call from a brand-new actor must still be judged
	// purely on its own count, unaffected by the concurrent burst above.
	d := Breaker(Input{
		HookEventName:  "PostToolUse",
		SessionID:      "concurrent-session",
		TranscriptPath: "subagents/agent-d.jsonl",
		ToolName:       "Read",
		ToolInput:      readSame,
	}, cfg, store)
	if d.Blocked() {
		t.Fatal("a fresh actor's first call must not be blocked by another actor's concurrent repeats")
	}
}
