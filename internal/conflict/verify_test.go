package conflict

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// twoSides builds a repository where both branches edit the same file, and
// returns base, ours and theirs. The resolution is left to the caller,
// because the resolution is what is under test.
func twoSides(t *testing.T) (dir, base, ours, theirs string) {
	t.Helper()
	dir = t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@example.com")
	run(t, dir, "config", "user.name", "T")
	// The hooks this repository installs would run on these commits and are
	// irrelevant to what is being tested.
	run(t, dir, "config", "core.hooksPath", "/dev/null")

	write(t, dir, "shared.txt", "one\ntwo\nthree\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "base")
	base = run(t, dir, "rev-parse", "HEAD")

	run(t, dir, "checkout", "-q", "-b", "ours")
	write(t, dir, "shared.txt", "one\nOURS\nthree\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "ours")
	ours = run(t, dir, "rev-parse", "HEAD")

	run(t, dir, "checkout", "-q", base)
	run(t, dir, "checkout", "-q", "-b", "theirs")
	write(t, dir, "shared.txt", "one\ntwo\nTHEIRS\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "theirs")
	theirs = run(t, dir, "rev-parse", "HEAD")

	// The resolution branch starts from THEIRS, which is what a rebase of
	// ours onto theirs actually looks like. Starting it from ours would make
	// "take ours wholesale" an empty commit, and the case worth testing --
	// silently discarding a side -- would not even be expressible.
	run(t, dir, "checkout", "-q", "-b", "resolved", theirs)
	return dir, base, ours, theirs
}

// The case the whole package exists for. Both sides edited the file, the
// resolution kept only one, and the tree is perfectly valid -- it would
// build, vet and pass tests. Only comparing against both parents finds it.
func TestResolutionThatDropsOneSideIsCaught(t *testing.T) {
	dir, base, ours, theirs := twoSides(t)

	// "Resolve" by taking ours wholesale, which silently discards THEIRS.
	write(t, dir, "shared.txt", "one\nOURS\nthree\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "resolve")

	r, err := Verify(dir, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if r.Safe() {
		t.Fatal("a resolution that discarded one side entirely was reported as safe; " +
			"this is the exact failure that passed a green build on 2026-08-29")
	}
	if len(r.MatchedOneSide) != 1 || r.MatchedOneSide[0].File != "shared.txt" {
		t.Fatalf("did not name the dropped file: %+v", r.MatchedOneSide)
	}
	if !strings.Contains(r.MatchedOneSide[0].Why, "not in it") {
		t.Errorf("finding does not state the claim to check: %q", r.MatchedOneSide[0].Why)
	}
}

// A genuine resolution keeps both edits, and must not be flagged. Without
// this the check would fire on every merge and be switched off.
func TestResolutionKeepingBothSidesIsSafe(t *testing.T) {
	dir, base, ours, theirs := twoSides(t)

	write(t, dir, "shared.txt", "one\nOURS\nTHEIRS\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "resolve")

	r, err := Verify(dir, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Safe() {
		t.Fatalf("a resolution keeping both edits was flagged: %+v", r.Findings())
	}
}

func TestConflictMarkersAreCaught(t *testing.T) {
	dir, base, ours, theirs := twoSides(t)

	write(t, dir, "shared.txt", "one\n<<<<<<< HEAD\nOURS\n=======\nTHEIRS\n>>>>>>> theirs\nthree\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "half-resolved")

	r, err := Verify(dir, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Markers) == 0 {
		t.Fatal("committed conflict markers were not detected")
	}
	if r.Safe() {
		t.Error("a tree with conflict markers was reported as safe")
	}
}

func TestMergeToolLitterIsCaught(t *testing.T) {
	dir, base, ours, theirs := twoSides(t)

	write(t, dir, "shared.txt", "one\nOURS\nTHEIRS\n")
	write(t, dir, "shared.txt.orig", "one\ntwo\nthree\n")
	run(t, dir, "add", "shared.txt")
	run(t, dir, "commit", "-q", "-m", "resolve")

	r, err := Verify(dir, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Litter) == 0 {
		t.Fatal("a .orig file left in the worktree was not reported")
	}
}

// A file only one side touched had no conflict to resolve, so matching that
// side is the change being carried through, not a loss.
func TestFileOnlyOneSideTouchedIsNotFlagged(t *testing.T) {
	dir, base, ours, theirs := twoSides(t)

	write(t, dir, "solo.txt", "only ours\n")
	write(t, dir, "shared.txt", "one\nOURS\nTHEIRS\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "resolve")

	r, err := Verify(dir, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Safe() {
		t.Fatalf("flagged a file that only one side changed: %+v", r.Findings())
	}
}

// Deleting a file both sides were editing is a larger claim than dropping a
// hunk, and must never pass silently.
func TestDeletingAContestedFileIsFlagged(t *testing.T) {
	dir, base, ours, theirs := twoSides(t)

	if err := os.Remove(filepath.Join(dir, "shared.txt")); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "resolve by deletion")

	r, err := Verify(dir, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if r.Safe() {
		t.Fatal("deleting a file both sides were changing was reported as safe")
	}
}
