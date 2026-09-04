package decompose

import (
	"bytes"
	"strings"
	"testing"
)

// File-path extraction: separators+extension, a path wrapped in backticks, a
// bare well-known manifest with no directory, no false positive from plain
// prose, and no duplicate when a task line names the same path twice.
func TestParseExtractsFilePaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		desc string
		want []string
	}{
		{
			name: "separator and extension",
			desc: "Implement listing in internal/catalogue/list.go",
			want: []string{"internal/catalogue/list.go"},
		},
		{
			name: "backtick-wrapped path",
			desc: "Add the handler in `internal/api/handler.go`",
			want: []string{"internal/api/handler.go"},
		},
		{
			name: "bare well-known manifest",
			desc: "Initialise the module with dependencies in go.mod",
			want: []string{"go.mod"},
		},
		{
			name: "bare well-known manifest, package.json",
			desc: "Add the build script to package.json",
			want: []string{"package.json"},
		},
		{
			name: "no false positive from prose with a slash and no path",
			desc: "Support 10/20 requests per second without a path in sight",
			want: nil,
		},
		{
			name: "no duplicate when the same path is named twice",
			desc: "Update internal/catalogue/list.go and re-test internal/catalogue/list.go",
			want: []string{"internal/catalogue/list.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := "# Tasks: Paths\n\n## Phase 1: Setup\n\n- [ ] T001 " + tc.desc + "\n"
			tree, err := Parse(in, "specs/001-paths/tasks.md")
			if err != nil {
				t.Fatal(err)
			}
			task := find(t, tree, "T001")
			if len(task.Paths) != len(tc.want) {
				t.Fatalf("paths = %v, want %v", task.Paths, tc.want)
			}
			for i, p := range tc.want {
				if task.Paths[i] != p {
					t.Errorf("paths[%d] = %q, want %q (from %q)", i, task.Paths[i], p, tc.desc)
				}
			}
		})
	}
}

// An empty task list is a valid input, not a crash: it parses to a lone epic
// named after the feature directory, since there is no `# Tasks:` heading to
// read a name from.
func TestParseEmptyTaskListIsALoneEpic(t *testing.T) {
	tree, err := Parse("", "specs/009-empty-feature/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Epic.Summary != "empty feature" {
		t.Errorf("epic summary = %q, want the feature directory name", tree.Epic.Summary)
	}
	if tree.Count() != 1 {
		t.Errorf("an empty task list must parse to the epic alone, got %d items", tree.Count())
	}
}

// A phase section with no task lines under it contributes nothing to the
// tree, while the phases around it still parse -- the same shape as the
// fixture's own "Phase 2: Foundational", asserted directly here so the case
// stands on its own rather than only being implied by the whole-tree count.
func TestParseSkipsAPhaseWithNoTasks(t *testing.T) {
	in := "# Tasks: Gap\n\n" +
		"## Phase 1: Setup\n\n- [ ] T001 Scaffold the project in main.go\n\n" +
		"## Phase 2: Nothing Here\n\n**Purpose**: reserved for later\n\n" +
		"## Phase 3: Wrap-up\n\n- [ ] T002 Finish up in main.go\n"
	tree, err := Parse(in, "specs/010-gap/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Count() != 3 {
		t.Fatalf("want the epic plus T001 and T002 only, got %d items", tree.Count())
	}
	t001 := find(t, tree, "T001")
	if t001.Phase != "Phase 1: Setup" {
		t.Errorf("T001 phase = %q, want Phase 1", t001.Phase)
	}
	t002 := find(t, tree, "T002")
	if t002.Phase != "Phase 3: Wrap-up" {
		t.Errorf("T002 phase = %q: the empty phase between them must not bleed into it", t002.Phase)
	}
}

// The identity label is capped at 40 characters so it stays readable on a
// board, and it is lowercase with hyphens even when the feature name is not.
func TestSlugIsLowercaseHyphenatedAndCappedAtFortyChars(t *testing.T) {
	name := "This Is A Genuinely Very Long Feature Name That Runs On And On"
	tree, err := Parse("# Tasks: "+name+"\n\n- [ ] T001 Do it in a.go\n", "specs/011-long/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(tree.Slug) != tree.Slug {
		t.Errorf("slug %q is not lowercase", tree.Slug)
	}
	if strings.Contains(tree.Slug, " ") {
		t.Errorf("slug %q must use hyphens rather than spaces", tree.Slug)
	}
	if len(tree.Slug) > 40 {
		t.Errorf("slug %q is %d chars, want at most 40 so the label stays readable on a board",
			tree.Slug, len(tree.Slug))
	}
	if strings.HasPrefix(tree.Slug, "-") || strings.HasSuffix(tree.Slug, "-") {
		t.Errorf("slug %q must not truncate onto a trailing hyphen", tree.Slug)
	}
	if !strings.HasPrefix(tree.Label(), "orion-spec-") {
		t.Errorf("label %q must carry the orion-spec- prefix", tree.Label())
	}
}

