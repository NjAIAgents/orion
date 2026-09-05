package decompose

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

const collidingTasks = `# Tasks: Release tooling

## Phase 3: User Story 1 - Cut the release (Priority: P1)
**Goal**: one command cuts a release

- [ ] T001 [P] Add the tag step to scripts/release.sh
- [ ] T002 Read the version from internal/update/version.go

## Phase 4: User Story 2 - Verify the release (Priority: P2)
**Goal**: the release is checked before it ships

- [ ] T003 Add the checksum step to scripts/release.sh
`

func treeFor(t *testing.T, text string) *Tree {
	t.Helper()
	tree, err := Parse(text, "specs/003-release/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func storyNamed(t *testing.T, tree *Tree, id string) *Item {
	t.Helper()
	for _, c := range tree.Epic.Children {
		if c.Kind == KindStory && strings.HasPrefix(c.Summary, id) {
			return c
		}
	}
	t.Fatalf("no story %s in the tree", id)
	return nil
}

// A story is the unit an agent claims and works in one branch, so the story is
// the level a batch collides at -- and therefore the level a declared scope
// has to exist at. Written in the one spelling the queue reads back.
func TestAStoryDeclaresTheUnionOfItsTasksScopes(t *testing.T) {
	us1 := storyNamed(t, treeFor(t, collidingTasks), "US1")

	want := []string{"scripts/release.sh", "internal/update/version.go"}
	if len(us1.Paths) != 2 || us1.Paths[0] != want[0] || us1.Paths[1] != want[1] {
		t.Fatalf("US1 declares %v, want %v", us1.Paths, want)
	}
	// The contract is the line, not the field: the queue reads a description.
	got := tracker.DeclaredScope(us1.Body)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("the story body declares %v to the tracker, want %v\n%s", got, want, us1.Body)
	}
}

// Absent means unknown, and unknown must never read as "touches nothing". A
// story whose tasks named no files writes no line at all.
func TestAStoryWithNoPathsWritesNoDeclaration(t *testing.T) {
	tree := treeFor(t, "# Tasks: Vague\n\n## Phase 3: User Story 1 - Think about it\n\n- [ ] T001 Consider the options\n")
	us1 := storyNamed(t, tree, "US1")

	if strings.Contains(us1.Body, "Files:") {
		t.Fatalf("a story with nothing to declare wrote a declaration:\n%s", us1.Body)
	}
	if got := tracker.DeclaredScope(us1.Body); len(got) != 0 {
		t.Errorf("read %v back off a story that declared nothing", got)
	}
}

// A tracker tree whose siblings collide is a tree that will batch badly for
// its whole life, and nothing downstream can undo a decomposition. So it is
// said here, on both records, before anything is created.
func TestCollidingSiblingsAreDeclaredCoupledOnBothRecords(t *testing.T) {
	tree := treeFor(t, collidingTasks)

	if len(tree.Coupled) != 1 {
		t.Fatalf("found %d couplings, want 1: US1 and US2 both edit scripts/release.sh",
			len(tree.Coupled))
	}
	c := tree.Coupled[0]
	if len(c.Shared) != 1 || c.Shared[0] != "scripts/release.sh" {
		t.Errorf("shared ground = %v, want [scripts/release.sh]", c.Shared)
	}
	for _, id := range []string{"US1", "US2"} {
		body := storyNamed(t, tree, id).Body
		if !strings.Contains(body, "Coupled with") {
			t.Errorf("%s does not say it is coupled:\n%s", id, body)
		}
		if !strings.Contains(body, "scripts/release.sh") {
			t.Errorf("%s does not name the shared ground:\n%s", id, body)
		}
	}
}

// Independent siblings must stay silent. A warning on every tree is a warning
// nobody reads.
func TestIndependentSiblingsAreNotReportedAsCoupled(t *testing.T) {
	tree := treeFor(t, `# Tasks: Two things

## Phase 3: User Story 1 - One
- [ ] T001 Change scripts/release.sh

## Phase 4: User Story 2 - Two
- [ ] T002 Change internal/update/version.go
`)
	if len(tree.Coupled) != 0 {
		t.Fatalf("reported %v as coupled; they share nothing", tree.Coupled)
	}
	if body := storyNamed(t, tree, "US1").Body; strings.Contains(body, "Coupled") {
		t.Errorf("an independent story claims a coupling:\n%s", body)
	}
}

// The preview is the last moment anyone can act on this: once these exist as
// siblings the queue refuses to admit them together for the rest of their
// lives.
func TestThePreviewNamesTheCoupledPairsBeforeAnythingIsCreated(t *testing.T) {
	tree := treeFor(t, collidingTasks)
	p, err := Build(tree, stubBackend{}, "OR")
	if err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	Preview(&b, p)
	out := b.String()

	for _, want := range []string{"coupled pair", "scripts/release.sh", "US1", "US2"} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview never mentions %q:\n%s", want, out)
		}
	}
}

// The coupling is written into the story's Body at Parse time, and Apply only
// ever copies Item.Body verbatim into the create request (internal/decompose/
// create.go) -- there is no path from tree.Coupled back into the body once it
// is written. Mutating tree.Coupled after the fact must not change what a
// story already says: the story that gets created is the one Parse wrote,
// not whatever tree.Coupled happens to hold at Apply time.
func TestACoupledStoryCannotBeUndiscoveredAfterTheTreeIsParsed(t *testing.T) {
	tree := treeFor(t, collidingTasks)
	us1 := storyNamed(t, tree, "US1")
	if !strings.Contains(us1.Body, "Coupled with") {
		t.Fatalf("US1's body does not record the coupling before the mutation:\n%s", us1.Body)
	}

	// Erase the tree's record of the coupling, as if a caller tried to
	// un-discover it after the fact.
	tree.Coupled = nil

	if !strings.Contains(us1.Body, "Coupled with") {
		t.Fatalf("clearing tree.Coupled erased the coupling already written into US1's body:\n%s",
			us1.Body)
	}

	// And Apply creates exactly that body: nothing re-derives it from
	// tree.Coupled at create time.
	p, err := Build(tree, stubBackend{}, "OR")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Steps {
		if s.Item == us1 && !strings.Contains(s.Item.Body, "Coupled with") {
			t.Fatalf("the plan's step for US1 lost the coupling: %s", s.Item.Body)
		}
	}
}

type stubBackend struct{}

func (stubBackend) Name() string { return "stub" }
func (stubBackend) Existing(string, string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (stubBackend) Create(CreateRequest) (string, error) { return "OR-1", nil }
