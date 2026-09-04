//go:build windows

package suite

import (
	"os/exec"
	"strconv"
)

// setProcessGroup is a no-op on Windows: POSIX process groups do not exist
// here, and killGroup below walks the tree by parent pid instead of relying
// on one being set.
func setProcessGroup(cmd *exec.Cmd) {}

// killGroup kills the child AND everything it started.
//
// Until OR-341 this reached only the direct child, and said so in a comment.
// The gap was not cosmetic: cmd.Wait() returns when the output pipes close,
// not when the child exits, and a grandchild inherits those pipes. A
// timed-out suite whose script had backgrounded anything therefore held Run
// open for the whole life of the grandchild -- a 300ms deadline waited 60
// seconds, which is exactly what the deadline test measured on the Windows
// CI leg.
//
// taskkill /T walks the tree by parent pid, which is the nearest thing
// Windows offers to killing a process group. Shelling out rather than
// implementing job objects: a job object has to be created at spawn time and
// carried on the Cmd, which is a much larger change than this gap warrants,
// and taskkill ships with the OS.
//
// Best-effort throughout, as on the unix side: a process that has already
// exited makes taskkill exit non-zero, and there is nothing useful to do
// with that.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// The tree first, so the children are gone before their parent stops
	// being their parent. /T for the tree, /F to not ask.
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	_ = cmd.Process.Kill()
}
