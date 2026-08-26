package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectOfHandlesIssueAndProjectKeys(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"FCIA-6", "FCIA"},
		{"FCIA", "FCIA"},
		{"fcia-6", "FCIA"},
		{"  FCIA-123  ", "FCIA"},
		// A hyphen in the key itself must not be truncated: the tail is only
		// an issue number when it actually IS a number.
		{"MY-PROJ", "MY-PROJ"},
		{"MY-PROJ-42", "MY-PROJ"},
		{"", ""},
	} {
		if got := ProjectOf(tc.in); got != tc.want {
			t.Errorf("ProjectOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The collision this package exists to prevent: two repositories claiming
// one project key would each believe the queue is theirs, and work would
// land in whichever ran last.
func TestBindRefusesASecondRepoForTheSameKey(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "repo-a")
	b := filepath.Join(home, "repo-b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Bind(home, Entry{Key: "FCIA", Source: a, Workspace: "ws-a"}); err != nil {
		t.Fatal(err)
	}
	err := Bind(home, Entry{Key: "FCIA", Source: b, Workspace: "ws-b"})
	if err == nil {
		t.Fatal("a second repository silently took the key")
	}
	if !strings.Contains(err.Error(), a) {
		t.Errorf("the refusal must name the repo that already holds it, got: %v", err)
	}
	got, err := Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != "ws-a" {
		t.Errorf("binding = %+v; the original must survive a refused overwrite", got)
	}
}

// orion init is how a repo is repaired, so re-binding the same source has to
// be an update rather than a collision.
func TestRebindingTheSameSourceIsAnUpdate(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "repo")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Bind(home, Entry{Key: "FCIA", Source: src, Workspace: "old", Channel: "C1"}); err != nil {
		t.Fatal(err)
	}
	if err := Bind(home, Entry{Key: "FCIA", Source: src, Workspace: "new", Channel: "C2"}); err != nil {
		t.Fatalf("re-adopting the same repo must not fail: %v", err)
	}
	e, _ := Lookup(home, "FCIA")
	if e.Workspace != "new" || e.Channel != "C2" {
		t.Errorf("entry = %+v, want the updated values", e)
	}
}

func TestLookupExplainsWhatIsRegistered(t *testing.T) {
	home := t.TempDir()
	if _, err := Lookup(home, "FCIA-6"); err == nil ||
		!strings.Contains(err.Error(), "orion init") {
		t.Errorf("an empty registry should point at the fix, got: %v", err)
	}
	src := filepath.Join(home, "r")
	_ = os.MkdirAll(src, 0o755)
	_ = Bind(home, Entry{Key: "OTHER", Source: src})
	_, err := Lookup(home, "FCIA-6")
	if err == nil || !strings.Contains(err.Error(), "OTHER") {
		t.Errorf("an unknown key should list what IS registered, got: %v", err)
	}
}

// A corrupt registry must not read as empty: starting fresh would let a
// second repository claim a key the first still owns.
func TestCorruptRegistryRefusesRatherThanResetting(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "repos.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("a corrupt registry silently became an empty one")
	}
	if err := Bind(home, Entry{Key: "FCIA", Source: home}); err == nil {
		t.Error("Bind wrote over a registry it could not read")
	}
}

func TestSaveIsOwnerOnly(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "r")
	_ = os.MkdirAll(src, 0o755)
	if err := Bind(home, Entry{Key: "FCIA", Source: src}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(home, "repos.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %o; it records local paths and channel ids", mode)
	}
}

// A missing source directory is reported, never silently forgotten: an
// unmounted volume looks exactly like a deletion, and dropping the binding
// would free the key for a different repository.
func TestPruneReportsWithoutDeleting(t *testing.T) {
	home := t.TempDir()
	gone := filepath.Join(home, "gone")
	_ = os.MkdirAll(gone, 0o755)
	_ = Bind(home, Entry{Key: "FCIA", Source: gone})
	_ = os.RemoveAll(gone)

	missing, err := Prune(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0].Key != "FCIA" {
		t.Fatalf("missing = %+v", missing)
	}
	if _, err := Lookup(home, "FCIA"); err != nil {
		t.Error("Prune deleted the binding; it must only report")
	}
}

func TestUnbind(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "r")
	_ = os.MkdirAll(src, 0o755)
	_ = Bind(home, Entry{Key: "FCIA", Source: src})
	if err := Unbind(home, "fcia"); err != nil {
		t.Fatalf("unbind should accept any case: %v", err)
	}
	if _, err := Lookup(home, "FCIA"); err == nil {
		t.Error("still registered after unbind")
	}
	if err := Unbind(home, "FCIA"); err == nil {
		t.Error("unbinding something absent should say so")
	}
}
