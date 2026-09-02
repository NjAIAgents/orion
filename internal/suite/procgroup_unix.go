//go:build !windows

package suite

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes cmd the leader of its own process group, so a
// timeout can reach what it spawned.
//
// A test runner is a tree: `go test` compiles and then runs one binary per
// package, and a script runner spawns whatever the script names. Killing
// only the direct child leaves those reparented to init and running, which
// on a hung suite means a machine quietly accumulating orphaned compilers.
// Same reasoning as internal/supervisor's own group handling (OR-141).
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup signals every process in cmd's group via the kill(2) convention
// of a negative pid. Safe to call after a clean exit: the group is gone and
// the error is discarded.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
