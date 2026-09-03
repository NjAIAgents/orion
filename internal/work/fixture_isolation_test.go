package work

// Isolation tests for buildSeed/copyTree (added in v0.8.10, extended by
// OR-315): the repository behind project() is built once per config and
// copied per test. These assert the property the rest of the package leans
// on without checking: a commit pushed through one copy's clone must not
// reach the shared seed, nor a sibling copy made from that same seed.

import (
	"path/filepath"
	"testing"
)

func TestWorkFixtureCopiesAreIndependent(t *testing.T) {
	seed := buildSeed(t, cfg)

	root1 := t.TempDir()
	copyTree(t, seed, root1)
	root2 := t.TempDir()
	copyTree(t, seed, root2)

	clone1 := filepath.Join(root1, "seed")
	origin1 := filepath.Join(root1, "origin.git")
	origin2 := filepath.Join(root2, "origin.git")
	seedOrigin := filepath.Join(seed, "origin.git")

	before2 := git(t, origin2, "rev-parse", "refs/heads/develop")
	beforeSeed := git(t, seedOrigin, "rev-parse", "refs/heads/develop")

	// A copied clone's .git/config still names the seed's own origin.git by
	// its absolute path from build time (git absolutises it on clone); this
	// repoints it at root1's copy, exactly as workspace.Bind's Remote
	// argument does for project()'s real callers.
	git(t, clone1, "remote", "set-url", "origin", origin1)

	git(t, clone1, "commit", "--allow-empty", "-q", "-m", "belongs to root1 alone")
	git(t, clone1, "push", "-q", "origin", "develop")
	want := git(t, clone1, "rev-parse", "HEAD")

	if got := git(t, origin1, "rev-parse", "refs/heads/develop"); got != want {
		t.Errorf("root1's own origin is %s, want the pushed %s", got, want)
	}
	if got := git(t, origin2, "rev-parse", "refs/heads/develop"); got != before2 {
		t.Errorf("root2's origin moved from %s to %s after a push through root1's clone",
			before2, got)
	}
	if got := git(t, seedOrigin, "rev-parse", "refs/heads/develop"); got != beforeSeed {
		t.Errorf("the shared seed moved from %s to %s after a push through a copy's clone",
			beforeSeed, got)
	}
}
