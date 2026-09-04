package supervisor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFakeBin puts a shell-script stand-in for `name` on PATH.
//
// Windows dispatches on the extension rather than on `#!`, so a bare script
// is not executable there. A <name>.bat handing the script to bash is written
// beside it; Git Bash ships on the GitHub Windows runner and is what the CI
// job's own steps use, so the SAME script runs on every platform instead of
// the test being skipped on one (OR-340).
func writeFakeBin(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		shim := "@echo off\r\nbash \"%~dp0" + name + "\" %*\r\n"
		if err := os.WriteFile(filepath.Join(dir, name+".bat"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}
