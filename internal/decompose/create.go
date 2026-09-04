package decompose

// Creating the tree: search first, preview, then create parent-first.
//
// Nothing in this file names a tracker. Backend is the seam, and it is
// narrow on purpose -- two methods, both in the tree's own vocabulary -- so
// that when OR-303 lands its interface this becomes an adapter swap rather
// than a rewrite. What a backend has to do with a Kind is the backend's
// problem, and it is a real problem rather than a field rename: Jira calls
// the bottom level a Sub-task under a Story and a Task under an Epic, and
// GitHub has no Epic issue type at all.

import (
	"errors"
	"fmt"
	"strings"
)

// Backend is the tracker capability creating a tree needs.
type Backend interface {
	// Name is the tracker's own name, for the preview and the messages.
	Name() string

	// Existing maps the summary of every item already created from this
	// tree to its key in the tracker, found by the identity label.
	//
	// KEYED BY SUMMARY because the summary leads with the source artifact's
	// own id -- "T004 Create User model in src/models/user.py" -- and that
	// id is what makes a re-run able to tell the item it created last time
	// from a different item that happens to read similarly.
	Existing(project, identityLabel string) (map[string]string, error)

	// Create makes one item and returns its key in the tracker.
	Create(CreateRequest) (string, error)
}

// CreateRequest is one item, ready to create.
type CreateRequest struct {
	Project string
	Kind    Kind
	Summary string
	Body    string
	Labels  []string
	// Parent is the key of the parent already created, empty for the epic.
	// ParentKind travels with it because the level a child sits at can
	// depend on what it hangs off -- see the Jira mapping.
	Parent     string
	ParentKind Kind
}

// Step is one item's place in the plan: what will be created, or what is
// already there.
type Step struct {
	Item   *Item
	Parent *Item
	// ExistingKey is non-empty when the tracker already holds this item, in
	// which case it is linked rather than created again.
	ExistingKey string
}

// New reports whether this step would create something.
func (s Step) New() bool { return s.ExistingKey == "" }

// Plan is the whole tree, resolved against what the tracker already holds.
// It is what gets previewed, and one confirmation covers all of it.
type Plan struct {
	Tree    *Tree
	Project string
	Backend string
	Steps   []Step
}

// NewCount is how many items the plan would create.
func (p *Plan) NewCount() int {
	n := 0
	for _, s := range p.Steps {
		if s.New() {
			n++
		}
	}
	return n
}

// ExistingCount is how many items are already in the tracker.
func (p *Plan) ExistingCount() int { return len(p.Steps) - p.NewCount() }

// Build resolves the tree against the tracker WITHOUT creating anything.
//
// The search happens once, for the whole tree, before a single create: a
// re-run of the same tasks.md has to link what is there rather than
// duplicate it, and asking per item would be one round trip per item to
// answer a question one query answers.
func Build(t *Tree, b Backend, project string) (*Plan, error) {
	if t == nil || t.Epic == nil {
		return nil, errors.New("nothing to create: the task list parsed to an empty tree")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, errors.New("which project? a destination project key is required")
	}

	have, err := b.Existing(project, t.Label())
	if err != nil {
		return nil, fmt.Errorf("searching %s for what a previous run created: %w", project, err)
	}

	p := &Plan{Tree: t, Project: project, Backend: b.Name()}
	_ = t.Walk(func(it, parent *Item) error {
		p.Steps = append(p.Steps, Step{Item: it, Parent: parent, ExistingKey: have[it.Summary]})
		return nil
	})
	return p, nil
}

// Result is what Apply did, whether or not it finished.
type Result struct {
	// Keys is every item's key, created or already there, by summary.
	Keys map[string]string
	// Created are the keys this run made, in creation order. Separate from
	// Keys because "what did THIS run add" is the question a partial failure
	// has to answer.
	Created []string
	// Linked are the items that were already in the tracker.
	Linked []string
	// FailedAt is the summary of the item whose create failed, empty when
	// none did. The boundary a re-run resumes from.
	FailedAt string
}

// Apply creates the plan's new items, parent-first, and stops at the first
// failure.
//
// STOPS, rather than carrying on with the siblings. A half-created tree is
// recoverable -- Build finds what exists and the next run makes only the
// rest -- but only if the failure is reported at the point it happened. A
// run that skipped the failed item and created its children would leave
// orphans that no re-run can reattach, because their parent never got a key.
func Apply(p *Plan, b Backend) (Result, error) {
	res := Result{Keys: map[string]string{}}

	for _, s := range p.Steps {
		if !s.New() {
			res.Keys[s.Item.Summary] = s.ExistingKey
			res.Linked = append(res.Linked, s.ExistingKey)
			continue
		}

		parentKey, parentKind := "", Kind("")
		if s.Parent != nil {
			parentKey = res.Keys[s.Parent.Summary]
			parentKind = s.Parent.Kind
			if parentKey == "" {
				// Only reachable if Walk handed a child out before its
				// parent, which is the one thing its order guarantees. Fail
				// loudly rather than create a parentless item that looks
				// like part of the tree and is not in it.
				res.FailedAt = s.Item.Summary
				return res, fmt.Errorf("internal: %q would be created before its parent %q",
					s.Item.Summary, s.Parent.Summary)
			}
		}

		key, err := b.Create(CreateRequest{
			Project:    p.Project,
			Kind:       s.Item.Kind,
			Summary:    s.Item.Summary,
			Body:       s.Item.Body,
			Labels:     s.Item.Labels,
			Parent:     parentKey,
			ParentKind: parentKind,
		})
		if err != nil {
			res.FailedAt = s.Item.Summary
			return res, fmt.Errorf("creating %q in %s: %w", s.Item.Summary, p.Project, err)
		}
		res.Keys[s.Item.Summary] = key
		res.Created = append(res.Created, key)
	}
	return res, nil
}
