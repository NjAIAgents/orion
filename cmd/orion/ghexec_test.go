package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// OR-128: a `gh` that hangs must not be able to block the caller forever.
// A fake `gh` that sleeps far longer than the (shrunk-for-the-test) timeout
// proves the context actually cuts it off, rather than merely existing.
func TestGhCommandEnforcesItsTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gh")
	// `exec sleep` rather than a plain `sleep` line: the real `gh` binary is
	// a single process (it does not shell out and fork), so `exec` here
	// makes the fake match that shape -- the interpreter REPLACES itself
	// with sleep rather than forking a child. Without `exec`, sh forks
	// sleep as a genuine child; killing the parent orphans it, and it keeps
	// the stdout pipe open until its own 5s elapses regardless of the
	// context firing -- a fixture artifact, not something a real gh hang
	// would do.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

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

// The ordinary case -- a fast gh -- must not be affected by the timeout
// wiring at all.
func TestGhCommandSucceedsWithinItsTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

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
