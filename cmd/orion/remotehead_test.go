package main

import (
	"path/filepath"
	"testing"
)

// OR-566: a batch lands through the forge, so the sandbox clone's
// origin/develop never moves by itself. remoteHead must see the new head.
func TestRemoteHeadSeesAMoveNobodyFetched(t *testing.T) {
	root := t.TempDir()
	origin, clone, other := filepath.Join(root, "origin.git"), filepath.Join(root, "clone"), filepath.Join(root, "other")
	git(t, root, "init", "-q", "--bare", "-b", "develop", origin)
	git(t, root, "clone", "-q", origin, other)
	git(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "one")
	git(t, other, "push", "-q", "origin", "HEAD:develop")
	git(t, root, "clone", "-q", origin, clone)
	before := git(t, clone, "rev-parse", "origin/develop")

	git(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "two")
	git(t, other, "push", "-q", "origin", "HEAD:develop")
	moved := git(t, other, "rev-parse", "HEAD")

	if got := remoteHead(clone, "develop"); got != moved || got == before {
		t.Fatalf("remoteHead = %s, want the moved head %s (stale was %s)", got, moved, before)
	}
}
