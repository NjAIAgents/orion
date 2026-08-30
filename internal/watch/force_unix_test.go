//go:build !windows

package watch

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestSIGTERMForcesTheSameWayAsCtrlC drives real signals through the real
// registration, rather than a channel the test writes itself: `kill
// <watcher-pid>` from another shell has exactly the shape of ctrl-c, and an
// unattended watcher is more likely to be stopped that way than
// interactively. If SIGTERM were left off signal.Notify, or the second one
// were handed back to the default disposition, this test's process would be
// terminated instead of reaching the force path.
func TestSIGTERMForcesTheSameWayAsCtrlC(t *testing.T) {
	quiet(t)
	// Written only by the handler goroutine; never read here, so there is
	// nothing for the race detector to catch and nothing for this test to
	// assert twice -- force_test.go already pins the wording.
	var out bytes.Buffer
	exited := make(chan int, 1)
	stop := listen(&out, func(code int) { exited <- code })
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !stopping.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !stopping.Load() {
		t.Fatal("SIGTERM did not reach the watcher's handler at all")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code == 0 {
			t.Fatal("a forced quit exited 0")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second SIGTERM did not force; the watcher would have died " +
			"with its agents still running")
	}
}
