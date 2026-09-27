package decompose

import (
	"slices"
	"testing"
)

// OR-540: the shape log-triage-agent's Phase 2 had. [P] tasks may run
// together; a task without [P] runs in phase order. None of this became a
// link, so the whole phase was claimable at once and the tasks that import
// model.py and errors.py were written before either existed.
const phaseOrderList = `# Tasks: Thing

## Phase 2: Foundational

- [ ] T013 [P] Implement src/thing/model.py
- [ ] T014 [P] Implement src/thing/errors.py
- [ ] T015 Implement src/thing/config.py
- [ ] T016 [P] Add refusal rules in src/thing/config_rules.py
- [ ] T017 [P] Assert orion.json is never read in tests/test_config.py
- [x] T018 Decide the store backend
- [ ] T019 Implement src/thing/store.py

## Phase 3: User Story 1 — Do the thing (Priority: P1)

- [ ] T020 [US1] Write tests/test_one.py
- [ ] T021 [US1] Implement src/thing/one.py
`

func TestTasksWithoutParallelMarkerRunInPhaseOrder(t *testing.T) {
	tr, err := Parse(phaseOrderList, "specs/001-thing/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][]string{
		"T013": nil,              // first run of the phase
		"T014": nil,              // [P] alongside T013, not after it
		"T015": {"T013", "T014"}, // no [P]: waits for the whole run before it
		"T016": {"T015"},         // a new [P] run starts after T015
		"T017": {"T015"},
		"T019": {"T016", "T017"}, // the done T018 is skipped, not a gap
	} {
		if got := blockersOf(tr, id); !slices.Equal(got, want) {
			t.Errorf("%s blocked by %v, want %v", id, got, want)
		}
	}
}

// A story is claimed and worked as one unit, so its own tasks get no
// phase-order links: they would hold back nothing the queue dispatches.
func TestTasksInsideAStoryGetNoPhaseOrderLinks(t *testing.T) {
	tr, err := Parse(phaseOrderList, "specs/001-thing/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := blockersOf(tr, "T021"); len(got) != 0 {
		t.Errorf("a story's task was ordered against its sibling: T021 blocked by %v", got)
	}
}

// A done task is never in a link (OR-486), including the phase-order ones.
func TestADoneTaskIsNotInAPhaseOrderLink(t *testing.T) {
	tr, err := Parse(phaseOrderList, "specs/001-thing/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range tr.Blocks {
		if e.Blocker == "T018" || e.Blocked == "T018" {
			t.Errorf("done task in a link: %s -> %s", e.Blocker, e.Blocked)
		}
	}
}
