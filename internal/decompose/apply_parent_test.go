package decompose

import "testing"

// Walk guarantees parent-before-child, but Apply must not trust that blindly:
// a child whose parent has not been created yet -- and so has no key to
// carry -- must never reach the backend. Creating it anyway would produce an
// item that looks like part of the tree and is not in it.
func TestApplyRefusesToCreateAChildWhoseParentHasNoKeyYet(t *testing.T) {
	f := newFake()
	parent := &Item{Kind: KindStory, Summary: "US1 parent", Labels: []string{"x"}}
	child := &Item{Kind: KindTask, Summary: "T001 child", Labels: []string{"x"}}

	p := &Plan{
		Project: "CAT",
		Steps: []Step{
			// The child listed before its parent -- the one order Walk never
			// produces, forced here to prove Apply refuses rather than
			// silently creating an orphan.
			{Item: child, Parent: parent},
			{Item: parent, Parent: nil},
		},
	}

	res, err := Apply(p, f)
	if err == nil {
		t.Fatal("want an error: a child cannot be created before the parent whose key it needs")
	}
	if res.FailedAt != child.Summary {
		t.Errorf("FailedAt = %q, want %q", res.FailedAt, child.Summary)
	}
	if len(f.calls) != 0 {
		t.Errorf("%d creates were sent; a child without its parent's key must never reach the\n"+
			"backend", len(f.calls))
	}
}
