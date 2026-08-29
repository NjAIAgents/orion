//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startTree starts a process that leaves a background grandchild behind and
// reports both pids, standing in for `claude -p` running bash running a test
// run or a dev server. Registered with track() exactly as runOnce does.
func startTree(t *testing.T) (*supervised, int, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := exec.Command("/bin/sh", "-c",
		"sleep 300 >/dev/null 2>&1 & echo $! > "+pidFile+"\nsleep 300\n")
	setNewProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := track(cmd)
	go func() {
		_ = cmd.Wait()
		c.done()
	}()

	var grandchild int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				grandchild = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("grandchild never reported its pid -- test setup is broken")
	}
	return c, cmd.Process.Pid, grandchild
}

// TestKillAllKillsEveryLiveChildNotJustTheFirst is OR-195's concurrency
// case: at max_concurrent_tickets 2 a forced quit orphans two agents, at the
// ceiling of 5 it orphans five. Killing whatever the registry happened to
// hand back first is not a fix.
//
// The grandchildren matter as much as the children: killing the pid rather
// than the group is what leaves a dev server or a test run behind.
func TestKillAllKillsEveryLiveChildNotJustTheFirst(t *testing.T) {
	var pids []int
	for i := 0; i < 3; i++ {
		_, child, grandchild := startTree(t)
		pids = append(pids, child, grandchild)
	}

	if survived := KillAll(5 * time.Second); len(survived) != 0 {
		t.Fatalf("KillAll reported survivors it should have killed: %v", survived)
	}
	for _, pid := range pids {
		if !waitGone(t, pid) {
			killGroupPID(pid)
			t.Fatalf("pid %d survived KillAll: the orphan a forced quit leaves behind", pid)
		}
	}
	if n := len(liveKids); n != 0 {
		t.Fatalf("registry still holds %d children after they all exited", n)
	}
}

// TestKillAllReachesAChildStartedByTheSupervisor closes the loop the test
// above leaves open: it is runOnce, not the test helper, that has to put a
// started agent into the registry. Without that registration KillAll is a
// correct function nobody's children are in.
func TestKillAllReachesAChildStartedByTheSupervisor(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	fakeClaudeTree(t, "sleep 300 >/dev/null 2>&1 & echo $! > "+pidFile+"\nsleep 300\n")

	w := ws(t, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Generous wall clock: this run must be ended by KillAll, not by the
		// timeout, or the test proves the wrong thing.
		_, _ = Run(w, Options{Stage: "intent", Prompt: "x", MaxMinutes: 30, MaxTurns: 1})
	}()

	var grandchild int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				grandchild = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("the agent never started -- test setup is broken")
	}

	if survived := KillAll(5 * time.Second); len(survived) != 0 {
		t.Fatalf("a child started by Run survived KillAll: %v", survived)
	}
	if !waitGone(t, grandchild) {
		killGroupPID(grandchild)
		t.Fatal("the agent's own child survived: KillAll signalled the pid, not the group")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run never returned after its child was killed")
	}
}

// TestKillAllNamesAChildItCouldNotKill covers the last-resort clause: a pid
// that is still there when the grace elapses is REPORTED, because a named
// pid is something a person can kill and a silent one is an orphan they
// never learn about.
//
// The child here is registered and never reaped, which is what an
// unkillable process looks like from the caller's side: the kill goes out,
// and nothing ever confirms the process is gone.
func TestKillAllNamesAChildItCouldNotKill(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 300")
	setNewProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := track(cmd)
	t.Cleanup(func() {
		_ = killGroup(cmd)
		_ = cmd.Wait()
		c.done()
	})

	survived := KillAll(100 * time.Millisecond)
	if len(survived) != 1 || survived[0] != cmd.Process.Pid {
		t.Fatalf("expected KillAll to name pid %d, got %v", cmd.Process.Pid, survived)
	}
}
