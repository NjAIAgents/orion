//go:build !windows

package testproc

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

// The property the whole package exists for: when the test that spawned a
// command ends, nothing it started is still running -- including a
// GRANDCHILD, which is what a plain exec.Command leaves reparented to init.
//
// Driven through a sub-test so the cleanup has actually run by the time the
// assertion is made.
func TestCleanupKillsTheWholeGroupIncludingGrandchildren(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell here")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	var pid int
	t.Run("spawn", func(t *testing.T) {
		// sh is the child; the backgrounded sleep is the grandchild, and it
		// records its own pid so the parent test can look for it after this
		// sub-test's cleanup has run.
		cmd := Command(t, "sh", "-c",
			"sleep 120 & echo $! > "+pidFile+"; wait")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// READ HERE, inside the sub-test, and only once the file has content.
		// Reading it afterwards raced the shell's own write: an empty file
		// then looked like a missing grandchild rather than a killed one.
		waitFor(t, func() bool {
			b, err := os.ReadFile(pidFile)
			if err != nil {
				return false
			}
			n, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				return false
			}
			pid = n
			return true
		}, "the grandchild never reported its pid")
	})

	if pid == 0 {
		t.Fatal("no grandchild pid was captured")
	}
	// SIGKILL is asynchronous: the kernel reaps on its own schedule, so a
	// check made in the same instant can still see the process. Give it a
	// moment before concluding it survived.
	waitFor(t, func() bool { return !alive(pid) },
		"the grandchild survived the test that started it: "+
			"this is the orphan that filled the machine")
	if alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the grandchild (pid %d) survived the test that started it: "+
			"this is the orphan that filled the machine", pid)
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func alive(pid int) bool {
	// Signal 0 tests for existence without delivering anything.
	return syscall.Kill(pid, 0) == nil
}
