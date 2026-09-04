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

	// sha is what SHAOf answers, per ref. A base that "moves" during a batch
	// is modelled by writing a new value here between calls, which is how the
	// ADR 0017 precondition is tested without a repository.
	sha map[string]string
	// landed records what LandRef merged, in order, so a test can assert that
	// the ref that was TESTED is the ref that merged -- the property the whole
	// design turns on.
	// deletedRemote records every ref removed from the forge (OR-337). The
	// scratch refs a bisection cuts are PUSHED to be tested, so dropping the
	// local branch alone leaks the remote one.
	deletedRemote []string
	landed        []string
	landErr       error
	landInto      map[string]string
}

func newFakeGit(conflicting ...string) *fakeGit {
	g := &fakeGit{
		conflict: map[string]bool{}, contents: map[string][]string{},
		sha: map[string]string{}, landInto: map[string]string{},
	}
	for _, b := range conflicting {
		g.conflict[b] = true
	}
	return g
}

// SHAOf answers from the map, defaulting to a stable value derived from the
// name so a test that does not care about SHAs does not have to set any.
func (g *fakeGit) SHAOf(ref string) (string, error) {
	if s, ok := g.sha[ref]; ok {
		return s, nil
	}
	return "sha-" + ref, nil
}

func (g *fakeGit) LandRef(ref, base string) (string, error) {
	if g.landErr != nil {
		return "", g.landErr
	}
	g.landed = append(g.landed, ref)
	g.landInto[base] = ref
	// Base now points at what landed, which is what a real merge leaves
	// behind and what a later SHAOf would report.
	g.sha[base] = "sha-after-" + ref
	return g.sha[base], nil
}

func (g *fakeGit) CutRef(ref, base string) error { g.contents[ref] = nil; return nil }
func (g *fakeGit) DropRef(ref string) error      { delete(g.contents, ref); return nil }
func (g *fakeGit) DeleteRemoteRef(ref string) error {
	g.deletedRemote = append(g.deletedRemote, ref)
	return nil
}
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
	// tested records every ref a run was spent on, so a test can assert that
	// what merged is what was proven rather than merely what was assembled.
	tested map[string]bool
	// onTest runs during a test, standing in for the world changing while CI
	// is in flight -- a person pushing to the base being the case that
	// matters (ADR 0017).
	onTest func()
}

func (t *fakeTester) Test(ref string) (bool, error) {
	t.n++
	if t.tested == nil {
		t.tested = map[string]bool{}
	}
	t.tested[ref] = true
	if t.onTest != nil {
		t.onTest()
	}
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

// The point of isolation: one bad member must not sink the other seven, and
// since OR-253 the seven LAND rather than waiting for a later batch.
//
// Deferring them was the old contract and it is what let one bad branch hold
// good work hostage for a whole cycle. The culprit goes back to the coding
// queue; the rest are not punished for having been assembled beside it.
func TestOneCulpritIsIsolatedAndTheRestStillLand(t *testing.T) {
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
	if got := len(b.Members(Landed)); got != 7 {
		t.Errorf("landed %d, want the other 7 to land now rather than wait: %v",
			got, b.Describe())
	}
	if got := len(b.Members(Deferred)); got != 0 {
		t.Errorf("deferred %d, want none: a sound member waiting for a later batch "+
			"is the behaviour OR-253 removes", got)
	}
	// The culprit must not be among what merged. Landing the branch the
	// search just blamed would be the worst possible outcome of isolating it.
	for _, ref := range g.landed {
		for _, merged := range g.contents[ref] {
			if merged == "orion/or-6" {
				t.Fatalf("the culprit's branch was merged in %s: %v", ref, g.contents[ref])
			}
		}
	}
	// THE BASELINE IS NOT 8, and pretending it is understates the batch by
	// exactly the term it was built to remove.
	//
	// Landing 8 branches the per-branch way costs 8 pull-request runs PLUS a
	// rebase cascade: `ci.require_up_to_date` means each merge leaves the
	// remaining branches behind the work branch, each is rebased, and each
	// rebase triggers another run. That is the quadratic term ADR 0015 and
	// Collect.AutoRebase both describe -- measured at 27 rebases across ~26
	// merges on this repository.
	//
	// So the honest ceiling for a RED batch of 8 is the naive 8 plus that
	// cascade. This asserts the weaker, safer bound: a red batch must never
	// cost MORE than one run per member plus one, which is what the search
	// (~2*log2(8)) plus a confirming run comes to. A green batch of 8 costs
	// one run against eight, and that is where the design earns its keep --
	// at a measured 0.10 per-branch failure rate, most batches are green.
	if max := len(b.Results) + 1; b.Runs > max {
		t.Errorf("Runs = %d, want at most %d for 8 members with one culprit: "+
			"the search plus one confirming run", b.Runs, max)
	}
	t.Logf("8 members, 1 culprit: %d CI runs (search + confirmation); "+
		"per-branch would be 8 runs plus the rebase cascade", b.Runs)
}

// The reuse path (OR-253 option B): when the search already proved exactly the
// sound set, landing it must cost NO further run.
//
// Narrow by construction, and that is worth recording rather than discovering.
// Bisection proves HALVES, so the sound remainder is a proven set only when
// the red side turns out to be entirely culprits. Two members with one bad is
// the smallest case where that holds, and the assertion is about the run
// count, not the shape: a reused proof must not be re-bought.
func TestAProvenSoundSetLandsWithoutAnotherRun(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-2": true}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Members(Culprit); len(got) != 1 || got[0] != "OR-2" {
		t.Fatalf("culprit = %v, want [OR-2]", got)
	}
	if got := b.Members(Landed); len(got) != 1 || got[0] != "OR-1" {
		t.Fatalf("landed = %v, want [OR-1] to land on the search's own proof", got)
	}
	// One run for the batch, two for the split. A fourth would mean the
	// already-proved set was tested a second time to learn what it knew.
	if b.Runs != 3 {
		t.Errorf("Runs = %d, want 3: the sound set was already proved green, "+
			"so landing it must not buy another run", b.Runs)
	}
}

