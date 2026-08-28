//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
)

// setNewProcessGroup makes cmd the leader of its own process group. Without
// it, a killed cmd.Process takes only the direct child with it -- bash
// itself -- while whatever bash spawned (go test, npm, a dev server, a
// docker command) is reparented to init and keeps running.
func setNewProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// interruptGroup and killGroup signal every process in cmd's group, not
// just cmd itself, via the kill(2) convention of a negative pid. This only
// reaches grandchildren because setNewProcessGroup put them in that group
// in the first place.
func interruptGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
