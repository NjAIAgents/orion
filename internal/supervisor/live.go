package supervisor

import (
	"os/exec"
	"sort"
	"sync"
	"time"
)

// The registry of children running under this process, so a caller that has
// to stop NOW can reach their process groups.
//
// Nothing else holds them. Every kill path in this package -- terminate() on
// the wall clock, the sweep on an ordinary exit -- is reached from runOnce's
// own goroutine, and that goroutine dies with the process when the watcher
// is forced to quit. So a forced quit killed the watcher and left `claude -p`
// running: reparented to init, still holding a worktree, still spending, and
// killable only by hand once somebody noticed (OR-195).
//
// The child cannot see the signal itself. setNewProcessGroup deliberately
// puts it outside the terminal's foreground process group -- that is what
// makes the timeout kill reach grandchildren -- which is exactly why ctrl-c
// never reaches it. Signalling it is the parent's job, and this is what the
// parent has to signal.
var (
	liveMu   sync.Mutex
	liveKids = map[*supervised]struct{}{}
)

type supervised struct {
	cmd *exec.Cmd
	// gone closes when cmd.Wait has returned, which is the only honest
	// signal that the child is dead. A pid check cannot answer it: a killed
	// child is a zombie, indistinguishable from a live one, until its parent
	// reaps it.
	gone chan struct{}
}

// track registers a started child. The caller must call done() once
// cmd.Wait has returned, or the child is reported as a survivor forever.
func track(cmd *exec.Cmd) *supervised {
	c := &supervised{cmd: cmd, gone: make(chan struct{})}
	liveMu.Lock()
	liveKids[c] = struct{}{}
	liveMu.Unlock()
	return c
}

func (c *supervised) done() {
	liveMu.Lock()
	_, tracked := liveKids[c]
	delete(liveKids, c)
	liveMu.Unlock()
	if tracked {
		close(c.gone)
	}
}

// KillAll SIGKILLs the process group of every child still running under this
// process, waits up to grace for each to be reaped, and returns the pids
// that were still alive when it gave up, lowest first.
//
// It returns them rather than swallowing them because a named pid is a thing
// a person can deal with and an orphan they do not know about is not.
//
// The GROUP, not the pid: each child leads its own group (see
// setNewProcessGroup), so killing the pid alone would leave whatever the
// agent started with `cmd &` running. SIGKILL rather than the polite SIGINT
// first step terminate() takes -- this is the path a person reaches for
// after already asking once, and it must be faster than draining, not
// safer.
func KillAll(grace time.Duration) []int {
	liveMu.Lock()
	kids := make([]*supervised, 0, len(liveKids))
	for c := range liveKids {
		kids = append(kids, c)
	}
	liveMu.Unlock()

	for _, c := range kids {
		_ = killGroup(c.cmd)
	}

	deadline := time.Now().Add(grace)
	var survived []int
	for _, c := range kids {
		select {
		case <-c.gone:
		case <-time.After(time.Until(deadline)):
			if c.cmd.Process != nil {
				survived = append(survived, c.cmd.Process.Pid)
			}
		}
	}
	sort.Ints(survived)
	return survived
}
