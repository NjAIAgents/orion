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
