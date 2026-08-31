package collect

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeGit records what was merged into each ref and fails the merges the test
// names as conflicting. Enough to exercise assembly and isolation without a
// repository, which is the point: the interesting failures here are ordering
// and arithmetic, not git.
type fakeGit struct {
	conflict map[string]bool     // branch -> will not merge
	contents map[string][]string // ref -> branches merged into it
}

func newFakeGit(conflicting ...string) *fakeGit {
	g := &fakeGit{conflict: map[string]bool{}, contents: map[string][]string{}}
	for _, b := range conflicting {
		g.conflict[b] = true
	}
	return g
}

func (g *fakeGit) CutRef(ref, base string) error { g.contents[ref] = nil; return nil }
func (g *fakeGit) DropRef(ref string) error      { delete(g.contents, ref); return nil }
func (g *fakeGit) MergeInto(ref, branch string) error {
	if g.conflict[branch] {
		return errors.New("CONFLICT (content): Merge conflict in a.go\nfix it on the branch")
	}
	g.contents[ref] = append(g.contents[ref], branch)
	return nil
}

// fakeTester fails any ref containing a branch it was told is bad.
type fakeTester struct {
	g   *fakeGit
	bad map[string]bool
	n   int
}

func (t *fakeTester) Test(ref string) (bool, error) {
	t.n++
	for _, b := range t.g.contents[ref] {
		if t.bad[b] {
			return false, nil
		}
	}
	return true, nil
}

func members(keys ...string) []Member {
	out := make([]Member, 0, len(keys))
	for _, k := range keys {
		out = append(out, Member{Key: k, Branch: "orion/" + strings.ToLower(k)})
	}
	return out
}

// The saving the whole design is justified by: four branches, one CI run.
func TestAGreenBatchCostsOneRunForEveryMember(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3", "OR-4"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Green() {
		t.Fatalf("batch should be green: %v", b.Describe())
	}
	if b.Runs != 1 {
		t.Errorf("Runs = %d, want 1: four branches must not cost four runs", b.Runs)
	}
	if got := len(b.Members(Landed)); got != 4 {
		t.Errorf("landed %d, want 4", got)
	}
}

