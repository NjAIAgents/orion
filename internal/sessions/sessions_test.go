package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/registry"
)

// bind registers a project key against a workspace id, the way `orion init`
// does, so the tests exercise the real registry rather than a hand-written
// repos.json that could drift from its format.
func bind(t *testing.T, home, key, wsID string) {
	t.Helper()
	if err := registry.Bind(home, registry.Entry{
		Key:       key,
		Source:    filepath.Join(home, "src", key),
		Workspace: wsID,
	}); err != nil {
		t.Fatalf("bind %s: %v", key, err)
	}
}

// mkws creates a workspace directory under <home>/projects.
func mkws(t *testing.T, home, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "projects", id), 0o700); err != nil {
		t.Fatalf("mkws %s: %v", id, err)
	}
}

func ids(ss []Session) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

// Neither source present is a fresh install, not a fault. If this regresses,
// the run view refuses to draw on a machine where nothing has run yet.
func TestScanEmptyHomeIsNotAnError(t *testing.T) {
	got, err := Scan(t.TempDir())
	if err != nil {
		t.Fatalf("Scan on an empty home returned %v; a fresh install is a normal state", err)
	}
	if len(got) != 0 {
		t.Errorf("Scan on an empty home returned %v, want none", got)
	}
}

// Registry entries with no projects directory yet: the registry is the only
// source, and a missing directory must not swallow it.
func TestScanRegistryOnly(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "OR", "orion-83d87b")
	bind(t, home, "FCIA", "fcia-11ab22")

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d session(s) %v, want one per registry entry", len(got), ids(got))
	}
	// Keys sorted: FCIA before OR.
	if got[0].Key != "FCIA" || got[0].ID != "fcia-11ab22" {
		t.Errorf("first session is %+v, want FCIA/fcia-11ab22", got[0])
	}
	if got[1].Key != "OR" || got[1].ID != "orion-83d87b" {
		t.Errorf("second session is %+v, want OR/orion-83d87b", got[1])
	}
	if want := filepath.Join(home, "projects", "orion-83d87b"); got[1].Dir != want {
		t.Errorf("Dir is %q, want %q", got[1].Dir, want)
	}
}

// A registry entry whose workspace directory is gone is still reported --
// the same rule registry.Prune states. Dropping it would make a vanished
// workspace indistinguishable from one that was never bound.
func TestScanKeepsEntryWhoseWorkspaceIsMissing(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "OR", "orion-83d87b")
	mkws(t, home, "other-9f")

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 || got[0].Key != "OR" || got[0].ID != "orion-83d87b" {
		t.Fatalf("got %v, want the missing OR workspace still listed", got)
	}
}

// A workspace nobody bound is exactly what `orion new` leaves behind, and it
// is invisible to every key-resolving command. It must still be enumerated,
// with an empty Key marking it unregistered.
func TestScanIncludesUnregisteredWorkspaces(t *testing.T) {
	home := t.TempDir()
	mkws(t, home, "zeta-01")
	mkws(t, home, "alpha-02")

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d session(s) %v, want 2", len(got), ids(got))
	}
	for _, s := range got {
		if s.Key != "" {
			t.Errorf("%s has key %q; an unbound workspace has no key", s.ID, s.Key)
		}
	}
	if got[0].ID != "alpha-02" || got[1].ID != "zeta-01" {
		t.Errorf("unregistered order is %v, want sorted by id", ids(got))
	}
}

// The union, not the concatenation: a workspace that IS registered must not
// also appear as an orphan. A duplicated card is a run that never happened.
func TestScanDoesNotDuplicateARegisteredWorkspace(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "OR", "orion-83d87b")
	mkws(t, home, "orion-83d87b")
	mkws(t, home, "unbound-7c")

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d session(s) %v, want 2 (one registered, one orphan)", len(got), ids(got))
	}
	if got[0].Key != "OR" || got[0].ID != "orion-83d87b" {
		t.Errorf("registered session is %+v, want OR/orion-83d87b first", got[0])
	}
	if got[1].Key != "" || got[1].ID != "unbound-7c" {
		t.Errorf("orphan session is %+v, want unbound-7c with no key", got[1])
	}
}

// Files and the registry's own repos.json/repos.lock sit beside the projects
// directory, but anything non-directory inside it is not a workspace.
func TestScanSkipsNonDirectories(t *testing.T) {
	home := t.TempDir()
	mkws(t, home, "real-01")
	if err := os.WriteFile(filepath.Join(home, "projects", "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].ID != "real-01" {
		t.Errorf("got %v, want only the real workspace", ids(got))
	}
}

// Two identical scans must produce an identical list. A grid that reshuffles
// between refreshes cannot be read or diffed.
func TestScanOrderIsStable(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "OR", "orion-83d87b")
	bind(t, home, "FCIA", "fcia-11ab22")
	for _, id := range []string{"m-3", "a-1", "z-9", "b-2"} {
		mkws(t, home, id)
	}

	first, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	second, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []string{"fcia-11ab22", "orion-83d87b", "a-1", "b-2", "m-3", "z-9"}
	for i, w := range want {
		if first[i].ID != w {
			t.Fatalf("scan order is %v, want %v", ids(first), want)
		}
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("two scans of one home disagree at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// An unreadable registry is a real failure and must surface: starting empty
// would report every bound workspace as unregistered.
func TestScanFailsOnACorruptRegistry(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "repos.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(home); err == nil {
		t.Error("Scan accepted a corrupt registry; every bound workspace would read as unregistered")
	}
}
