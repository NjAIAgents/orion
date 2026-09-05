package testproc

import "testing"

// Registering a command and never starting it must not make cleanup panic or
// fail: a test can register one and then fail before Start.
//
// Untagged, unlike testproc_test.go beside it, because nothing here is POSIX:
// the command is never executed, so its name is a string rather than a
// program, and the property under test -- that cleanup tolerates a process
// that does not exist -- is exactly the one Windows needs covered too.
func TestCleanupOnACommandThatNeverStartedIsSafe(t *testing.T) {
	Command(t, "true") // never started; cleanup runs at test end
}
