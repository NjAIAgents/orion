package toolkit

import (
	"os"
	"path/filepath"
	"testing"
)

// requireSymlinks skips when the OS will not let this process create one.
//
// Windows needs either Developer Mode or SeCreateSymbolicLinkPrivilege, and
// the GitHub runner grants neither -- so os.Symlink fails and every test that
// resolves an installed skill back to its clone reported fromRunnerSymlink =
// "" (OR-340).
//
// Probed rather than assumed from runtime.GOOS: a Windows machine WITH
// Developer Mode on should run these tests, and a probe says what is true
// here instead of what is usually true.
func requireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Skipf("this platform will not create symlinks for this process: %v", err)
	}
}

// setHome points os.UserHomeDir at dir on every platform.
//
// t.Setenv("HOME") alone is not enough: os.UserHomeDir reads USERPROFILE on
// Windows and ignores HOME entirely, so a test that set only HOME left
// fromRunnerSymlink searching the RUNNER's real home -- where the fixture
// does not exist -- and it returned "" (OR-341).
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
