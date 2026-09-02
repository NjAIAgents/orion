//go:build windows

package suite

import "os/exec"

// setProcessGroup is a no-op on Windows: POSIX process groups do not exist
// here, and reaping a whole tree would need job objects this package does
// not implement. A timed-out suite's grandchildren on Windows are therefore
// NOT reaped.
//
// That is a real gap, stated rather than hidden, exactly as
// internal/supervisor states the same one. It matters more here than it
// looks: Windows is where the suite is slowest (OR-292 measured
// internal/collect at 559s), so it is the platform most likely to hit a
// timeout in the first place.
func setProcessGroup(cmd *exec.Cmd) {}

// killGroup can only reach the direct child on Windows.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
