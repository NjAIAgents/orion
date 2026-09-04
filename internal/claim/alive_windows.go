package claim

import "syscall"

// processExists asks Windows whether pid is still there.
//
// Windows has no signals, so the POSIX `Signal(0)` probe the unix build uses
// does not exist here -- os.Process.Signal returns an error for any signal on
// this platform, which made alive() report every live agent as dead and let a
// watcher steal a running claim (OR-341).
//
// OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION is the equivalent
// question. That right is deliberately the weakest one that answers it: a
// process running as another user can still be OPENED with it, so -- exactly
// as EPERM does on unix -- somebody else's live process reads as alive rather
// than as free to steal.
//
// A pid that has exited but whose handle is still held by a parent reports
// alive here. That is the safe direction: the cost is a claim released a
// little late, against a claim stolen from a running agent.
func processExists(pid int) bool {
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	// The handle outlives the process, so holding one is not proof of life:
	// a still-running process has no exit code yet.
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true // openable but unreadable: assume alive, never steal
	}
	const stillActive = 259 // STILL_ACTIVE
	return code == stillActive
}
