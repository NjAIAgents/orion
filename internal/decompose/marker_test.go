package decompose

import "testing"

// marker.go reads the routing marker from STRUCTURE -- directory components
// and the phase heading -- never from prose, and matches only against
// work.Rules()'s own published vocabulary. These tests exercise marker.go
// directly, at the level Parse's fixture test cannot reach: a base name that
// happens to spell a keyword, and a story whose tasks disagree.
func TestMarkerReadsDirectoryComponentsAndPhaseAgainstThePublishedVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name string
		item *Item
		want string
	}{
		{
			name: "a directory component matches the docs vocabulary",
			item: &Item{Paths: []string{"docs/api/catalogue.md"}},
			want: "documentation",
		},
		{
			name: "a phase heading matches the frontend vocabulary",
			item: &Item{Phase: "Phase 5: Frontend polish"},
			want: "ui",
		},
		{
			// internal/catalogue/docs.go sits nowhere near a docs/ directory;
			// only its base name spells the keyword. Directories are a
			// structural statement and a base name is prose that happens to
			// end in an extension -- see signals()'s own comment for why
			// matching on it would be the same mistake route.go warns
			// against, arriving through a path instead of a sentence.
			name: "a file's base name is prose, not structure, and must not match",
			item: &Item{Paths: []string{"internal/catalogue/docs.go"}},
			want: "",
		},
		{
			name: "neither the path nor the phase matches: no marker",
			item: &Item{Paths: []string{"internal/catalogue/list.go"}, Phase: "Phase 1: Setup"},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := marker(signals(tc.item)...); got != tc.want {
				t.Errorf("marker = %q, want %q", got, tc.want)
			}
		})
	}
}

