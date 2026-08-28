//go:build windows

package supervisor

import (
	"os"
	"os/exec"
)

// setNewProcessGroup is a no-op on Windows: POSIX process groups do not
// exist here. Killing the whole tree would need Windows job objects, which
// this package does not implement -- so a killed run's grandchildren on
// Windows are NOT reaped. That is a real, known gap, left honest rather
// than papered over with a fix that only looks like it works.
func setNewProcessGroup(cmd *exec.Cmd) {}

// interruptGroup and killGroup can only reach the direct child on Windows.
func interruptGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
