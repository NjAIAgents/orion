package decompose

// The Jira backend: the only one shipped, and the reason this capability is
// opt-in.
//
// The hierarchy mapping is the whole content of this file, and it is real
// work rather than a field rename. Jira has three levels and two different
// parent relationships that use one field: an Epic sits at hierarchy level
// 1, a Story or Task at level 0, and a sub-task below both. So a task
// GROUPED UNDER A STORY becomes a sub-task, and a task in no story group --
// Setup, Foundational, Polish -- becomes a level-0 Task hanging directly off
// the Epic, because a sub-task cannot be a child of an Epic. Both end up
// navigable in Jira's own terms: the Epic's children are its stories and its
// ungrouped tasks, and a story's children are its tasks.
//
// Types are matched by Jira's HIERARCHY LEVEL, never by a hardcoded name. A
// project may call the bottom level "Sub-task" or "Subtask", and may have
// renamed any of the three; a mapping keyed on a name works on the instance
// it was written against and fails on the next one.

import (
	"fmt"
	"strings"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// JiraClient is the part of *tracker.Jira this needs. An interface so the
// mapping can be tested without an instance to talk to -- the mapping is
// where the bugs are, not the POST.
type JiraClient interface {
	Search(jql string, maxResults int) ([]tracker.Issue, error)
	IssueTypes(project string) ([]tracker.IssueType, error)
	CreateIssue(in tracker.NewIssue) (string, error)
}

// JiraBackend adapts the Jira client to Backend.
type JiraBackend struct {
	c JiraClient
	// types is read once per project and reused: it is per-project metadata
	// that cannot change mid-run, and one lookup per item would be one
	// round trip per item to answer the same question.
	types map[string][]tracker.IssueType
}

// NewJiraBackend wraps a Jira client.
func NewJiraBackend(c JiraClient) *JiraBackend {
	return &JiraBackend{c: c, types: map[string][]tracker.IssueType{}}
}

func (b *JiraBackend) Name() string { return "jira" }

// Existing finds what a previous run of the same task list created.
//
// By the identity label, not by summary text alone: two features decomposed
// into one project both have a T001, and a search that ignored the label
// would link this tree's task to the other feature's.
func (b *JiraBackend) Existing(project, identityLabel string) (map[string]string, error) {
	jql := tracker.JQLAnd(
		tracker.JQLEq("project", project),
		tracker.JQLEq("labels", identityLabel),
	) + " ORDER BY created ASC"

	issues, err := b.c.Search(jql, 100)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(issues))
	for _, is := range issues {
		// First writer wins. If a project somehow holds two issues with the
		// same summary and label, linking to the older one keeps a re-run
		// stable rather than moving the tree from run to run.
		if _, dup := out[is.Summary]; !dup {
			out[is.Summary] = is.Key
		}
	}
	return out, nil
}

// Create maps one neutral item onto a Jira issue and creates it.
func (b *JiraBackend) Create(req CreateRequest) (string, error) {
	types, err := b.typesFor(req.Project)
	if err != nil {
		return "", err
	}
	t, err := jiraType(types, req.Kind, req.ParentKind, req.Project)
	if err != nil {
		return "", err
	}
	return b.c.CreateIssue(tracker.NewIssue{
		Project:     req.Project,
		TypeID:      t.ID,
		Summary:     req.Summary,
		Description: req.Body,
		Labels:      req.Labels,
		ParentKey:   req.Parent,
	})
}

func (b *JiraBackend) typesFor(project string) ([]tracker.IssueType, error) {
	if ts, ok := b.types[project]; ok {
		return ts, nil
	}
	ts, err := b.c.IssueTypes(project)
	if err != nil {
		return nil, err
	}
	b.types[project] = ts
	return ts, nil
}

// jiraType picks the issue type for one item.
func jiraType(types []tracker.IssueType, kind, parentKind Kind, project string) (tracker.IssueType, error) {
	switch kind {
	case KindEpic:
		if t, ok := byLevel(types, 1); ok {
			return t, nil
		}
		if t, ok := byName(types, "epic"); ok {
			return t, nil
		}
		return tracker.IssueType{}, fmt.Errorf(
			"%s has no epic-level issue type, so the tree has no root to hang off.\n"+
				"  It offers: %s", project, names(types))

	case KindStory:
		if t, ok := byName(types, "story"); ok && !t.Subtask {
			return t, nil
		}
		if t, ok := byLevel(types, 0); ok {
			return t, nil
		}
		return tracker.IssueType{}, fmt.Errorf(
			"%s has no story-level issue type.\n  It offers: %s", project, names(types))

	case KindTask:
		if parentKind == KindStory {
			// A task grouped under a story is a sub-task, because that is
			// the only Jira level that can take a Story as its parent. A
			// project with no sub-task type cannot express this tree, and
			// saying so beats creating a level-0 Task that Jira will either
			// refuse the parent on or silently leave detached.
			if t, ok := subtask(types); ok {
				return t, nil
			}
			return tracker.IssueType{}, fmt.Errorf(
				"%s has no sub-task issue type, so a task cannot be a child of a story there.\n"+
					"  It offers: %s", project, names(types))
		}
		// A task in no story group hangs off the epic, where level 0 is the
		// child level.
		if t, ok := byName(types, "task"); ok && !t.Subtask {
			return t, nil
		}
		if t, ok := byLevel(types, 0); ok {
			return t, nil
		}
		return tracker.IssueType{}, fmt.Errorf(
			"%s has no task-level issue type.\n  It offers: %s", project, names(types))
	}
	return tracker.IssueType{}, fmt.Errorf("unknown item kind %q", kind)
}

func byLevel(types []tracker.IssueType, level int) (tracker.IssueType, bool) {
	for _, t := range types {
		if t.HierarchyLevel == level && !t.Subtask {
			return t, true
		}
	}
	return tracker.IssueType{}, false
}

func byName(types []tracker.IssueType, name string) (tracker.IssueType, bool) {
	for _, t := range types {
		if strings.EqualFold(strings.TrimSpace(t.Name), name) {
			return t, true
		}
	}
	return tracker.IssueType{}, false
}

func subtask(types []tracker.IssueType) (tracker.IssueType, bool) {
	for _, t := range types {
		if t.Subtask || t.HierarchyLevel < 0 {
			return t, true
		}
	}
	return tracker.IssueType{}, false
}

func names(types []tracker.IssueType) string {
	var out []string
	for _, t := range types {
		out = append(out, t.Name)
	}
	return strings.Join(out, ", ")
}
