//go:build !windows

package testproc

import (
	"os/exec"
	"syscall"
)

// setNewProcessGroup makes cmd the leader of its own process group, so a
// later kill reaches whatever it spawned rather than only the direct child.
func setNewProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killStartedGroup signals every process in cmd's group, via the kill(2)
// convention of a negative pid.
//
// The group id is read with getpgid rather than assumed to be the leader's
// pid, because by cleanup time the leader has often exited: its pid then
// names nothing, the kill reaches nothing, and the grandchild survives --
// exactly the orphan this package exists to prevent, and exactly what its
// own test caught. getpgid on a reaped leader fails, so the pid is used as
// the group id in that case, which is what it was when the group was made.
//
// A command that never started is not an error: cleanup runs on every path,
// including the ones where the command failed to launch.
func killStartedGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		pgid = pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
