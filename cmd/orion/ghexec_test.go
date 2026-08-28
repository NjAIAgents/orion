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
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 5\necho too-late\n"), 0o755); err != nil {
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
	if string(out) == "too-late\n" {
		t.Fatal("the process was not actually killed before producing output")
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
