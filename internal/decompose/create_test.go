package decompose

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fake is a tracker that records what it was asked to do. It stands in for
// the backends OR-303 will add: everything above Backend is written against
// the interface, so a test that passes here is a test of the creation
// contract rather than of Jira.
type fake struct {
	name string
	// have is what the tracker already holds: summary -> key.
	have map[string]string
	// calls is every create, in order.
	calls []CreateRequest
	// failOn makes the create of that summary fail, standing in for a
	// permission, a validation or a network failure mid-tree.
	failOn string
	n      int
	// searched records the arguments Existing was called with.
	searched []string
}

func newFake() *fake { return &fake{name: "fake", have: map[string]string{}} }

func (f *fake) Name() string { return f.name }

func (f *fake) Existing(project, label string) (map[string]string, error) {
	f.searched = append(f.searched, project+" "+label)
	out := map[string]string{}
	for k, v := range f.have {
		out[k] = v
	}
	return out, nil
}

func (f *fake) Create(req CreateRequest) (string, error) {
	if req.Summary == f.failOn {
		return "", errors.New("the tracker refused this one")
	}
	f.calls = append(f.calls, req)
	f.n++
	key := fmt.Sprintf("%s-%d", req.Project, f.n)
	f.have[req.Summary] = key
	return key, nil
}

func TestBuildCreatesNothing(t *testing.T) {
	f := newFake()
	p, err := Build(parseFixture(t), f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("Build created %d items; the preview must come first and cost nothing", len(f.calls))
	}
	if p.NewCount() != 8 || p.ExistingCount() != 0 {
		t.Errorf("plan = %d new, %d existing; want the whole tree new against an empty tracker",
			p.NewCount(), p.ExistingCount())
	}
	if len(f.searched) != 1 {
		t.Errorf("Existing was called %d times; one query answers it for the whole tree",
			len(f.searched))
	}
	if want := "CAT orion-spec-product-catalogue"; f.searched[0] != want {
		t.Errorf("searched %q, want %q: without the identity label a re-run links another\n"+
			"feature's T001 to this tree", f.searched[0], want)
	}
}

func TestApplyCreatesParentFirstAndEveryChildCarriesItsParent(t *testing.T) {
	f := newFake()
	tree := parseFixture(t)
	p, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(p, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 8 {
		t.Fatalf("created %d items, want the whole tree", len(res.Created))
	}

	// The epic first, and nothing before it: every other item needs its key.
	if f.calls[0].Kind != KindEpic || f.calls[0].Parent != "" {
		t.Fatalf("first create = %+v, want the epic with no parent", f.calls[0])
	}
	epicKey := res.Keys[tree.Epic.Summary]

	seen := map[string]bool{}
	for i, c := range f.calls {
		if c.Parent == "" {
			if c.Kind != KindEpic {
				t.Errorf("call %d (%s %q) has no parent: an item outside the tree is not\n"+
					"part of the tree, and no re-run can reattach it", i, c.Kind, c.Summary)
			}
			seen[c.Summary] = true
			continue
		}
		if !seen[parentSummaryOf(res, c.Parent)] {
			t.Errorf("call %d (%q) names parent %s, which had not been created yet",
				i, c.Summary, c.Parent)
		}
		seen[c.Summary] = true
	}

	// The shape, read back from what was sent: stories and ungrouped tasks
	// hang off the epic, and a grouped task hangs off its story.
	for _, c := range f.calls {
		switch {
		case c.Kind == KindStory:
			if c.Parent != epicKey || c.ParentKind != KindEpic {
				t.Errorf("story %q hangs off %q, want the epic", c.Summary, c.Parent)
			}
		case c.Kind == KindTask && strings.HasPrefix(c.Summary, "T00"):
			if c.Parent == "" {
				t.Errorf("task %q has no parent", c.Summary)
			}
		}
	}
	if got := parentKindOf(f.calls, "T003"); got != KindStory {
		t.Errorf("T003 is in the [US1] group and hangs off %q, want the story", got)
	}
	if got := parentKindOf(f.calls, "T001"); got != KindEpic {
		t.Errorf("T001 is in no story group and hangs off %q, want the epic", got)
	}
}

// The failure is where the guarantee lives: creation halts, reports the
// boundary, and a re-run makes only the rest.
func TestApplyHaltsAtTheFailureAndAReRunResumes(t *testing.T) {
	f := newFake()
	f.failOn = "T003 Create the Product model in internal/catalogue/product.go"

	tree := parseFixture(t)
	p, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(p, f)
	if err == nil {
		t.Fatal("want an error from the failed create")
	}
	if res.FailedAt != f.failOn {
		t.Errorf("FailedAt = %q, want the item that failed: a boundary nobody is told\n"+
			"cannot be resumed from", res.FailedAt)
	}
	firstRound := len(res.Created)
	if firstRound != 2 {
		t.Fatalf("created %d before the failure, want the epic and US1 -- the two items\n"+
			"parent-first order reaches before T003; got %v", firstRound, res.Created)
	}
	// Nothing after the failure was attempted, so no orphan was made under a
	// parent that never got a key.
	for _, c := range f.calls {
		if c.Summary == "T004 Implement listing in internal/catalogue/list.go (depends on T003)" {
			t.Error("a sibling after the failure was created; creation must stop at the boundary")
		}
	}

	// Second run: the failure is gone, and only the missing items are made.
	f.failOn = ""
	f.calls = nil
	p2, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	if p2.ExistingCount() != firstRound {
		t.Errorf("re-run sees %d existing, want the %d the first run created",
			p2.ExistingCount(), firstRound)
	}
	res2, err := Apply(p2, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Created) != 8-firstRound {
		t.Errorf("re-run created %d items, want only the %d that were missing",
			len(res2.Created), 8-firstRound)
	}
	if len(res2.Linked) != firstRound {
		t.Errorf("re-run linked %d, want the %d already there rather than duplicating them",
			len(res2.Linked), firstRound)
	}
	// Every item exists exactly once, which is the whole point of linking.
	counts := map[string]int{}
	for s := range f.have {
		counts[s]++
	}
	if len(counts) != 8 {
		t.Errorf("the tracker holds %d distinct items after two runs, want 8", len(counts))
	}
	// And a THIRD run, with nothing left to do, creates nothing at all.
	p3, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	if p3.NewCount() != 0 {
		t.Errorf("a settled tree still proposes %d creates", p3.NewCount())
	}
}

func TestBuildRefusesAnEmptyDestination(t *testing.T) {
	if _, err := Build(parseFixture(t), newFake(), "  "); err == nil {
		t.Error("want an error rather than a tree created into no project")
	}
	if _, err := Build(nil, newFake(), "CAT"); err == nil {
		t.Error("want an error rather than a silent success on an empty tree")
	}
}

func parentSummaryOf(res Result, key string) string {
	for s, k := range res.Keys {
		if k == key {
			return s
		}
	}
	return ""
}

func parentKindOf(calls []CreateRequest, id string) Kind {
	for _, c := range calls {
		if strings.HasPrefix(c.Summary, id+" ") {
			return c.ParentKind
		}
	}
	return ""
}
