package decompose

import (
	"strings"
	"testing"
)

// A real /speckit.tasks output, trimmed to the shapes that matter: the
// template's own "## Format" section (whose bullets look like task lines and
// are not), a Setup phase whose tasks carry no story group, two [USn]
// groups, a [P] marker, exact file paths, a phase with no tasks at all, and
// the Dependencies section.
const speckitTasks = "" +
	"---\n" +
	"description: \"Task list for feature implementation\"\n" +
	"---\n" +
	"\n" +
	"# Tasks: Product Catalogue\n" +
	"\n" +
	"**Input**: Design documents from `/specs/003-product-catalogue/`\n" +
	"\n" +
	"## Format: `[ID] [P?] [Story] Description`\n" +
	"\n" +
	"- **[P]**: Can run in parallel (different files, no dependencies)\n" +
	"- **[Story]**: Which user story this task belongs to\n" +
	"- Include exact file paths in descriptions\n" +
	"\n" +
	"## Phase 1: Setup (Shared Infrastructure)\n" +
	"\n" +
	"**Purpose**: Project initialisation\n" +
	"\n" +
	"- [ ] T001 Create the project structure per implementation plan\n" +
	"- [ ] T002 [P] Initialise the module with dependencies in go.mod\n" +
	"\n" +
	"---\n" +
	"\n" +
	"## Phase 2: Foundational (Blocking Prerequisites)\n" +
	"\n" +
	"**Purpose**: nothing here yet; the plan folded this into Setup\n" +
	"\n" +
	"---\n" +
	"\n" +
	"## Phase 3: User Story 1 - Browse the catalogue (Priority: P1) 🎯 MVP\n" +
	"\n" +
	"**Goal**: A shopper can list and filter products.\n" +
	"\n" +
	"### Implementation for User Story 1\n" +
	"\n" +
	"- [ ] T003 [P] [US1] Create the Product model in internal/catalogue/product.go\n" +
	"- [ ] T004 [US1] Implement listing in internal/catalogue/list.go (depends on T003)\n" +
	"\n" +
	"**Checkpoint**: the catalogue lists.\n" +
	"\n" +
	"---\n" +
	"\n" +
	"## Phase 4: User Story 2 - Document the API (Priority: P2)\n" +
	"\n" +
	"**Goal**: The catalogue endpoints are documented.\n" +
	"\n" +
	"- [ ] T005 [P] [US2] Write the endpoint reference in docs/api/catalogue.md\n" +
	"\n" +
	"---\n" +
	"\n" +
	"## Dependencies & Execution Order\n" +
	"\n" +
	"- T003 blocks T004\n" +
	"- User Story 2 depends on User Story 1\n"

