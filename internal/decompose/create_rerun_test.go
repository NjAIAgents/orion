package decompose

import (
	"errors"
	"strings"
	"testing"
)

// The plain case, isolated from the failure-and-resume test next to it: a
// run that fully succeeds, followed by a second run of the same tasks.md,
// must link every item it already made and create none of them twice.
func TestSecondRunAfterACleanFirstRunLinksEverythingAndCreatesNothing(t *testing.T) {
	f := newFake()
	tree := parseFixture(t)

	p1, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	res1, err := Apply(p1, f)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(res1.Created) != 8 || len(res1.Linked) != 0 {
		t.Fatalf("first run created %d, linked %d; want the whole tree new",
			len(res1.Created), len(res1.Linked))
	}
	firstCallCount := len(f.calls)

	p2, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	if p2.NewCount() != 0 {
		t.Errorf("second run proposes %d new items against a settled tree, want 0", p2.NewCount())
	}
	if p2.ExistingCount() != 8 {
		t.Errorf("second run sees %d existing, want all 8", p2.ExistingCount())
	}

	res2, err := Apply(p2, f)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(res2.Created) != 0 {
		t.Errorf("second run created %d items; a settled tree must create nothing", len(res2.Created))
	}
	if len(res2.Linked) != 8 {
		t.Errorf("second run linked %d items, want all 8", len(res2.Linked))
	}
	if len(f.calls) != firstCallCount {
		t.Errorf("Create was called again on the second run: %d calls now vs %d after the first run",
			len(f.calls), firstCallCount)
	}
	// Every item's key is stable across the two runs, which is what "linked"
	// promises rather than a same-looking duplicate under a new key.
	for summary, key := range res1.Keys {
		if res2.Keys[summary] != key {
			t.Errorf("%q resolved to %q on the first run and %q on the second",
				summary, key, res2.Keys[summary])
		}
	}
}

// A tasks.md with a `# Tasks:` heading and no task lines at all still parses
// to a lone epic, and that epic must still be created: an empty feature is a
// real state (the plan is written, nothing is broken out yet), not an error
// that should leave the tracker with nothing to show for it.
func TestBuildAndApplyStillCreateTheEpicWhenThereAreNoTasksAtAll(t *testing.T) {
	f := newFake()
	tree, err := Parse("# Tasks: Nothing Yet\n", "specs/014-empty/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Count() != 1 {
		t.Fatalf("want a lone epic, got %d items", tree.Count())
	}

	p, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}
	if p.NewCount() != 1 {
		t.Fatalf("plan proposes %d new items, want the epic alone", p.NewCount())
	}

	res, err := Apply(p, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("created %d items, want the epic alone", len(res.Created))
	}
	if len(f.calls) != 1 || f.calls[0].Kind != KindEpic {
		t.Fatalf("backend received %+v, want a single epic create", f.calls)
	}
}

// failBackend stands in for a tracker whose Create fails for a reason of its
// own -- a permission refusal, a validation error, a network failure -- each
// with its own distinct message. Apply must not collapse that into a generic
// "failed": the operator reading the error is the one who has to act on
// whichever of those it actually was.
type failBackend struct {
	*fake
	err error
}

func (b *failBackend) Create(req CreateRequest) (string, error) {
	if req.Summary == b.fake.failOn {
		return "", b.err
	}
	return b.fake.Create(req)
}

func TestApplySurfacesTheBackendsOwnErrorMessageRatherThanAGenericOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"permission", errors.New("403: user lacks the Create Issues permission in project CAT")},
		{"validation", errors.New("400: field 'summary' cannot exceed 255 characters")},
		{"network", errors.New("dial tcp: connect: connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newFake()
			base.failOn = "T001 Do it in a.go"
			b := &failBackend{fake: base, err: tc.err}

			tree, err := Parse("# Tasks: Fails\n\n- [ ] T001 Do it in a.go\n", "specs/015-fails/tasks.md")
			if err != nil {
				t.Fatal(err)
			}
			p, err := Build(tree, b, "CAT")
			if err != nil {
				t.Fatal(err)
			}
			_, applyErr := Apply(p, b)
			if applyErr == nil {
				t.Fatal("want an error from the failed create")
			}
			if !strings.Contains(applyErr.Error(), tc.err.Error()) {
				t.Errorf("error = %q, want the backend's own message %q preserved verbatim\n"+
					"rather than replaced with a generic one", applyErr.Error(), tc.err.Error())
			}
		})
	}
}