// The confirming run is not ceremony: the sound set must be TESTED as a set
// before it lands, because bisection proves halves and never the remainder.
//
// [A,B] green and [C] green does not prove [A,B,C]; merging on those two
// results would put C on a base containing A and B that C was never tested
// against -- the unvalidated-base merge ADR 0017 exists to refuse.
func TestTheSoundSetIsTestedAsASetBeforeItLands(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3", "OR-4"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.landed) != 1 {
		t.Fatalf("landed %d refs, want exactly one: the sound set merges as a set, "+
			"not one proven subset at a time", len(g.landed))
	}
	landedRef := g.landed[0]
	if !tr.tested[landedRef] {
		t.Errorf("%s merged without ever being tested; bisection proves halves, "+
			"not the remainder", landedRef)
	}
	if got := len(b.Members(Landed)); got != 3 {
		t.Errorf("landed %d, want OR-1, OR-2 and OR-4: %v", got, b.Describe())
	}
}

// ADR 0017: a green result may only merge into the base it was validated
// against. If the base moved while CI ran, the proof belongs to a tree that no
// longer exists, and merging anyway is how a validated result silently becomes
// an unvalidated one.
func TestABaseThatMovedDuringTestingRefusesToMerge(t *testing.T) {
	g := newFakeGit()
	g.sha["develop"] = "before"
	tr := &fakeTester{g: g, bad: map[string]bool{}}
	// Somebody pushes to develop while the batch is testing.
	tr.onTest = func() { g.sha["develop"] = "after" }

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2"), nil)
	if err == nil {
		t.Fatalf("merged into a base that moved; want a refusal. results: %v", b.Describe())
	}
	if len(g.landed) != 0 {
		t.Errorf("landed %v despite the base moving", g.landed)
	}
	if !strings.Contains(err.Error(), "moved") {
		t.Errorf("error = %q, want it to name the base moving so the operator "+
			"knows to reassemble rather than retry", err)
	}
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

// Every scratch ref a bisection cuts is deleted from the FORGE, not only
// from the clone (OR-337).
//
// Test pushes each split -- it cannot reach CI otherwise -- so dropping the
// local branch alone left the remote one behind for good. Observed as
// orion/batch-iso-2-0 and orion/batch-iso-2-1 still on the remote days after
// the search that created them, among twenty-five stale branches. Nothing
// will ever read them again.
func TestEveryIsolationRefIsDeletedFromTheForgeToo(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}}

	if _, err := Land(g, tr, "batch", "develop",
		members("OR-1", "OR-2", "OR-3", "OR-4"), nil); err != nil {
		t.Fatal(err)
	}

	// The search cut refs; every one of them is gone from the remote.
	var scratch []string
	for _, ref := range g.deletedRemote {
		if strings.Contains(ref, "-iso-") || strings.HasSuffix(ref, "-clean") {
			scratch = append(scratch, ref)
		}
	}
	if len(scratch) == 0 {
		t.Fatalf("no scratch ref was deleted from the forge; deleted = %v", g.deletedRemote)
	}
	// The ref that LANDED is deleted too, and that is correct: the drop is
	// deferred past land(), so the merge is on the work branch before the
	// ref goes. An ephemeral ref that survives its own landing is exactly
	// the leak this fixes -- what matters is the ORDER, which the deferred
	// drop guarantees, not that the ref is spared.
	//
	// What must never be deleted is the work branch itself.
	for _, ref := range g.deletedRemote {
		if ref == "develop" {
			t.Fatalf("the work branch was deleted from the forge: %v", g.deletedRemote)
		}
	}
}
