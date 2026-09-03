package collect

// Isolation tests for the fixture seeds built once per binary in
// fixture_test.go (OR-315). fixture_test.go already asserts that the trees
// are built only once and that a copied clone does not push back into the
// seed's origin; the two cases here close the gap it leaves open: that a
// mutation made through ONE test's copy -- workspace or repository -- never
// leaks into the seed itself or into a SIBLING copy made from that same
// seed.

import (
	"path/filepath"
	"testing"
)

// A copy's task.json is edited in place by SaveTask. If that ever wrote
// through to the seed, every later newWorkspace call for this idea would
// start from a polluted seed instead of the pristine one workspace.New built.
func TestWorkspaceCopyMutationDoesNotAffectTheSeedOrOtherCopies(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())

	first := newWorkspace(t, "isolation")
	first.Task.Branches = []string{"mutated-by-first"}
	if err := first.SaveTask(); err != nil {
		t.Fatal(err)
	}

	// The FIRST COPY'S value, not a non-empty list. workspace.New provisions
	// main and develop on every workspace, so a fresh copy carrying them is
	// what a provisioned workspace looks like -- asserting emptiness here
	// fired on the ordinary case and could never pass. What must not appear
	// is the branch the first copy wrote.
	second := newWorkspace(t, "isolation")
	for _, b := range second.Task.Branches {
		if b == "mutated-by-first" {
			t.Errorf("a fresh copy from the same seed carries the first "+
				"copy's branch %q; the mutation leaked through the seed", b)
		}
	}
}

// repos(t) is called independently by many tests. Two calls in the same test
// stand in for two different tests sharing the seed: a commit pushed through
// one clone must not appear in the other, since they are supposed to be
// independent copies of the same starting point rather than views onto one.
func TestTwoRepoCopiesFromTheSameSeedAreIndependent(t *testing.T) {
	firstOrigin, firstClone := repos(t)
	secondOrigin, secondClone := repos(t)

	before := head(t, secondOrigin, "refs/heads/develop")

	commit(t, firstClone, "", "a commit belonging to the first copy alone")
	gitRun(t, firstClone, "push", "--quiet", "origin", "develop")

	if got := head(t, secondOrigin, "refs/heads/develop"); got != before {
		t.Errorf("the second copy's origin moved from %s to %s after a push "+
			"through the first copy's clone", before, got)
	}
	if got := head(t, secondClone, "HEAD"); got != before {
		t.Errorf("the second copy's clone moved from %s to %s after a push "+
			"through the first copy's clone", before, got)
	}
	if filepath.Clean(firstOrigin) == filepath.Clean(secondOrigin) {
		t.Fatalf("both copies share one origin directory: %s", firstOrigin)
	}
}
