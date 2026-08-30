package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docs/decisions/0009: one canonical slug names the tracker project, the
// workspace and the git repo. It can only do that if the workspace id IS the
// slug -- a random suffix makes the filesystem name differ from the other two
// on every run, which is the mapping table 0009 exists to remove.
func TestCanonicalSlugBecomesTheWorkspaceIdVerbatim(t *testing.T) {
	home(t)
	ws, err := New(NewOptions{Idea: "Take payments from the web app", Slug: "orion-payments"})
	if err != nil {
		t.Fatal(err)
	}
	if ws.ID != "orion-payments" {
		t.Errorf("id = %q, want exactly %q: a suffix here is a second name for one project",
			ws.ID, "orion-payments")
	}
	if ws.Task.Slug != "orion-payments" {
		t.Errorf("Task.Slug = %q, want %q: the stage prompts name artefacts by this",
			ws.Task.Slug, "orion-payments")
	}
	if filepath.Base(ws.Dir) != "orion-payments" {
		t.Errorf("directory %q is not named by the canonical slug", ws.Dir)
	}
}

// The slug arrives from a tracker project's FREE-TEXT name and becomes a path
// component, so it is sanitised rather than trusted. A name with spaces, a
// slash or a traversal in it must not reach the filesystem as typed.
func TestCanonicalSlugIsSanitisedBeforeItBecomesAPath(t *testing.T) {
	h := home(t)
	for _, in := range []string{"Orion Payments", "../../etc/orion", "Orion/Payments"} {
		ws, err := New(NewOptions{Idea: "an idea", Slug: in})
		if err != nil {
			// A repeat slug legitimately collides; only the first of a pair
			// matters for this property.
			continue
		}
		if strings.ContainsAny(ws.ID, `/\ `) || strings.Contains(ws.ID, "..") {
			t.Errorf("Slug %q produced id %q, which is not a safe path component", in, ws.ID)
		}
		if !strings.HasPrefix(filepath.Clean(ws.Dir), filepath.Clean(h)) {
			t.Errorf("Slug %q escaped ORION_HOME: %s", in, ws.Dir)
		}
	}
}

// docs/decisions/0012: a tracker project gets ONE workspace. A second call
// refuses -- it does not reuse the existing one (that is the contamination
// workspace.New's original comment warned about) and it does not suffix a
// second one (that breaks 0009's single name).
func TestASecondCallOnTheSameCanonicalSlugRefuses(t *testing.T) {
	home(t)
	first, err := New(NewOptions{Idea: "Take payments", Slug: "orion-payments"})
	if err != nil {
		t.Fatal(err)
	}
	// Something the first run left behind. If the second call reused the
	// workspace, this state would carry into it.
	marker := filepath.Join(first.Dir, ".orion", "state", "half-finished")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := New(NewOptions{Idea: "Take payments", Slug: "orion-payments"})
	if err == nil {
		t.Fatalf("second call returned workspace %q; it must refuse", second.ID)
	}
	if !strings.Contains(err.Error(), "orion rm") {
		t.Errorf("refusal %q does not say how to start over; a refusal with no way "+
			"forward is a dead end", err)
	}

	ids, err := IDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Errorf("workspaces = %v, want exactly one: a suffixed twin would mean two "+
			"filesystem names for one tracker project", ids)
	}
}

// The original contract still holds where it was written for. An idea is free
// text and two typings of it are two attempts, so nothing about naming a
// workspace after a project may make `orion new` idempotent by accident.
func TestWithoutACanonicalSlugTwoIdenticalIdeasStillGetTwoWorkspaces(t *testing.T) {
	home(t)
	a, err := New(NewOptions{Idea: "Customers should see claim status"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(NewOptions{Idea: "Customers should see claim status"})
	if err != nil {
		t.Fatalf("second identical idea was refused: %v", err)
	}
	if a.ID == b.ID {
		t.Errorf("both ideas got %q; one task's failed state can now reach the other", a.ID)
	}
}
