package sessions

import (
	"path/filepath"
	"strings"
	"testing"
)

// A traversal-shaped workspace id must be refused, not joined. Session.Dir is
// carried so callers can reach .orion/events.jsonl inside it, so an id of
// "../.." turns that read into a read of an arbitrary file outside the
// projects tree. These are the shapes that actually escape.
func TestScanRejectsTraversalWorkspaceID(t *testing.T) {
	for _, id := range []string{
		"..",
		"../..",
		"../../..",
		"../../../etc",
		"a/../../b",
		"sub/dir",
		"/etc",
		".",
		"",
	} {
		t.Run(id, func(t *testing.T) {
			home := t.TempDir()
			bind(t, home, "OR", id)

			got, err := Scan(home)
			if err == nil {
				t.Fatalf("Scan accepted workspace id %q and returned %v; it walks outside %s",
					id, ids(got), filepath.Join(home, "projects"))
			}
			// The message has to name the offending id, or the operator
			// cannot find which registry entry to fix.
			if id != "" && !strings.Contains(err.Error(), id) {
				t.Errorf("error %q does not name the rejected id %q", err, id)
			}
		})
	}
}

// Every id Orion creates comes out of workspace.Slugify, so lowercase
// letters, digits and hyphens is the whole legal alphabet. None of these
// shapes can traverse, but they are not something Slugify would ever
// produce either, so a hand-edited repos.json carrying one is refused on
// charset the same way a traversal shape is.
func TestScanRejectsInvalidCharsetWorkspaceID(t *testing.T) {
	for _, id := range []string{
		"Orion-83d87b",
		"orion_83d87b",
		"orion 83d87b",
		"orion.83d87b",
	} {
		t.Run(id, func(t *testing.T) {
			home := t.TempDir()
			bind(t, home, "OR", id)

			got, err := Scan(home)
			if err == nil {
				t.Fatalf("Scan accepted workspace id %q and returned %v; only lowercase letters, digits and hyphens are a workspace id",
					id, ids(got))
			}
			if !strings.Contains(err.Error(), id) {
				t.Errorf("error %q does not name the rejected id %q", err, id)
			}
		})
	}
}

// The operator fixing repos.json needs to know what a valid id looks like,
// not just that this one was rejected -- otherwise the error names the
// disease without the cure.
func TestScanErrorDescribesValidIDFormat(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "OR", "../..")

	_, err := Scan(home)
	if err == nil {
		t.Fatal("Scan accepted a traversal workspace id")
	}
	if !strings.Contains(err.Error(), "lowercase letters, digits and hyphens") {
		t.Errorf("error %q does not describe the valid id format", err)
	}
}

// The error has to hand the operator the fix, not just name the fault -- the
// registry file is not something most people edit by hand, but `orion
// unbind` is a command they already know.
func TestScanErrorNamesUnbindRecoveryCommand(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "OR", "../..")

	_, err := Scan(home)
	if err == nil {
		t.Fatal("Scan accepted a traversal workspace id")
	}
	if !strings.Contains(err.Error(), "orion unbind OR") {
		t.Errorf("error %q does not tell the operator to run `orion unbind OR`", err)
	}
}

// One bad entry in a registry that otherwise has good ones must fail the
// whole scan, not just be dropped from the results -- a partial list would
// look like a clean scan of a smaller registry rather than a registry with a
// traversal in it.
func TestScanOneInvalidIDFailsWholeScanAmongManyEntries(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "AA", "aa-ws")
	bind(t, home, "BB", "../..")
	bind(t, home, "CC", "cc-ws")

	got, err := Scan(home)
	if err == nil {
		t.Fatalf("Scan accepted a registry with a traversal entry and returned %v", ids(got))
	}
	if !strings.Contains(err.Error(), "BB") {
		t.Errorf("error %q does not name the offending entry BB", err)
	}
}

// The escape only matters because Dir is what callers append to. If a
// traversal id ever slipped through, this is the property it would break.
func TestScanSessionDirStaysUnderProjects(t *testing.T) {
	home := t.TempDir()
	bind(t, home, "OR", "orion-83d87b")
	mkws(t, home, "orphan-9z")

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	projects := filepath.Join(home, "projects")
	for _, s := range got {
		rel, err := filepath.Rel(projects, filepath.Clean(s.Dir))
		if err != nil {
			t.Fatalf("%s: Dir %q is not relative to %s: %v", s.ID, s.Dir, projects, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("%s: Dir %q escapes %s", s.ID, s.Dir, projects)
		}
	}
}

// A directory found by reading the projects tree is exempt from the charset
// check: it is a single path component handed back by the filesystem, not a
// string from repos.json, and it cannot traverse. If validID were ever
// applied to it too, a workspace somebody created by hand with an uppercase
// letter or an underscore in its name would vanish from every scan instead
// of showing up unregistered.
func TestScanIncludesOrphanDirectoryWithCharsetValidIDWouldReject(t *testing.T) {
	home := t.TempDir()
	mkws(t, home, "Some_Workspace.old")

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	found := false
	for _, s := range got {
		if s.ID == "Some_Workspace.old" {
			found = true
		}
	}
	if !found {
		t.Errorf("Scan(%v) = %v, want an unregistered session for the on-disk directory %q",
			home, ids(got), "Some_Workspace.old")
	}
}

// Ordinary ids -- Slugify output, with and without the hex suffix -- must
// keep working. A charset check that rejects a real workspace is a worse
// outage than the traversal it prevents.
func TestScanAcceptsOrdinaryWorkspaceIDs(t *testing.T) {
	home := t.TempDir()
	for key, id := range map[string]string{
		"OR":   "orion-83d87b",
		"FCIA": "fcia-11ab22",
		"X":    "a",
		"Y":    "some-longer-workspace-name",
		"Z":    "9",
	} {
		bind(t, home, key, id)
	}

	got, err := Scan(home)
	if err != nil {
		t.Fatalf("Scan rejected an ordinary workspace id: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d session(s) %v, want 5", len(got), ids(got))
	}
}
