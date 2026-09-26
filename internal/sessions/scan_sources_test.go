package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A projects directory that exists but can't be read (permission denied) is
// a real fault, distinct from ENOENT's fresh-install case, and must surface
// as an error rather than silently reporting an empty board.
//
// Windows has no POSIX permission bits (OR-334): chmod there does not
// produce a read error, so this assertion is only meaningful on POSIX.
func TestScanProjectsDirReadErrorCausesError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not expressible on Windows (OR-334)")
	}

	home := t.TempDir()
	projects := filepath.Join(home, "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(projects, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(projects, 0o700) })

	if _, err := Scan(home); err == nil {
		t.Fatal("Scan on an unreadable projects directory returned no error")
	}
}

// An empty projects directory, with the registry present, is not a fault:
// it just means every registered workspace's directory already accounts
// for everything on disk and nothing unbound was left behind.
func TestScanEmptyProjectsDirWithRegistryIsNotAnError(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "OR", "orion-83d87b")
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].Key != "OR" || got[0].ID != "orion-83d87b" {
		t.Fatalf("got %v, want just the one registry entry", got)
	}
}
