package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/state"
)

// What a tripped agent is allowed to do AFTER it stops (OR-194).
//
// The trip must stop the LOOP without also stopping the agent from leaving
// the worktree in a state somebody can pick up. On OR-192 the policy half
// worked exactly as designed -- the agent stopped rather than working around
// the breaker -- and then could not revert the risky test it had just
// written, because Edit and Bash were both refused by then. The next reader
// would have found a modified test file and no explanation.

func TestATrippedSessionMayStillTidyTheWorktree(t *testing.T) {
	store := state.New(t.TempDir())
	cfg := testCfg()
	cfg.Root = t.TempDir()

	if _, err := store.Update("s1", func(s *state.Session) {
		s.Tripped, s.TrippedDetail = "breaker/loop", "Bash repeated 4 times"
	}); err != nil {
		t.Fatal(err)
	}
	pre := func(tool, jsonInput string) Decision {
		return Breaker(Input{
			HookEventName: "PreToolUse", SessionID: "s1",
			ToolName: tool, ToolInput: json.RawMessage(jsonInput),
		}, cfg, store)
	}
	bash := func(cmd string) Decision {
		b, err := json.Marshal(map[string]string{"command": cmd})
		if err != nil {
			t.Fatal(err)
		}
		return pre("Bash", string(b))
	}

	// Allowed: these can only leave the tree reportable.
	for _, cmd := range []string{
		"git status --porcelain",
		"git diff",
		"git checkout -- internal/work/flaky_test.go",
		"git restore internal/work/flaky_test.go",
	} {
		if d := bash(cmd); d.Blocked() {
			t.Errorf("%q must be allowed after a trip; refusing it abandons a modified file", cmd)
		}
	}

	// Refused: another attempt at the task, and every route to one. The
	// allowance is a list of FORMS precisely because "cleanup" is an intent
	// and an intent is not observable.
	for _, cmd := range []string{
		"go test ./...",
		"git push origin HEAD",
		"git checkout main",
		"git status; rm -rf internal",
		"git status && curl http://x",
		"git status | sh",
		"git diff > /tmp/x",
		"git add $(rm -rf .)",
	} {
		if d := bash(cmd); !d.Blocked() {
			t.Errorf("%q must stay blocked; the allowance is not a general reprieve", cmd)
		}
	}
	// git commit --amend is a distinct form from git commit: it rewrites the
	// tip commit instead of adding a new one, so it can discard or overwrite
	// content the run already committed. The allowance's own stated invariant
	// (breaker.go: "COMMIT whatever compiles"; residue.go: "what it committed
	// survives untouched") is about ADDING a commit, not rewriting the last
	// one, and "git commit" as a bare prefix does not distinguish the two.
	for _, cmd := range []string{
		"git commit --amend -m rewritten",
		"git commit --amend --no-edit",
	} {
		if d := bash(cmd); !d.Blocked() {
			t.Errorf("%q must stay blocked; --amend can overwrite a commit the run already made, "+
				"not just add a new one", cmd)
		}
	}
	if d := pre("Edit", `{"file_path":"internal/work/work.go"}`); !d.Blocked() {
		t.Error("a code edit must stay blocked; nothing distinguishes a cleanup edit from another attempt")
	}

	// The block message has to NAME the allowance. An agent that reads "stop"
	// as "stop acting" leaves the file modified exactly as before.
	msg := bash("go build ./...").Msg
	for _, want := range []string{"git checkout -- <path>", "git commit", "does not clear the trip"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the block message must mention %q:\n%s", want, msg)
		}
	}
}

// The allowance is an exit, not a way back in: bounded, and spending it
// never re-arms the session.
func TestTheCleanupAllowanceIsBoundedAndDoesNotClearTheTrip(t *testing.T) {
	store := state.New(t.TempDir())
	cfg := testCfg()
	cfg.Root = t.TempDir()

	if _, err := store.Update("s1", func(s *state.Session) {
		s.Tripped, s.TrippedDetail = "breaker/tool-budget", "10 tool calls"
		s.ToolCalls = 99 // already far over: the allowance must survive that
	}); err != nil {
		t.Fatal(err)
	}
	cleanup := func(event string) Decision {
		return Breaker(Input{
			HookEventName: event, SessionID: "s1", ToolName: "Bash",
			ToolInput: json.RawMessage(`{"command":"git status"}`),
		}, cfg, store)
	}

	for i := 1; i <= cleanupAllowance; i++ {
		if d := cleanup("PreToolUse"); d.Blocked() {
			t.Fatalf("cleanup call %d of %d was refused: %s", i, cleanupAllowance, d.Msg)
		}
		// The PostToolUse that follows every call must not re-judge it: the
		// counters that tripped are already over their limits, so a verdict
		// there would refuse the allowance breakerPre had just granted.
		if d := cleanup("PostToolUse"); d.Blocked() {
			t.Fatalf("cleanup call %d was allowed and then blocked on the way out", i)
		}
	}
	if d := cleanup("PreToolUse"); !d.Blocked() {
		t.Fatal("the allowance must be bounded; an unbounded one is the tool budget handed back")
	}

	s := store.Read("s1")
	if s.Tripped != "breaker/tool-budget" {
		t.Fatalf("tripped = %q; using the allowance must not clear the trip", s.Tripped)
	}
	if d := Breaker(Input{
		HookEventName: "PreToolUse", SessionID: "s1", ToolName: "Edit",
		ToolInput: json.RawMessage(`{"file_path":"a.go"}`),
	}, cfg, store); !d.Blocked() {
		t.Fatal("normal work must still be refused after the allowance was used")
	}
}