func parseFixture(t *testing.T) *Tree {
	t.Helper()
	tree, err := Parse(speckitTasks, "specs/003-product-catalogue/tasks.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return tree
}

// find returns the item whose summary starts with the given id.
func find(t *testing.T, tree *Tree, id string) *Item {
	t.Helper()
	var got *Item
	_ = tree.Walk(func(it, _ *Item) error {
		if it.ID == id {
			got = it
		}
		return nil
	})
	if got == nil {
		t.Fatalf("no item %q in the tree", id)
	}
	return got
}

func TestParseShapesTheTree(t *testing.T) {
	tree := parseFixture(t)

	if tree.Epic.Summary != "Product Catalogue" {
		t.Errorf("epic summary = %q, want the feature name from the `# Tasks:` heading", tree.Epic.Summary)
	}
	if tree.Slug != "product-catalogue" || tree.Label() != "orion-spec-product-catalogue" {
		t.Errorf("identity = %q / %q, and a re-run finds nothing without it", tree.Slug, tree.Label())
	}

	// One epic, two stories, and the two ungrouped Setup tasks as children of
	// the epic itself -- not of an invented "Misc" story.
	var stories, epicTasks []*Item
	for _, c := range tree.Epic.Children {
		switch c.Kind {
		case KindStory:
			stories = append(stories, c)
		case KindTask:
			epicTasks = append(epicTasks, c)
		}
	}
	if len(stories) != 2 {
		t.Fatalf("got %d stories, want one per [USn] group", len(stories))
	}
	if len(epicTasks) != 2 {
		t.Fatalf("got %d ungrouped tasks under the epic, want T001 and T002", len(epicTasks))
	}
	if epicTasks[0].ID != "T001" || epicTasks[1].ID != "T002" {
		t.Errorf("ungrouped tasks are %q, %q: source order is the execution order",
			epicTasks[0].ID, epicTasks[1].ID)
	}
	if len(stories[0].Children) != 2 || len(stories[1].Children) != 1 {
		t.Errorf("story task counts = %d, %d; want the [USn] grouping",
			len(stories[0].Children), len(stories[1].Children))
	}
	if got := stories[0].Summary; got != "US1 Browse the catalogue" {
		t.Errorf("story summary = %q: the phase heading names the story, without its priority decoration", got)
	}
	if !strings.Contains(stories[0].Body, "A shopper can list and filter products.") {
		t.Errorf("the story's Goal line is its description, and it was lost:\n%s", stories[0].Body)
	}

	// The template's own "## Format" bullets are not tasks.
	if n := tree.Count(); n != 8 {
		t.Errorf("tree holds %d items, want 8 (epic + 2 stories + 5 tasks); the template's\n"+
			"own format bullets must not parse as tasks", n)
	}
}

func TestParseKeepsWhatMakesTheArtifactBetterThanAList(t *testing.T) {
	tree := parseFixture(t)

	t003 := find(t, tree, "T003")
	if !t003.Parallel {
		t.Error("T003 carries [P] and the marker was dropped")
	}
	if want := "internal/catalogue/product.go"; len(t003.Paths) != 1 || t003.Paths[0] != want {
		t.Errorf("T003 paths = %v, want the exact path from the task line", t003.Paths)
	}
	if !strings.Contains(t003.Body, "internal/catalogue/product.go") {
		t.Errorf("the file path must survive into the description:\n%s", t003.Body)
	}
	if !strings.Contains(t003.Body, "[P]") {
		t.Errorf("the parallel marker must survive into the description:\n%s", t003.Body)
	}
	if !strings.Contains(t003.Body, "User Story 1") {
		t.Errorf("the phase is the dependency order and must survive:\n%s", t003.Body)
	}
	if t003.Summary != "T003 Create the Product model in internal/catalogue/product.go" {
		t.Errorf("summary = %q: it leads with the id because reconcile and the\n"+
			"artifact's own dependency lines both refer to tasks by it", t003.Summary)
	}

	t004 := find(t, tree, "T004")
	if t004.Parallel {
		t.Error("T004 carries no [P] and must not be marked parallel")
	}
	if !strings.Contains(t004.Body, "depends on T003") {
		t.Errorf("an inline dependency stated on the task line was lost:\n%s", t004.Body)
	}

	if !strings.Contains(tree.Epic.Body, "T003 blocks T004") {
		t.Errorf("the Dependencies section states the execution order and belongs on the epic:\n%s",
			tree.Epic.Body)
	}
	if !strings.Contains(tree.Epic.Body, "specs/003-product-catalogue/tasks.md") {
		t.Errorf("the epic must name where it came from:\n%s", tree.Epic.Body)
	}
}

// The routing marker is set here rather than asked for in a prompt, and it
// comes from the structural parts of a task -- its paths and its phase --
// never from prose.
func TestParseSetsTheRoutingMarkerFromThePublishedVocabulary(t *testing.T) {
	tree := parseFixture(t)

	docTask := find(t, tree, "T005")
	if !has(docTask.Labels, "documentation") {
		t.Errorf("T005 writes docs/api/catalogue.md and carries %v: an unmarked docs ticket\n"+
			"is worked by the backend developer and nothing reports the mistake", docTask.Labels)
	}
	us2 := find(t, tree, "US2")
	if !has(us2.Labels, "documentation") {
		t.Errorf("US2's tasks are all documentation, and the story is the unit an agent\n"+
			"claims, so the story must carry the marker too; got %v", us2.Labels)
	}

	codeTask := find(t, tree, "T003")
	if has(codeTask.Labels, "documentation") || len(codeTask.Labels) != 1 {
		t.Errorf("T003 touches internal/catalogue and must take the default actor; got %v",
			codeTask.Labels)
	}
	if !has(codeTask.Labels, tree.Label()) {
		t.Errorf("every item carries the identity label or a re-run cannot find it; got %v",
			codeTask.Labels)
	}
	if len(tree.Epic.Labels) != 1 {
		t.Errorf("an epic is a container that nothing works, so it takes no routing marker; got %v",
			tree.Epic.Labels)
	}
}

func TestParseEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		src   string
		check func(t *testing.T, tree *Tree, err error)
	}{
		{
			name: "no heading falls back to the feature directory",
			in:   "- [ ] T001 Do the thing in main.go\n",
			src:  "specs/007-user-auth/tasks.md",
			check: func(t *testing.T, tree *Tree, err error) {
				if err != nil {
					t.Fatal(err)
				}
				if tree.Epic.Summary != "user auth" {
					t.Errorf("epic summary = %q, want the feature directory", tree.Epic.Summary)
				}
			},
		},
		{
			name: "no tasks at all is an empty epic, not a crash",
			in:   "# Tasks: Nothing\n\n## Phase 1: Setup\n\n**Purpose**: none\n",
			src:  "specs/001-nothing/tasks.md",
			check: func(t *testing.T, tree *Tree, err error) {
				if err != nil {
					t.Fatal(err)
				}
				if len(tree.Epic.Children) != 0 || tree.Count() != 1 {
					t.Errorf("want a lone epic, got %d items", tree.Count())
				}
			},
		},
		{
			name: "a story tag on the task line wins where there is no story phase",
			in:   "# Tasks: Tagged\n\n## Phase 2: Foundational\n\n- [ ] T001 [US3] Add the guard in a.go\n",
			src:  "specs/002-tagged/tasks.md",
			check: func(t *testing.T, tree *Tree, err error) {
				if err != nil {
					t.Fatal(err)
				}
				if len(tree.Epic.Children) != 1 || tree.Epic.Children[0].Kind != KindStory {
					t.Fatalf("want one story from the [US3] tag, got %+v", tree.Epic.Children)
				}
				st := tree.Epic.Children[0]
				if st.Summary != "US3 User Story 3" {
					t.Errorf("story with no naming heading = %q, want a stable fallback title", st.Summary)
				}
			},
		},
		{
			name: "a checked task is still a task",
			in:   "# Tasks: Resumed\n\n## Phase 1: Setup\n\n- [x] T001 Already done in a.go\n",
			src:  "specs/004-resumed/tasks.md",
			check: func(t *testing.T, tree *Tree, err error) {
				if err != nil {
					t.Fatal(err)
				}
				if tree.Count() != 2 {
					t.Errorf("a completed task still belongs in the tracker; got %d items", tree.Count())
				}
			},
		},
		{
			name: "nothing to name the feature after",
			in:   "- [ ] T001 Do a thing in a.go\n",
			src:  "tasks.md",
			check: func(t *testing.T, _ *Tree, err error) {
				if err == nil {
					t.Error("want an error rather than a tree named after nothing")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := Parse(tc.in, tc.src)
			tc.check(t, tree, err)
		})
	}
}

func has(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
