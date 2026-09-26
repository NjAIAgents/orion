package decompose

// OR-486..OR-489: the four ways a tasks.md turned into a tracker tree whose
// ordering the queue could not enforce. Found on log-triage-agent, 2026-09-26.

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

const orderingList = `# Tasks: Thing

## Phase 1: Setup

- [x] T001 Decide the version in pyproject.toml
- [ ] T002 Create pyproject.toml
- [ ] T003 Create src/thing/__init__.py

## Phase 2: Foundational

- [ ] T004 Implement src/thing/model.py
- [ ] T005 Implement src/thing/errors.py

## Phase 3: User Story 1 — Do the thing (Priority: P1)

- [ ] T006 [US1] Write tests/test_one.py
- [ ] T007 [US1] Implement src/thing/one.py

## Phase 4: User Story 2 — Do the other thing (Priority: P2)

- [ ] T008 [US2] Write tests/test_two.py
- [ ] T009 [US2] Implement src/thing/two.py

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: no dependencies.
- **Phase 2 (Foundational)**: %s
- **Phase 3 (US1)**: after T005.
- **Phase 4 (US2)**: after Phase 3.
`

func parseOrdering(t *testing.T, phase2 string) *Tree {
	t.Helper()
	tr, err := Parse(strings.Replace(orderingList, "%s", phase2, 1), "specs/001-thing/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func blockersOf(tr *Tree, id string) []string {
	var out []string
	for _, e := range tr.Blocks {
		if e.Blocked == id {
			out = append(out, e.Blocker)
		}
	}
	slices.Sort(out)
	return out
}

// OR-487: "needs" (and friends) read the same as "after".
func TestNeedsReadsLikeAfter(t *testing.T) {
	for _, w := range []string{"after Phase 1.", "needs Phase 1.", "requires Phase 1.", "depends on Phase 1.", "blocked by Phase 1."} {
		tr := parseOrdering(t, w)
		if got := blockersOf(tr, "T005"); !slices.Equal(got, []string{"T003"}) {
			t.Errorf("%q: T005 blocked by %v, want [T003]", w, got)
		}
	}
}

// OR-488: "after Phase N" blocks EVERY task of the phase, not only its first.
func TestAfterPhaseBlocksTheWholePhase(t *testing.T) {
	tr := parseOrdering(t, "after Phase 1.")
	for _, id := range []string{"T004", "T005"} {
		if got := blockersOf(tr, id); !slices.Equal(got, []string{"T003"}) {
			t.Errorf("%s blocked by %v, want [T003] (the whole phase waits)", id, got)
		}
	}
	for _, id := range []string{"T008", "T009"} {
		if got := blockersOf(tr, id); !slices.Equal(got, []string{"T007"}) {
			t.Errorf("%s blocked by %v, want [T007]", id, got)
		}
	}
}

// OR-486: a done task is never a blocker or blocked, and a phase's "last
// task" is its last OPEN one.
func TestDoneTasksAreNeverInALink(t *testing.T) {
	tr := parseOrdering(t, "after T001.")
	for _, e := range tr.Blocks {
		if e.Blocker == "T001" || e.Blocked == "T001" {
			t.Errorf("a done task is in a link: %s blocks %s", e.Blocker, e.Blocked)
		}
	}
	if !slices.Equal(tr.DoneTasks, []string{"T001"}) {
		t.Errorf("DoneTasks = %v, want [T001]", tr.DoneTasks)
	}
}

// OR-486: done tasks are created (OR-302 keeps them in the tracker), never
// labelled for the queue, and closed afterwards.
func TestDoneTasksAreCreatedThenClosed(t *testing.T) {
	tr := parseOrdering(t, "after Phase 1.")
	var done *Item
	_ = tr.Walk(func(it, _ *Item) error {
		if it.ID == "T001" {
			done = it
		}
		return nil
	})
	if done == nil || !done.Done {
		t.Fatal("T001 missing or not marked done")
	}
	for _, l := range done.Labels {
		if l != tr.Label() {
			t.Errorf("a done task carries a queue label %q", l)
		}
	}

	f := &closingFake{fake: newFake()}
	p, err := Build(tr, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(p, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Closed) != 1 || len(f.closed) != 1 {
		t.Errorf("want T001 created and closed; Closed=%v closedCalls=%v", res.Closed, f.closed)
	}
}

// A backend that cannot close still creates the task, and says it stayed open.
func TestDoneTaskWithoutACloserIsReportedOpen(t *testing.T) {
	tr := parseOrdering(t, "after Phase 1.")
	f := newFake()
	p, _ := Build(tr, f, "CAT")
	res, err := Apply(p, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CloseFailed) != 1 {
		t.Errorf("want the done task reported as left open, got %v", res.CloseFailed)
	}
}

// OR-489: a link into a story's task also blocks the story -- the queue
// dispatches stories and reads only their own links.
func TestLinksRollUpToTheStory(t *testing.T) {
	tr := parseOrdering(t, "after Phase 1.")
	if got := blockersOf(tr, "US1"); !slices.Equal(got, []string{"T005"}) {
		t.Errorf("US1 blocked by %v, want [T005]", got)
	}
	if got := blockersOf(tr, "US2"); !slices.Equal(got, []string{"T007"}) {
		t.Errorf("US2 blocked by %v, want [T007]", got)
	}
	// Not by its own task: US1's T006 -> T007 would be inside the story.
	for _, e := range tr.Blocks {
		if e.Blocked == "US1" && (e.Blocker == "T006" || e.Blocker == "T007") {
			t.Errorf("a story is blocked by its own task %s", e.Blocker)
		}
	}
}

// OR-487: bullets that name phases but yield no edge are reported as
// unread, not as "states no dependencies".
func TestPreviewSaysWhenDependenciesWereNotRead(t *testing.T) {
	src := strings.Replace(strings.Replace(orderingList, "%s", "once the model settles.", 1),
		"after T005.", "when ready.", 1)
	src = strings.Replace(src, "after Phase 3.", "later.", 1)
	tr, err := Parse(src, "specs/001-thing/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Blocks) != 0 {
		t.Fatalf("expected no edges from unreadable wording, got %v", tr.Blocks)
	}
	var out bytes.Buffer
	previewBlocks(&out, tr.Blocks, tr.DependencyLines)
	if !strings.Contains(out.String(), "READ") || strings.Contains(out.String(), "states no dependencies") {
		t.Errorf("preview misreports unread dependencies:\n%s", out.String())
	}
}

type closingFake struct {
	*fake
	closed []string
}

func (c *closingFake) Close(key string) error {
	c.closed = append(c.closed, key)
	return nil
}