// A "Dependencies" section is recognised only by its own heading, matched
// case-insensitively, and nowhere else: the word appearing in a task's own
// description must not be swept onto the epic as if it were the artifact's
// stated execution order.
func TestParseRecognisesDependenciesOnlyUnderItsOwnHeadingCaseInsensitively(t *testing.T) {
	in := "# Tasks: Deps\n\n" +
		"## Phase 1: Setup\n\n" +
		"- [ ] T001 Handle dependencies between services in internal/deps/wire.go\n\n" +
		"## dependencies\n\n" +
		"- T001 blocks T002\n"
	tree, err := Parse(in, "specs/005-deps/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tree.Epic.Body, "T001 blocks T002") {
		t.Errorf("a lowercase ## dependencies heading must still be recognised:\n%s", tree.Epic.Body)
	}
	t001 := find(t, tree, "T001")
	if strings.Contains(t001.Body, "T001 blocks T002") {
		t.Errorf("the Dependencies section content leaked into T001's own body:\n%s", t001.Body)
	}
}

// The word "dependencies" said in prose, under a heading that is not itself
// named Dependencies, states nothing about execution order and must not
// appear on the epic as if it did.
func TestParseDoesNotTreatTheWordDependenciesElsewhereAsTheDependenciesSection(t *testing.T) {
	in := "# Tasks: Deps\n\n" +
		"## Phase 1: Setup\n\n" +
		"**Purpose**: note that dependencies between T001 and T002 exist\n\n" +
		"- [ ] T001 Wire the module in internal/deps/wire.go\n"
	tree, err := Parse(in, "specs/006-deps-elsewhere/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tree.Epic.Body, "Dependencies and execution order") {
		t.Errorf("no ## Dependencies heading was present; the epic must not claim a stated\n"+
			"execution order:\n%s", tree.Epic.Body)
	}
}

// The checkbox is required, not decorative: a line naming a T-id with no
// `- [ ]` / `- [x]` marker is not a task line at all, the same rule that
// keeps the template's own "## Format" bullets out of the tree.
func TestParseRequiresTheCheckboxOrTheLineIsNotATask(t *testing.T) {
	in := "# Tasks: NoBox\n\n" +
		"## Phase 1: Setup\n\n" +
		"- T001 Looks like a task but has no checkbox in a.go\n" +
		"- [ ] T002 A real task in b.go\n"
	tree, err := Parse(in, "specs/008-nobox/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Count() != 2 {
		t.Fatalf("want the epic plus T002 only, got %d items", tree.Count())
	}
	_ = tree.Walk(func(it, _ *Item) error {
		if it.ID == "T001" {
			t.Error("T001 has no checkbox and must not have parsed as a task")
		}
		return nil
	})
	find(t, tree, "T002")
}

// [P] is recognised anywhere in the remainder of the line, but the marker
// vocabulary is exact: [p] and other case variants are not the same token
// and must not be read as parallel.
func TestParallelMarkerIsCaseSensitive(t *testing.T) {
	in := "# Tasks: Case\n\n## Phase 1: Setup\n\n" +
		"- [ ] T001 [p] Lowercase marker must not count in a.go\n" +
		"- [ ] T002 [P] Uppercase marker in b.go\n"
	tree, err := Parse(in, "specs/012-case/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if find(t, tree, "T001").Parallel {
		t.Error("[p] (lowercase) must not be read as the [P] parallel marker")
	}
	if !find(t, tree, "T002").Parallel {
		t.Error("[P] (uppercase) must be read as the parallel marker")
	}
	// The unmatched [p] text stays in the description; it is not silently
	// dropped as if it had been understood.
	if !strings.Contains(find(t, tree, "T001").Summary, "[p]") {
		t.Errorf("summary = %q, want the literal [p] preserved since it was not\n"+
			"recognised as a marker", find(t, tree, "T001").Summary)
	}
}

// [USn] is recognised anywhere in the remainder of the line, but is matched
// on the exact uppercase token: [us5] must not group a task under a story.
func TestStoryMarkerIsCaseSensitive(t *testing.T) {
	in := "# Tasks: Case\n\n## Phase 1: Setup\n\n" +
		"- [ ] T001 [us5] Lowercase tag must not group in a.go\n"
	tree, err := Parse(in, "specs/013-case/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	t001 := find(t, tree, "T001")
	if t001.Kind != KindTask {
		t.Fatalf("T001 should still parse as a task, got %v", t001.Kind)
	}
	for _, c := range tree.Epic.Children {
		if c.Kind == KindStory {
			t.Errorf("a story %q was created from a lowercase [us5] tag, which must not match", c.Summary)
		}
	}
	if !strings.Contains(t001.Summary, "[us5]") {
		t.Errorf("summary = %q, want the literal [us5] preserved since it was not\n"+
			"recognised as a story tag", t001.Summary)
	}
}

// Preview only writes what Build already resolved; it takes no backend and
// must not be able to cause a create even indirectly.
func TestPreviewCreatesNothing(t *testing.T) {
	f := newFake()
	tree := parseFixture(t)
	p, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	Preview(&out, p)

	if len(f.calls) != 0 {
		t.Fatalf("Preview must be read-only: %d creates happened while only previewing", len(f.calls))
	}
	if out.Len() == 0 {
		t.Fatal("Preview wrote nothing")
	}
}
