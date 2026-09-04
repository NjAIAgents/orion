package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeBin puts a shell-script stand-in for `name` on PATH and returns the
// directory holding it.
//
// Windows will not exec a file because it starts with `#!` -- it dispatches on
// the extension -- so the script is written as <name> and a <name>.bat that
// hands it to bash is written beside it. Git Bash ships on the GitHub Windows
// runner and is what the CI job's own steps already use, so this runs the SAME
// script on every platform rather than skipping the test on one of them.
//
// Until OR-340 these fakes were POSIX-only, and every test that needed one
// failed on Windows with "claude CLI not found on PATH" -- the fake was on
// PATH, and unrunnable.
func writeFakeBin(t *testing.T, name, script string) string {
	t.Helper()
	return writeFakeBinIn(t, t.TempDir(), name, script)
}

// writeFakeBinIn is writeFakeBin into a directory the caller already holds --
// for a test that derives other paths from it, or that replaces the fake's
// body partway through and needs the .bat shim regenerated alongside.
func writeFakeBinIn(t *testing.T, dir, name, script string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// %~dp0 is the directory of the .bat itself, so the pair stays
		// relocatable; %* forwards the arguments unchanged.
		shim := "@echo off\r\nbash \"%~dp0" + name + "\" %*\r\n"
		if err := os.WriteFile(filepath.Join(dir, name+".bat"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// shPath renders a path the way the bash inside a fake binary must see it.
//
// A Windows path is not a bash path: C:\Users\x\args.txt reaches bash with
// its backslashes read as escapes, so a redirect to it silently writes
// somewhere else. That is how the first attempt at these fakes produced a
// script that ran, exited 0, and created no file -- the test then failed on
// the missing file rather than on anything it was about (OR-341).
//
// Git Bash accepts the forward-slash form with the drive letter left in
// place, so the conversion is just the separator.
func shPath(p string) string {
	if runtime.GOOS != "windows" {
		return p
	}
	return strings.ReplaceAll(p, `\`, "/")
}
