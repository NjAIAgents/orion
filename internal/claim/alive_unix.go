//go:build !windows

package claim

import (
	"errors"
	"os"
	"syscall"
)

// processExists asks the kernel whether pid is still there.
//
// Signal 0 performs the permission and existence checks and delivers nothing,
// which is the portable POSIX way to ask. A process owned by another user
// answers "exists" via EPERM, and that is the right answer here: it exists,
// so its claim must not be stolen.
func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}