// A directory that merely CONTAINS a keyword as a substring must not match:
// tokenSplit breaks text on non-alphanumeric characters only, so "guide"
// (which contains "ui") and "docsite" (which contains "docs") are each one
// whole token, neither of which equals the keyword it happens to contain.
// Matching on substring rather than whole token would route ticket after
// ticket to the wrong actor on the strength of a coincidence in a directory
// name.
func TestMarkerMatchesWholeTokensOnlyNeverASubstring(t *testing.T) {
	for _, tc := range []struct {
		name string
		item *Item
	}{
		{"guide contains ui but is not the token ui", &Item{Paths: []string{"internal/guide/notes.go"}}},
		{"docsite contains docs but is not the token docs", &Item{Paths: []string{"internal/docsite/index.go"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := marker(signals(tc.item)...); got != "" {
				t.Errorf("marker = %q, want unmatched: the keyword is a substring of the\n"+
					"directory, not the whole token", got)
			}
		})
	}
}

// A story is the unit an agent claims and works, so its marker is only
// trustworthy when every one of its tasks agrees on it. Tasks that disagree
// are mixed work, and the story must come out unmarked rather than routed to
// whichever task happened first.
func TestStoryMarkerIsTheAgreementOfItsTasksMarkers(t *testing.T) {
	agree := &Item{Kind: KindStory, Children: []*Item{
		{Paths: []string{"docs/a.md"}},
		{Paths: []string{"docs/b.md"}},
	}}
	if got := agreedMarker(agree); got != "documentation" {
		t.Errorf("agreedMarker = %q, want documentation when every task marker agrees", got)
	}

	disagree := &Item{Kind: KindStory, Children: []*Item{
		{Paths: []string{"docs/a.md"}},
		{Paths: []string{"internal/catalogue/list.go"}},
	}}
	if got := agreedMarker(disagree); got != "" {
		t.Errorf("agreedMarker = %q, want unmarked when the tasks disagree: mixed work sent to\n"+
			"one actor is wrong for the rest of it", got)
	}

	// A story whose tasks agree on having NO marker is still agreement, and
	// must stay unmarked rather than pick up a marker from nowhere.
	noneAgree := &Item{Kind: KindStory, Children: []*Item{
		{Paths: []string{"internal/catalogue/list.go"}},
		{Paths: []string{"internal/catalogue/product.go"}},
	}}
	if got := agreedMarker(noneAgree); got != "" {
		t.Errorf("agreedMarker = %q, want empty when every task agrees on carrying no marker", got)
	}
}

// label() puts the identity label on every item so a re-run can find it, and
// the routing marker only on the levels something actually works: a Task and
// a Story that agrees, never an Epic.
func TestLabelPutsIdentityOnEveryItemAndRoutingOnlyOnTasksAndStories(t *testing.T) {
	tree := &Tree{Slug: "widget", Epic: &Item{Kind: KindEpic, Children: []*Item{
		{Kind: KindStory, Children: []*Item{
			{Kind: KindTask, Paths: []string{"docs/a.md"}},
			{Kind: KindTask, Paths: []string{"docs/b.md"}},
		}},
		{Kind: KindTask, Paths: []string{"internal/catalogue/list.go"}},
	}}}
	label(tree)

	id := tree.Label()
	_ = tree.Walk(func(it, _ *Item) error {
		if !has(it.Labels, id) {
			t.Errorf("%s carries no identity label; a re-run cannot find it: %v", it.Kind, it.Labels)
		}
		return nil
	})

	epic := tree.Epic
	if len(epic.Labels) != 1 {
		t.Errorf("epic labels = %v, want the identity label only -- an epic is a container\n"+
			"that nothing works, so a marker on it would route nothing", epic.Labels)
	}

	story := epic.Children[0]
	if !has(story.Labels, "documentation") {
		t.Errorf("story labels = %v, want the documentation marker its tasks agree on", story.Labels)
	}

	task := story.Children[0]
	if !has(task.Labels, "documentation") {
		t.Errorf("task labels = %v, want the marker read from its own directory", task.Labels)
	}

	ungrouped := epic.Children[1]
	if has(ungrouped.Labels, "documentation") || len(ungrouped.Labels) != 1 {
		t.Errorf("ungrouped task labels = %v, want only the identity label -- it touches\n"+
			"internal/catalogue, not docs/", ungrouped.Labels)
	}
}

// The queue label lands on stories and epic-level tasks only: the levels
// watch.Queued admits once each (docs/decisions/0023).
func TestQueueLabelsStoriesAndEpicLevelTasksOnly(t *testing.T) {
	tree := &Tree{Slug: "widget", Epic: &Item{Kind: KindEpic, Children: []*Item{
		{Kind: KindStory, Children: []*Item{
			{Kind: KindTask, Paths: []string{"docs/a.md"}},
		}},
		{Kind: KindTask, Paths: []string{"go.mod"}},
	}}}
	label(tree)
	tree.Queue("ORION")

	epic := tree.Epic
	story, sub, setup := epic.Children[0], epic.Children[0].Children[0], epic.Children[1]
	if has(epic.Labels, "ORION") {
		t.Errorf("epic carries the queue label %v; it would become the one claimable unit", epic.Labels)
	}
	if !has(story.Labels, "ORION") {
		t.Errorf("story lacks the queue label: %v", story.Labels)
	}
	if has(sub.Labels, "ORION") {
		t.Errorf("a story's task carries the queue label %v; the story works it", sub.Labels)
	}
	if !has(setup.Labels, "ORION") {
		t.Errorf("epic-level task lacks the queue label: %v", setup.Labels)
	}
	// Empty is a no-op, never an empty label Jira would refuse.
	before := len(story.Labels)
	tree.Queue("  ")
	if len(story.Labels) != before {
		t.Error("an empty queue label was appended")
	}
}

// Work no agent can do carries no queue label: the ticket exists, and an
// agent never claims it only to discover it cannot register an OIDC client
// or sign an assessment.
func TestTheQueueLabelIsWithheldFromHumanWork(t *testing.T) {
	tree := &Tree{Slug: "widget", Epic: &Item{Kind: KindEpic, Children: []*Item{
		{Kind: KindStory, ID: "US1", Children: []*Item{
			{Kind: KindTask, ID: "T001", Paths: []string{"a.go"}},
			{Kind: KindTask, ID: "T002", Human: true, Paths: []string{"b.go"}},
		}},
		{Kind: KindTask, ID: "T080", Human: true, Paths: []string{"deploy/main.tf"}},
		{Kind: KindTask, ID: "T014", Paths: []string{"go.mod"}},
	}}}
	label(tree)
	tree.Queue("ORION")

	epic := tree.Epic
	story, setupHuman, setupAgent := epic.Children[0], epic.Children[1], epic.Children[2]
	if !has(story.Labels, "ORION") {
		t.Errorf("the story lost its queue label: %v", story.Labels)
	}
	if has(setupHuman.Labels, "ORION") {
		t.Errorf("T080 is human work and must not be claimable: %v", setupHuman.Labels)
	}
	if !has(setupAgent.Labels, "ORION") {
		t.Errorf("an ordinary epic-level task lost its queue label: %v", setupAgent.Labels)
	}
	// The identity label stays on everything: a re-run must still find it.
	for _, it := range []*Item{setupHuman, setupAgent, story} {
		if !has(it.Labels, tree.Label()) {
			t.Errorf("%s lost its identity label: %v", it.ID, it.Labels)
		}
	}
}
