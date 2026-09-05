package main

import (
	"testing"
	"time"
)

// OR-128: a `gh` that hangs must not be able to block the caller forever.
// A fake `gh` that sleeps far longer than the (shrunk-for-the-test) timeout
// proves the context actually cuts it off, rather than merely existing.
func TestGhCommandEnforcesItsTimeout(t *testing.T) {
	dir := t.TempDir()
	// `exec sleep` rather than a plain `sleep` line: the real `gh` binary is
	// a single process (it does not shell out and fork), so `exec` here
	// makes the fake match that shape -- the interpreter REPLACES itself
	// with sleep rather than forking a child. Without `exec`, sh forks
	// sleep as a genuine child; killing the parent orphans it, and it keeps
	// the stdout pipe open until its own 5s elapses regardless of the
	// context firing -- a fixture artifact, not something a real gh hang
	// would do.
	writeFakeBinIn(t, dir, "gh", "#!/bin/sh\nexec sleep 5\n")

	old := ghTimeout
	ghTimeout = 200 * time.Millisecond
	t.Cleanup(func() { ghTimeout = old })

	cmd, cancel := ghCommand(t.TempDir(), "pr", "view", "x")
	defer cancel()

	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("a hung gh must return an error, got clean output: %q", out)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ghCommand did not enforce its timeout: took %s", elapsed)
	}
	if len(out) != 0 {
		t.Fatalf("expected no output from a killed process, got: %q", out)
	}
}

// Opening the pull request is the LAST step of a job that has already been
// paid for, so a hang there strands work that otherwise succeeded -- and it
// happens inside orion watch's loop, where nothing else will print. It was
// the one gh call on that path still left unbounded.
func TestOpenPRDoesNotHangOnAStalledGh(t *testing.T) {
	fakeBin(t, "gh", "#!/bin/sh\nexec sleep 5\n")

	old := ghTimeout
	ghTimeout = 200 * time.Millisecond
	t.Cleanup(func() { ghTimeout = old })

	start := time.Now()
	_, err := openPR(t.TempDir(), "orion/or-128", "title", "body", "develop")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a hung `gh pr create` must report an error, not appear to succeed")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("openPR is not bounded by ghTimeout: took %s", elapsed)
	}
}

// `git push` is the other network call on the watch path. It is bounded by
// pushTimeout rather than ghTimeout because a push transfers objects and a
// slow one is not a broken one -- but unbounded it would still park the
// watcher forever on a credential prompt nobody will answer.
func TestPushBranchDoesNotHangOnAStalledGit(t *testing.T) {
	fakeBin(t, "git", "#!/bin/sh\nexec sleep 5\n")

	old := pushTimeout
	pushTimeout = 200 * time.Millisecond
	t.Cleanup(func() { pushTimeout = old })

	start := time.Now()
	err := pushBranch(t.TempDir(), "main")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a hung `git push` must report an error, not appear to succeed")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("pushBranch is not bounded by pushTimeout: took %s", elapsed)
	}
}

// fakeBin puts a stub executable at the front of PATH for one test. See the
// `exec sleep` note above for why the body matters.
func fakeBin(t *testing.T, name, script string) {
	t.Helper()
	writeFakeBin(t, name, script)
}

// The ordinary case -- a fast gh -- must not be affected by the timeout
// wiring at all.
func TestGhCommandSucceedsWithinItsTimeout(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinIn(t, dir, "gh", "#!/bin/sh\necho ok\n")

	cmd, cancel := ghCommand(t.TempDir(), "pr", "view", "x")
	defer cancel()

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("a fast gh must succeed, got: %v (%s)", err, out)
	}
	if string(out) != "ok\n" {
		t.Errorf("output = %q", out)
	}
}