// Ejection happens at ASSEMBLY, before CI. That ordering is what lets a red
// result mean "a real defect" rather than "something did not merge".
func TestAConflictingBranchIsEjectedBeforeAnythingIsTested(t *testing.T) {
	g := newFakeGit("orion/or-2")
	tr := &fakeTester{g: g, bad: map[string]bool{}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Members(Ejected); len(got) != 1 || got[0] != "OR-2" {
		t.Fatalf("ejected = %v, want [OR-2]", got)
	}
	if got := b.Members(Landed); len(got) != 2 {
		t.Errorf("landed = %v, want the other two to carry on without it", got)
	}
	if b.Runs != 1 {
		t.Errorf("Runs = %d, want 1: ejection must not cost a CI run", b.Runs)
	}
	for _, r := range b.Results {
		if r.Key == "OR-2" && !strings.Contains(r.Reason, "conflicts with the batch") {
			t.Errorf("the ejection must say why: %q", r.Reason)
		}
	}
}

// Everything conflicting is not an error, and must not cost a run.
func TestABatchThatFullyConflictsTestsNothing(t *testing.T) {
	g := newFakeGit("orion/or-1", "orion/or-2")
	tr := &fakeTester{g: g, bad: map[string]bool{}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Runs != 0 {
		t.Errorf("Runs = %d, want 0", b.Runs)
	}
	if len(b.Members(Ejected)) != 2 {
		t.Errorf("both should be ejected: %v", b.Describe())
	}
}

// The point of isolation: one bad member must not sink the other seven.
func TestOneCulpritIsIsolatedAndTheRestAreOfferedAgain(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-6": true}}

	b, err := Land(g, tr, "batch", "develop",
		members("OR-1", "OR-2", "OR-3", "OR-4", "OR-5", "OR-6", "OR-7", "OR-8"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Members(Culprit); len(got) != 1 || got[0] != "OR-6" {
		t.Fatalf("culprit = %v, want [OR-6]", got)
	}
	if got := len(b.Members(Deferred)); got != 7 {
		t.Errorf("deferred %d, want the other 7 offered again rather than blamed", got)
	}
	// 8 serial runs is the thing being beaten. ~2*log2(8) is the honest
	// expectation, so anything at or above 8 means isolation bought nothing.
	if b.Runs >= 8 {
		t.Errorf("Runs = %d; isolation must cost less than testing all 8 separately", b.Runs)
	}
	t.Logf("8 members, 1 culprit: %d CI runs", b.Runs)
}

// Two culprits is the case a search that stops at the first would get wrong:
// it would land the second.
func TestBothCulpritsAreFoundRatherThanTheFirst(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-2": true, "orion/or-7": true}}

	b, err := Land(g, tr, "batch", "develop",
		members("OR-1", "OR-2", "OR-3", "OR-4", "OR-5", "OR-6", "OR-7", "OR-8"), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := b.Members(Culprit)
	if len(got) != 2 || got[0] != "OR-2" || got[1] != "OR-7" {
		t.Fatalf("culprits = %v, want [OR-2 OR-7]: stopping at the first would land the second", got)
	}
	for _, r := range b.Results {
		if r.Outcome == Deferred && (r.Key == "OR-2" || r.Key == "OR-7") {
			t.Errorf("%s is a culprit and must not be reported as merely deferred", r.Key)
		}
	}
}

// A batch of one must behave exactly like today's single-branch path, so the
// degenerate case is not a second code path to maintain.
func TestABatchOfOneBehavesLikeASingleBranch(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}
	b, _ := Land(g, tr, "batch", "develop", members("OR-1"), nil)
	if !b.Green() || b.Runs != 1 {
		t.Fatalf("green=%v runs=%d, want a single green run", b.Green(), b.Runs)
	}

	g2 := newFakeGit()
	t2 := &fakeTester{g: g2, bad: map[string]bool{"orion/or-1": true}}
	b2, _ := Land(g2, t2, "batch", "develop", members("OR-1"), nil)
	if got := b2.Members(Culprit); len(got) != 1 || got[0] != "OR-1" {
		t.Fatalf("culprit = %v, want the only member blamed", got)
	}
}

// An empty batch is a no-op, not an error: the queue is often empty.
func TestAnEmptyBatchDoesNothing(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}
	b, err := Land(g, tr, "batch", "develop", nil, nil)
	if err != nil || b.Runs != 0 || len(b.Results) != 0 {
		t.Fatalf("empty batch: err=%v runs=%d results=%d", err, b.Runs, len(b.Results))
	}
	if b.Green() {
		t.Error("an empty batch is not green; there is nothing to land")
	}
}

// recorder captures the observer calls in order.
type recorder struct{ calls []string }

func (r *recorder) Assembling(ref, base string, keys []string) {
	r.calls = append(r.calls, "assembling "+strings.Join(keys, " "))
}
func (r *recorder) Merged(key string)     { r.calls = append(r.calls, "merged "+key) }
func (r *recorder) Ejected(key, _ string) { r.calls = append(r.calls, "ejected "+key) }
func (r *recorder) Testing(run int)       { r.calls = append(r.calls, "testing") }
func (r *recorder) Split(keys []string, green bool, depth, runs int, culprit bool) {
	v := "red"
	if green {
		v = "green"
	}
	c := ""
	if culprit {
		c = " culprit"
	}
	r.calls = append(r.calls,
		fmt.Sprintf("split [%s] %s d%d r%d%s", strings.Join(keys, " "), v, depth, runs, c))
}
func (r *recorder) Settled(landed, ejected, culprits, deferred []string) {
	r.calls = append(r.calls, fmt.Sprintf("settled landed=%v ejected=%v culprit=%v deferred=%v",
		landed, ejected, culprits, deferred))
}

// The display is only worth anything if Land actually drives it. A renderer
// that is correct and wired to nothing passes every test in internal/ui and
// still shows an empty region for thirty minutes.
func TestLandDrivesTheObserverThroughEveryPhase(t *testing.T) {
	g := newFakeGit("orion/or-2") // OR-2 conflicts, so it is ejected
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}}
	r := &recorder{}

	if _, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3"), r); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.calls, "\n")

	for _, want := range []string{
		"assembling OR-1 OR-2 OR-3",
		"merged OR-1",
		"ejected OR-2",
		"testing",
		"settled",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the observer never saw %q:\n%s", want, got)
		}
	}
	// The isolation tree has to be reported DURING the search, or the display
	// has nothing to draw while the runs are being spent.
	if !strings.Contains(got, "split") {
		t.Errorf("no split was reported, so the tree could not be drawn:\n%s", got)
	}
	// Assembling must come first: the display opens the batch on it, and a
	// member reported before the batch exists is dropped.
	if r.calls[0] != "assembling OR-1 OR-2 OR-3" {
		t.Errorf("the first call is %q, not the batch being opened", r.calls[0])
	}
}

// A nil observer must be safe, because every existing caller passes one.
func TestANilObserverIsSafe(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}
	if _, err := Land(g, tr, "batch", "develop", members("OR-1"), nil); err != nil {
		t.Fatal(err)
	}
}
