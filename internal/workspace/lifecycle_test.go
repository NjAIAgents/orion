package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("ORION_HOME", h)
	return h
}

func TestNewProvisionsTheFullLayout(t *testing.T) {
	home(t)
	ws, err := New(NewOptions{Idea: "Customers should see claim status"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ws.ID, "customers-should-see-claim-status-") {
		t.Errorf("id = %q; it should be readable, not opaque", ws.ID)
	}
	for _, d := range []string{"repo", "worktrees", ".orion/logs", ".orion/state"} {
		if fi, err := os.Stat(filepath.Join(ws.Dir, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s missing: %v", d, err)
		}
	}
	// The branch model must exist before any work, or a first commit lands
	// on the protected branch.
	if len(ws.Task.Branches) == 0 {
		t.Error("no branches recorded")
	}
	cur, _ := git(ws.RepoDir(), "branch", "--show-current")
	if strings.TrimSpace(cur) != "develop" {
		t.Errorf("provisioned onto %q, want develop", cur)
	}
	if _, err := os.Stat(ws.TaskPath()); err != nil {
		t.Errorf("task.json not written: %v", err)
	}
	if _, err := os.Stat(ws.SettingsPath()); err != nil {
		t.Errorf("settings not written: %v", err)
	}
}

func TestNewRequiresAnIdea(t *testing.T) {
	home(t)
	for _, idea := range []string{"", "   ", "\t\n"} {
		if _, err := New(NewOptions{Idea: idea}); err == nil {
			t.Errorf("New(%q) succeeded; an unnamed workspace cannot be found again", idea)
		}
	}
}

func TestOpenRoundTripsTheTask(t *testing.T) {
	home(t)
	ws, err := New(NewOptions{Idea: "a thing"})
	if err != nil {
		t.Fatal(err)
	}
	ws.Task.Stage = "plan"
	ws.Task.Status = "ready-for-review"
	if err := ws.SaveTask(); err != nil {
		t.Fatal(err)
	}

	got, err := Open(ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.Stage != "plan" || got.Task.Status != "ready-for-review" {
		t.Errorf("task = %+v; state must survive a reopen", got.Task)
	}
	if got.Dir != ws.Dir {
		t.Errorf("dir = %q, want %q", got.Dir, ws.Dir)
	}
}

// Two different states deserve two different messages. With nothing
// provisioned, naming the id the user typed is noise -- no id could have
// matched. Once workspaces exist, the id is exactly what they got wrong.
func TestOpenDistinguishesEmptyFromNotFound(t *testing.T) {
	home(t)

	_, err := Open("no-such-workspace")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no workspaces") {
		t.Errorf("with nothing provisioned the error should say so, got: %v", err)
	}

	if _, err := New(NewOptions{Idea: "something real"}); err != nil {
		t.Fatal(err)
	}
	_, err = Open("no-such-workspace")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no-such-workspace") {
		t.Errorf("with workspaces present the error should name the id, got: %v", err)
	}
}

// A prefix is what a person types; requiring the full id would mean copying
// it every time.
func TestOpenAcceptsAUniquePrefix(t *testing.T) {
	home(t)
	ws, _ := New(NewOptions{Idea: "unique thing here"})
	prefix := ws.ID[:8]

	got, err := Open(prefix)
	if err != nil {
		t.Fatalf("prefix %q did not resolve: %v", prefix, err)
	}
	if got.ID != ws.ID {
		t.Errorf("resolved to %q, want %q", got.ID, ws.ID)
	}
}

func TestIDsAndListSeeProvisionedWorkspaces(t *testing.T) {
	home(t)
	a, _ := New(NewOptions{Idea: "alpha idea"})
	b, _ := New(NewOptions{Idea: "beta idea"})

	ids, err := IDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want both", ids)
	}
	var out strings.Builder
	if err := List(&out); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if !strings.Contains(out.String(), id) {
			t.Errorf("List omitted %s:\n%s", id, out.String())
		}
	}
}

func TestListSaysSoWhenThereAreNone(t *testing.T) {
	home(t)
	var out strings.Builder
	if err := List(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "orion new") {
		t.Errorf("an empty list should say how to make one, got: %q", out.String())
	}
}

func TestStatusReportsStageAndPath(t *testing.T) {
	home(t)
	ws, _ := New(NewOptions{Idea: "status idea"})
	var out strings.Builder
	if err := Status(&out, ws.ID); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{ws.ID, "intent", ws.RepoDir()} {
		if !strings.Contains(s, want) {
			t.Errorf("status omitted %q:\n%s", want, s)
		}
	}
}

func TestPrintPathEmitsSomethingCdCanUse(t *testing.T) {
	home(t)
	ws, _ := New(NewOptions{Idea: "path idea"})
	var out strings.Builder
	if err := PrintPath(&out, ws.ID); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(out.String())
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("PrintPath gave %q, which is not a directory: %v", got, err)
	}
}

// Removal is destructive and cannot be undone, so it must be deliberate.
func TestRemoveNeedsForce(t *testing.T) {
	home(t)
	ws, _ := New(NewOptions{Idea: "remove me"})

	if err := Remove(ws.ID, false); err == nil {
		t.Fatal("removed a workspace without --force")
	}
	if _, err := os.Stat(ws.Dir); err != nil {
		t.Fatal("the workspace was deleted despite the refusal")
	}
	if err := Remove(ws.ID, true); err != nil {
		t.Fatalf("--force should remove it: %v", err)
	}
	if _, err := os.Stat(ws.Dir); !os.IsNotExist(err) {
		t.Error("still present after a forced removal")
	}
}

func TestSlugifyProducesUsableIdentifiers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Customers should see claim status", "customers-should-see-claim-status"},
		{"  Trim   me  ", "trim-me"},
		{"Weird!!! chars??? here", "weird-chars-here"},
		{"UPPER CASE", "upper-case"},
	} {
		got := Slugify(tc.in)
		if got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Never empty, and never a leading or trailing separator: the slug
	// becomes a directory name and a branch name.
	for _, in := range []string{"", "!!!", "---", "   "} {
		got := Slugify(in)
		if got == "" || strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("Slugify(%q) = %q is not a usable identifier", in, got)
		}
	}
	if len(Slugify(strings.Repeat("word ", 60))) > 80 {
		t.Error("slug is unbounded; it becomes a directory and branch name")
	}
}

func TestShortIDIsDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := shortID()
		if id == "" {
			t.Fatal("empty id")
		}
		if seen[id] {
			t.Fatalf("collision on %q within 200 draws", id)
		}
		seen[id] = true
	}
}

func TestSandboxModeReportsSomething(t *testing.T) {
	home(t)
	ws, _ := New(NewOptions{Idea: "sandbox idea"})
	if got := ws.SandboxMode(); strings.TrimSpace(got) == "" {
		t.Error("SandboxMode is empty; doctor and status both print it")
	}
}
