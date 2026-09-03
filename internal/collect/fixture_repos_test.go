package collect

// Dedicated coverage for the origin/clone seed in fixture_test.go (OR-315):
// that it is built exactly once for the binary, that a copy's remote points
// at ITS OWN origin rather than the shared seed's, and that two copies trace
// back to one build rather than two independently constructed histories.

import (
	"path/filepath"
	"testing"
)

// seedRepos must return the same directory on every call for the life of the
// binary. A regression back to building the pair per test would still return
// a usable path -- every test would keep passing -- but the pair would be
// rebuilt from scratch each time, which is exactly the cost this fixture
// exists to remove.
func TestSeedReposReturnsTheSameDirectoryForRepeatedCalls(t *testing.T) {
	first := seedRepos(t)
	second := seedRepos(t)
	third := seedRepos(t)

	if first != second || second != third {
		t.Errorf("seedRepos rebuilt across calls: %s, %s, %s", first, second, third)
	}
}

// A copied clone's origin remote must be repointed at the copy's own bare
// origin, not left pointing at the shared seed's origin.git. git absolutises
// a local clone URL on copy, so without the repoint every push would land in
// the tree every other test copies from.
func TestRepoCopyRemoteURLRepointedToItsOwnOrigin(t *testing.T) {
	origin, clone := repos(t)

	got, err := gitLine(clone, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("reading the clone's origin remote: %v", err)
	}
	if got != origin {
		t.Errorf("clone's origin remote = %q, want its own origin %q", got, origin)
	}

	seedOrigin := filepath.Join(seedRepos(t), "origin.git")
	if got == seedOrigin {
		t.Errorf("clone's origin remote still points at the shared seed %q", seedOrigin)
	}
}

// Two calls to repos(t) copy from the SAME seed build, so develop's tip
// should be byte-identical between them. Per-test independent construction
// (git init a fresh origin, commit, clone) would not reproduce this: nothing
// pins the commit timestamp, so two independently built histories get
// different commit hashes even with identical tree content and message.
func TestRepoCopiesShareIdenticalCommitHistory(t *testing.T) {
	firstOrigin, _ := repos(t)
	secondOrigin, _ := repos(t)

	first := head(t, firstOrigin, "refs/heads/develop")
	second := head(t, secondOrigin, "refs/heads/develop")
	if first != second {
		t.Errorf("develop's tip differs between two copies of the same seed: %s vs %s "+
			"(each copy looks like it was built independently rather than shared)", first, second)
	}
}
