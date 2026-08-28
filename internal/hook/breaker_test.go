package hook

import (
	"encoding/json"
	"strings"
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
		Repeats:      map[string]int{},
		CmdFailures:  map[string]int{},
		FilesTouched: map[string]int{},
	}
	if mut != nil {
		mut(s)
	}
	return s
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
				s.Repeats[bashLs.Signature()] = 2
			}),
		},
		{
			name: "identical call at threshold blocks as loop",
			in:   bashLs,
			s: sess(func(s *state.Session) {
				s.Repeats[bashLs.Signature()] = 3
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
				s.Repeats[bashLs.Signature()] = 5
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
