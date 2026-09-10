package collect

import (
	"errors"
	"strings"
	"testing"
)

// pairTester is red only when BOTH named branches are in the ref.
//
// fakeTester cannot express this: it fails any ref containing one bad branch,
// which is every fault bisection was designed for. An interaction fault is
// the case where no member is bad and a pair is, and it needs its own double
// or the bug under test cannot be reproduced at all.
type pairTester struct {
	g    *fakeGit
	a, b string
	n    int
}

func (t *pairTester) Test(ref string) (bool, error) {
	t.n++
	var seenA, seenB bool
	for _, br := range t.g.contents[ref] {
		if strings.Contains(br, t.a) {
			seenA = true
		}
		if strings.Contains(br, t.b) {
			seenB = true
		}
	}
	return !(seenA && seenB), nil
}

// THE NIGHT OF 2026-09-10, reproduced.
//
// OR-274 spelled the literals "orion-failed" and "ORION" in
// internal/web/gates.go; OR-273 added a test forbidding any queue label in a
// non-test file of that package. Each branch is green alone. The batch
// holding both is red, and every subset bisection tries is sound:
// [OR-59 OR-274 OR-277] green, [OR-276 OR-273] green.
//
// The search therefore found nobody, the caller read that as everybody
// innocent, and the loop spent 91 CI runs over 13.5 hours landing nothing.
func TestABatchRedOnlyAsAWholeIsReportedRatherThanRetried(t *testing.T) {
	g := newFakeGit()
	m := members("OR-59", "OR-274", "OR-277", "OR-276", "OR-273")
	tr := &pairTester{g: g, a: "or-274", b: "or-273"}

	b, err := Land(g, tr, "batch", "develop", m, nil)

	if !errors.Is(err, ErrInteractionFault) {
		t.Fatalf("a batch red only as a whole must say so; got err=%v, culprits=%v",
			err, b.Members(Culprit))
	}
	// The dangerous outcome is not the missing verdict, it is landing on it.
	if got := b.Members(Landed); len(got) > 0 {
		t.Errorf("nothing may land when no member was cleared; landed %v", got)
	}
	// And convicting an innocent branch would be just as wrong: neither
	// member is at fault on its own, so neither may be ejected.
	if got := b.Members(Culprit); len(got) > 0 {
		t.Errorf("no single branch is guilty of an interaction fault; convicted %v",
			got)
	}
}

// The ordinary case must keep working exactly as it did. One guilty branch is
// what bisection is for, and a fix that reported every red batch as an
// interaction fault would eject nobody ever.
func TestASingleCulpritIsStillConvicted(t *testing.T) {
	g := newFakeGit()
	m := members("OR-59", "OR-274", "OR-277", "OR-276")
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-277": true}}

	b, err := Land(g, tr, "batch", "develop", m, nil)
	if err != nil {
		t.Fatalf("a single culprit is an ordinary red batch: %v", err)
	}
	if got := b.Members(Culprit); len(got) != 1 || got[0] != "OR-277" {
		t.Errorf("expected OR-277 convicted alone, got %v", got)
	}
}

// A green batch is untouched by any of this.
func TestAGreenBatchIsUnaffected(t *testing.T) {
	g := newFakeGit()
	m := members("OR-59", "OR-274")
	tr := &fakeTester{g: g, bad: map[string]bool{}}

	if _, err := Land(g, tr, "batch", "develop", m, nil); err != nil {
		t.Fatalf("a green batch must not report a fault: %v", err)
	}
}

// The fault is reported ONCE, not re-searched every tick. This is the half of
// the bug that turned a missing verdict into 13.5 hours: knownRed sent the
// same members back into the same search on every pass.
//
// Asserted on the run count rather than on the record, because the record is
// how it is achieved and the run count is what it is for.
func TestTheSearchIsNotRepaidForTheSameSet(t *testing.T) {
	g := newFakeGit()
	m := members("OR-274", "OR-273")
	tr := &pairTester{g: g, a: "or-274", b: "or-273"}

	if _, err := Land(g, tr, "batch", "develop", m, nil); !errors.Is(err, ErrInteractionFault) {
		t.Fatalf("expected an interaction fault, got %v", err)
	}
	spent := tr.n
	if spent == 0 {
		t.Fatal("the search spent no runs at all; the fixture is not exercising it")
	}
	// A two-member batch splits into two single-member halves, both green,
	// so the whole search is three runs: the set and its two parts.
	if spent > 4 {
		t.Errorf("the search spent %d runs on two members; a bounded search "+
			"should not need more than the set and its parts", spent)
	}
}
