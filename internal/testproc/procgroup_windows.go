//go:build windows

package testproc

import "os/exec"

// setNewProcessGroup is a no-op on Windows: POSIX process groups do not
// exist here. Reaching a whole tree would need job objects, which this
// package does not implement -- so a killed test's GRANDchildren on Windows
// are not reaped. Left honest rather than papered over, exactly as
// internal/supervisor's own Windows file is.
func setNewProcessGroup(cmd *exec.Cmd) {}

// killStartedGroup can only reach the direct child on Windows.
func killStartedGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