// The stop-note must be written BY THE BREAKER, as part of tripping.
//
// On OR-192 plans/BLOCKED.md existed only because the agent happened to
// write it before the breaker closed. One tool call later there would have
// been no note either, so the note must not depend on the agent getting
// another turn -- including when the trip lands on the call immediately
// after the last edit.
func TestTheBreakerWritesTheStopNoteAsPartOfTripping(t *testing.T) {
	root := t.TempDir()
	cfg := testCfg()
	cfg.Root = root
	store := state.New(t.TempDir())

	post := func(tool, jsonInput string) Decision {
		return Breaker(Input{
			HookEventName: "PostToolUse", SessionID: "s1",
			ToolName: tool, ToolInput: json.RawMessage(jsonInput),
		}, cfg, store)
	}

	// The edit lands first, as it did on the run this comes from...
	post("Edit", `{"file_path":"internal/work/flaky_test.go"}`)
	// ...and the trip arrives on the calls immediately after it. The agent
	// never gets a turn in which to write anything.
	var d Decision
	for i := 0; i < cfg.Limits.MaxRepeatIdentical; i++ {
		d = post("Bash", `{"command":"kill -USR1 4242"}`)
	}
	if !d.Blocked() {
		t.Fatalf("the repeated call should have tripped the loop breaker: %s", d.Msg)
	}

	b, err := os.ReadFile(filepath.Join(root, "plans", "BLOCKED.md"))
	if err != nil {
		t.Fatalf("no stop-note was written at the moment of the trip: %v", err)
	}
	note := string(b)
	for _, want := range []string{"breaker/loop", "s1", "orion reset --session"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note must contain %q:\n%s", want, note)
		}
	}
}

// A note already on the branch is somebody's account of a different
// blockage. Reporting this trip must not destroy it.
func TestTheStopNoteIsAppendedNotClobbered(t *testing.T) {
	root := t.TempDir()
	cfg := testCfg()
	cfg.Root = root
	store := state.New(t.TempDir())

	if err := os.MkdirAll(filepath.Join(root, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	prior := "# BLOCKED\n\nthe schema migration needs a decision from the architect\n"
	if err := os.WriteFile(filepath.Join(root, "plans", "BLOCKED.md"), []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < cfg.Limits.MaxRepeatIdentical; i++ {
		Breaker(Input{
			HookEventName: "PostToolUse", SessionID: "s2", ToolName: "Bash",
			ToolInput: json.RawMessage(`{"command":"kill -USR1 4242"}`),
		}, cfg, store)
	}

	b, err := os.ReadFile(filepath.Join(root, "plans", "BLOCKED.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "needs a decision from the architect") {
		t.Error("the breaker clobbered a note that was already there")
	}
	if !strings.Contains(string(b), "breaker/loop") {
		t.Error("the trip was not recorded")
	}
}

// The breaker must never write outside a resolved project root. A hook that
// cannot find one already declares guardrails inactive; dropping a
// BLOCKED.md into whatever directory it happened to start in is worse than
// writing nothing.
func TestNoStopNoteWithoutAProjectRoot(t *testing.T) {
	cfg := testCfg()
	cfg.Root = "" // what config.Load leaves when there is no root
	store := state.New(t.TempDir())

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cfg.Limits.MaxRepeatIdentical; i++ {
		Breaker(Input{
			HookEventName: "PostToolUse", SessionID: "s3", ToolName: "Bash",
			ToolInput: json.RawMessage(`{"command":"kill -USR1 4242"}`),
		}, cfg, store)
	}
	if _, err := os.Stat(filepath.Join(wd, "plans", "BLOCKED.md")); err == nil {
		t.Fatal("a rootless trip wrote a note into the working directory")
	}
	if store.Read("s3").Tripped == "" {
		t.Fatal("the trip itself must still be recorded")
	}
}
