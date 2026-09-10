package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A corrupt registry must fail loudly, not read as empty: starting empty
// would report every already-bound workspace as unregistered.
func TestScanRegistryReadErrorCausesError(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "repos.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Scan(home); err == nil {
		t.Fatal("Scan on a corrupt registry returned no error; a fault was silently reported as an empty board")
	}
}

// Distinct from the corrupt-JSON case above: here the file parses fine (it
// isn't even read) because the OS refuses the read itself. registry.Load
// only special-cases os.IsNotExist; any other os.ReadFile error -- including
// permission denied -- must propagate rather than fall back to an empty
// registry and report every already-bound workspace as unregistered.
//
// Windows has no POSIX permission bits (OR-334): chmod there does not
// produce a read error, so this assertion is only meaningful on POSIX.
func TestScanRegistryFileUnreadableCausesError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not expressible on Windows (OR-334)")
	}

	home := t.TempDir()
	repos := filepath.Join(home, "repos.json")
	if err := os.WriteFile(repos, []byte(`{"version":1,"repos":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(repos, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(repos, 0o600) })

	if _, err := Scan(home); err == nil {
		t.Fatal("Scan on an unreadable registry file returned no error")
	}
}
