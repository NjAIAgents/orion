// Package fakebin puts a shell-script stand-in for a real binary on PATH,
// portably.
//
// The tests in this repository fake the `claude` CLI (and occasionally other
// tools) by writing a small shell script onto PATH. On unix that is the whole
// job: the script IS an executable. Windows dispatches on the file extension
// rather than the shebang, so the script alone is invisible to exec.LookPath
// -- and the obvious shim, a .bat that hands the script to bash, corrupts its
// arguments: cmd.exe's batch processing is LINE-oriented, so an argument
// containing a newline -- which every multi-line prompt does -- is truncated
// at the first one. That was measured, not theorised: a prompt of several
// lines arrived as its first line and nothing else (OR-342).
//
// So on Windows the fake is a real PE executable with no cmd.exe anywhere in
// the chain: a COPY OF THE RUNNING TEST BINARY. Main, called from the
// package's TestMain, notices when the current process is such a copy -- the
// script sits beside the executable under the same name -- and runs the
// script with bash, forwarding argv unchanged. CreateProcess passes one flat
// string and bash's own C-runtime parsing keeps quoted newlines intact, so
// the arguments survive.
//
// Used only from _test.go files; nothing in the product imports it.
package fakebin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Main turns this process into the fake it was copied to be, when it was.
//
// Call it at the top of TestMain. On unix, or when the process is the test
// binary itself, it returns immediately and the tests run as normal. When the
// executable is a fakebin copy -- detected by the script file sitting beside
// it under the executable's own name minus ".exe" -- it runs that script
// with bash and exits with the script's code, never reaching the test runner.
func Main() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	script := strings.TrimSuffix(exe, ".exe")
	if script == exe {
		return
	}
	if _, err := os.Stat(script); err != nil {
		// Not a fake: the test binary has no script beside it.
		return
	}
	cmd := exec.Command("bash", append([]string{ShPath(script)}, os.Args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			os.Exit(xe.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "fakebin:", err)
		os.Exit(127)
	}
	os.Exit(0)
}

// Install writes `script` as the fake binary `name` in dir and prepends dir
// to PATH for the rest of the test. It returns dir so a caller can derive
// sibling paths from it.
//
// The script is always written at <dir>/<name>: on unix that is the
// executable itself; on Windows it is the sidecar that the copied test
// binary (installed beside it as <name>.exe) reads back through Main.
// Installing again into the same dir replaces the script and leaves the
// copy alone -- which is what a test that extends its fake midway needs.
func Install(t testing.TB, dir, name, script string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		exePath := filepath.Join(dir, name+".exe")
		if _, err := os.Stat(exePath); err != nil {
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(self)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(exePath, b, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// ShPath renders a path the way the bash inside a fake's script must see it.
//
// A Windows path is not a bash path: C:\Users\x\args.txt reaches bash with
// its backslashes read as escapes, so a redirect to it silently writes
// somewhere else. Git Bash accepts the forward-slash form with the drive
// letter left in place, so the conversion is just the separator.
func ShPath(p string) string {
	if runtime.GOOS != "windows" {
		return p
	}
	return strings.ReplaceAll(p, `\`, "/")
}
