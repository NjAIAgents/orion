//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid still exists, via the kill(2) "probe" signal 0:
// no signal is actually delivered, only the existence/permission check
// happens.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitGone polls until pid is gone or the deadline passes.
func waitGone(t *testing.T, pid int) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !alive(pid)
}

// TestTerminateKillsGrandchildrenNotJustTheDirectChild reproduces OR-141's
// concern directly: claude -p runs bash, and bash runs whatever the agent
// invoked. Killing only the direct child (the old behavior) leaves that
// grandchild running -- orphaned, still holding the worktree, still
// consuming CPU -- even though the supervisor has already reported the run
// stopped.
func TestTerminateKillsGrandchildrenNotJustTheDirectChild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	// A shell child that spawns a background grandchild (a stand-in for
	// `go test`, `npm`, a dev server) and then blocks, exactly the shape a
	// hung or runaway `claude -p` leaves behind on timeout.
	cmd := exec.Command("sh", "-c",
		"sleep 300 & echo $! > "+pidFile+"; wait")
	setNewProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting fake claude tree: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Wait for the grandchild to actually exist before killing anything.
	var grandchildPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil && strings.TrimSpace(string(b)) != "" {
			grandchildPID, err = strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchildPID == 0 {
		t.Fatal("grandchild never reported its pid")
	}
	if !alive(grandchildPID) {
		t.Fatal("grandchild died before the test could kill its parent")
	}

	terminate(cmd, done)

	if !waitGone(t, grandchildPID) {
		// Clean up so a failing test doesn't leak a sleeping process.
		_ = syscall.Kill(grandchildPID, syscall.SIGKILL)
		t.Fatal("grandchild survived terminate(): only the direct child was killed, " +
			"exactly the orphan OR-141 warns about")
	}
	if !waitGone(t, cmd.Process.Pid) {
		t.Error("direct child (bash) was not reaped")
	}
}
