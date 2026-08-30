//go:build !windows

package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// killGroupPID is test-cleanup only: if a test's grandchild survives (the
// exact defect being probed for), this stops it from leaking into the rest
// of the test run rather than sleeping for its full 300s.
func killGroupPID(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// fakeClaudeTree puts a `claude` on PATH whose script is supplied verbatim,
// so tests can shape exactly what the "agent" does (block forever, spawn a
// background grandchild, exit clean) rather than only recording that it ran.
func fakeClaudeTree(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRunKillsGrandchildrenOnWallClockTimeout is OR-141's acceptance
// criterion run through the real entry point (Run), not just the terminate()
// helper: a fake claude that spawns a background grandchild (standing in for
// `go test`, `npm`, a dev server) and then hangs past MaxMinutes must leave
// no grandchild behind once Run returns.
func TestRunKillsGrandchildrenOnWallClockTimeout(t *testing.T) {
	// A real minute plus a real eight-second grace proved nothing this
	// sub-second budget does not: same ctx.Done() branch, same terminate(),
	// same group sweep (OR-202).
	shrinkWallClock(t, 500*time.Millisecond, 200*time.Millisecond)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	fakeClaudeTree(t, "sleep 300 & echo $! > "+pidFile+"\nwait\n")

	w := ws(t, "")
	start := time.Now()
	// A timeout is reported as a failed stage (err != nil) carrying the
	// killed Result, not a successful one -- Run still returns the Result
	// so the caller (and this test) can inspect what happened.
	res, _ := Run(w, Options{Stage: "intent", Prompt: "x", MaxMinutes: 1, MaxTurns: 1})
	if res == nil || !res.Killed {
		t.Fatalf("expected a killed result after the wall clock elapsed, got %+v", res)
	}
	// Run must return on its OWN deadline, not on the child's. The child
	// sleeps 300s; anything near that means the timeout branch waited for it.
	if time.Since(start) > 10*time.Second {
		t.Fatalf("Run took too long to return after its own timeout: %s", time.Since(start))
	}
	// OR-202's own acceptance criterion: this test "completes in under a
	// second". Non-fatal so a loaded CI runner doesn't flake the suite over
	// a budget that isn't this test's actual point.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Run took %s: OR-202 expects this test under a second", elapsed)
	}

	var grandchildPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(b)) != "" {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				grandchildPID = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchildPID == 0 {
		t.Fatal("grandchild never reported its pid -- test setup is broken")
	}
	if !waitGone(t, grandchildPID) {
		killGroupPID(grandchildPID)
		t.Fatal("grandchild survived Run()'s wall-clock kill: the orphan OR-141 warns about")
	}
}

// TestGrandchildSurvivesTheAgentEndingItsOwnTurn stands in for OR-141's
// breaker-triggered path. There is no separate supervisor-side "kill" for a
// tripped breaker: the breaker's PreToolUse hook only blocks the agent's
// NEXT tool call (see internal/hook/breaker.go); the agent then ends its own
// turn, and Run observes that as an ordinary exit on the `done` channel --
// the same branch a clean success or a plain failure takes. terminate() (and
// therefore killGroup) is only ever invoked from the ctx.Done() branch, so a
// background grandchild left running when the agent wraps up this way is
// never swept.
//
// This test documents that gap rather than asserting the fix covers it: it
// intentionally asserts the OUTCOME THE TICKET WANTS (no orphan survives a
// run whose agent stopped on its own) and is expected to fail today, because
// nothing in supervisor.Run reaps a process group on a non-timeout exit.
func TestGrandchildSurvivesTheAgentEndingItsOwnTurn(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	// No `wait`, and the background job's stdio is redirected away: a real
	// backgrounded dev server or test run does not inherit its parent's
	// stdout pipe either, and leaving it attached here would make Go's
	// exec.Cmd.Wait() (which waits for that pipe's EOF, not just the direct
	// child's exit) block on the grandchild instead of exercising the
	// no-wait exit this test is about.
	fakeClaudeTree(t, "sleep 300 >/dev/null 2>&1 & echo $! > "+pidFile+"\n"+
		`echo '{"type":"result","session_id":"abc","result":"done","total_cost_usd":0.1,"is_error":false}'`+"\n"+
		"exit 0\n")

	w := ws(t, "")
	res, err := Run(w, Options{Stage: "intent", Prompt: "x", MaxMinutes: 1, MaxTurns: 1})
	if err != nil {
		t.Fatalf("Run returned an unexpected error: %v", err)
	}
	if res == nil || res.Killed {
		t.Fatalf("expected a normal (non-timeout) exit, got %+v", res)
	}

	var grandchildPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(b)) != "" {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				grandchildPID = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchildPID == 0 {
		t.Fatal("grandchild never reported its pid -- test setup is broken")
	}
	if !waitGone(t, grandchildPID) {
		killGroupPID(grandchildPID)
		t.Fatal("a grandchild left running when the agent ends its own turn " +
			"(the shape a tripped breaker leaves behind) is never reaped by Run: " +
			"only the wall-clock timeout branch calls terminate()")
	}
}
