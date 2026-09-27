package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OR-542: a re-run with nothing new still applies the tree's ordering links,
// which is how a rule added later (OR-540's phase order) reaches tickets that
// already exist. It used to stop at "nothing to do" before any link.
func TestARerunWithNothingNewStillAppliesTheLinks(t *testing.T) {
	f := swapBackend(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.md")
	src := "# Tasks: Thing\n\n## Phase 1: Setup\n\n- [ ] T001 Do a in a.go\n- [ ] T002 Do b in b.go\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	yes := func(string) bool { return true }
	if err := decomposeTree(&bytes.Buffer{}, dir, "CAT", path, yes); err != nil {
		t.Fatal(err)
	}
	created := len(f.created)
	f.links = nil

	var asked []string
	ask := func(p string) bool { asked = append(asked, p); return true }
	var out bytes.Buffer
	if err := decomposeTree(&out, dir, "CAT", path, ask); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != created {
		t.Errorf("a re-run created %d more items", len(f.created)-created)
	}
	if len(f.links) == 0 {
		t.Errorf("a re-run applied no ordering links:\n%s", out.String())
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "Apply the") {
		t.Errorf("the re-run did not ask once, about links: %v", asked)
	}
}

// A tree with no links and nothing new is still nothing to do, and asks nothing.
func TestNothingNewAndNoLinksIsNothingToDo(t *testing.T) {
	swapBackend(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.md")
	src := "# Tasks: Thing\n\n## Phase 1: Setup\n\n- [ ] T001 [P] Do a in a.go\n- [ ] T002 [P] Do b in b.go\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	yes := func(string) bool { return true }
	_ = decomposeTree(&bytes.Buffer{}, dir, "CAT", path, yes)
	asked := 0
	var out bytes.Buffer
	if err := decomposeTree(&out, dir, "CAT", path, func(string) bool { asked++; return true }); err != nil {
		t.Fatal(err)
	}
	if asked != 0 || !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("asked %d time(s); output:\n%s", asked, out.String())
	}
}
