// Package testproc starts a subprocess from a test so that killing the test
// kills everything it started.
//
// WHY THIS EXISTS. On 2026-09-03 a working day was lost to 619 orphaned
// .test binaries and a load average of 256. They were cleared four times and
// came back each time. The cause was not Orion: its agent path has put every
// child in its own process group since OR-195, and KillAll signals the group.
// It was the TESTS. Around twenty of them spawn a long-lived binary -- the
// built orion binary, `go test`, `go build` -- with a plain exec.Command, so
// when the parent `go test` is killed (a harness timeout, a ctrl-c, a
// cancelled CI job) the child is reparented to init and runs forever.
//
// Nothing reaps it, and every measurement taken afterwards is worthless:
// packages that take twenty seconds take ten minutes, and tests with a clock
// in them fail for reasons that have nothing to do with the code (OR-332).
//
// So a test that spawns uses Command or Start from here. The child leads its
// own process group, and t.Cleanup kills that group when the test ends --
// including when it fails, panics or is killed, because Cleanup runs on all
// three.
package testproc

import (
	"context"
	"os/exec"
	"testing"
)

// Command is exec.Command with the child in its own process group and a
// cleanup that kills the group when the test ends.
//
// Use it for anything that could outlive the test: the orion binary, a go
// build or test, a shell. A git call that returns in milliseconds does not
// need it, and wrapping every one of those would be noise around a hazard
// that does not exist.
func Command(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	return prepare(t, exec.Command(name, args...))
}

// CommandContext is Command with a context, for a spawn that also needs a
// deadline. The context cancels the direct child; the cleanup still reaches
// the group.
func CommandContext(t *testing.T, ctx context.Context, name string, args ...string) *exec.Cmd {
	t.Helper()
	return prepare(t, exec.CommandContext(ctx, name, args...))
}

// Prepare puts an already-built command in its own group and registers the
// cleanup. For a caller that has to construct the command itself -- setting
// Dir, Env or Stdin before it starts.
func Prepare(t *testing.T, cmd *exec.Cmd) *exec.Cmd {
	t.Helper()
	return prepare(t, cmd)
}

func prepare(t *testing.T, cmd *exec.Cmd) *exec.Cmd {
	setNewProcessGroup(cmd)
	// KILLED BY GROUP ID, captured while the leader is alive.
	//
	// The obvious cleanup -- kill(-cmd.Process.Pid) at test end -- does not
	// work, and its own test caught that: by the time cleanup runs the
	// leader has usually exited, so its pid names a dead process and the
	// negative-pid kill reaches nothing while the grandchild it spawned
	// runs on. The group id equals the leader's pid, so reading it once the
	// process exists and keeping it is what makes the kill land.
	//
	// REGISTERED BEFORE THE COMMAND RUNS, not after it returns: a test that
	// fails between here and its own cleanup would otherwise leave the child
	// behind, which is the case that filled the machine.
	t.Cleanup(func() { killStartedGroup(cmd) })
	return cmd
}
