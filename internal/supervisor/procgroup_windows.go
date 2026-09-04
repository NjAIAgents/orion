//go:build windows

package supervisor

import (
	"os"
	"os/exec"
	"strconv"
)

// setNewProcessGroup is a no-op on Windows: POSIX process groups do not
// exist here. killGroup below walks the tree by parent pid instead of
// relying on one being set.
func setNewProcessGroup(cmd *exec.Cmd) {}

// interruptGroup reaches only the direct child.
//
// Deliberately not extended to the tree: an interrupt is a REQUEST to stop,
// and the only process that can honour it meaningfully is the one Orion
// started. killGroup below is the one that must not miss anything.
func interruptGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}

// killGroup kills the agent AND everything it started.
//
// This reached only the direct child until OR-341, and said so. What it left
// behind is exactly what the unix side's own comment warns about: claude -p
// runs bash, which runs whatever the agent invoked -- go test, npm, a dev
// server, docker. Killing only claude leaves that whole tree running, and on
// Windows there was no second mechanism to catch it.
//
// taskkill /T walks the tree by parent pid, the nearest thing Windows offers
// to killing a process group. Shelling out rather than implementing job
// objects: a job object must be created at spawn time and carried on the
// Cmd, a far larger change, and taskkill ships with the OS.
//
// Best-effort, like the unix side: a process that has already exited makes
// taskkill exit non-zero, and there is nothing useful to do with that. The
// direct kill's error is the one returned, so the caller's contract is
// unchanged.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// The tree first, so children are gone before their parent stops being
	// their parent. /T for the tree, /F to not ask.
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	return cmd.Process.Kill()
}
