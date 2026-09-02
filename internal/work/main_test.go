package work

import (
	"os"
	"testing"
)

// TestMain pins the terminal width for the whole package.
//
// ui clips a message to COLUMNS when the environment sets it, and many tests
// here assert on the TEXT of a line. Without this they pass or fail on the
// width of whatever terminal ran them: TestCommitFailureFromGitInfrastructure
// ErrorLeavesFilesUntouched looks for "could NOT commit" and "KEPT" in one
// message, and KEPT sits far enough along that an 100-column terminal cuts it
// off while a wider one does not.
//
// That failed a release twice. It reads as a broken test -- the output even
// contains most of the expected sentence, trailing off at "could NOT commit 2
// unco..." -- and the code was never wrong.
//
// Process-global rather than per-test, because COLUMNS is process-global:
// one place to set it is one place to get it right, and a test added later
// inherits the fix instead of having to remember it. internal/watch does the
// same thing per-test in runWatch, with the same reasoning.
func TestMain(m *testing.M) {
	// Empty rather than a large number: ui.columns() treats an unparseable or
	// under-40 value as "do not clip", which is what a test asserting on full
	// text needs. A width would still clip, just further out.
	os.Setenv("COLUMNS", "")
	os.Exit(m.Run())
}
