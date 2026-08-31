package collect

import (
	"path/filepath"
	"strings"
	"testing"
)

// readDiff is the evidence the intent question and the mechanical checks are
// both built from, and none of the acceptance criteria around it were
// exercised anywhere else in this package: three-dot semantics, the fetch
// that keeps the remote-tracking refs current, --stat and --name-only as the
// source of the file list, and the 60k truncation the model is told about.
//
// Fixtures below build a real bare origin plus a clone with an "origin"
// remote, because readDiff shells out to git and a stub cannot stand in for
// what `git diff A...B` actually does.

// Three dots, not two: the diff has to be against the point where the branch
// diverged, not against develop's current tip. A commit that lands on
// develop AFTER the branch was cut must not appear, or the model would be
// asked whether the ticket's criteria show up in somebody else's work (OR-112
// reasoning, applied here rather than to staleness).
func TestReadDiffIsThreeDotNotTwoDot(t *testing.T) {
	origin := t.TempDir()
	gitRun(t, origin, "init", "--quiet", "--bare", "--initial-branch=develop")

	seed := t.TempDir()
	gitRun(t, seed, "init", "--quiet", "--initial-branch=develop")
	write(t, seed, "base.txt", "base\n")
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "--quiet", "-m", "base")
	gitRun(t, seed, "remote", "add", "origin", origin)
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "develop")

	gitRun(t, seed, "checkout", "--quiet", "-b", "orion/x-2")
	write(t, seed, "branch.txt", "what the ticket added\n")
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "--quiet", "-m", "the ticket's change")
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "orion/x-2")

	// Lands on develop AFTER the branch diverged. A two-dot diff (or a diff
	// against develop's moving tip) would fold this in; three-dot must not.
	gitRun(t, seed, "checkout", "--quiet", "develop")
	write(t, seed, "unrelated.txt", "somebody else's work\n")
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "--quiet", "-m", "unrelated, landed later")
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "develop")

	clone := filepath.Join(t.TempDir(), "repo")
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, clone)

	d := readDiff(clone, "develop", "orion/x-2")

	if d.Unreadable != "" {
		t.Fatalf("readDiff could not read the diff: %s", d.Unreadable)
	}
	if !has(d.Files, "branch.txt") {
		t.Errorf("files = %v, want the ticket's own file", d.Files)
	}
	if has(d.Files, "unrelated.txt") {
		t.Errorf("files = %v, includes a commit that landed on develop after the branch "+
			"diverged -- this is a two-dot diff wearing a three-dot comment", d.Files)
	}
	if !strings.Contains(d.Stat, "branch.txt") {
		t.Errorf("stat = %q, want it to name the changed file", d.Stat)
	}
	if !strings.Contains(d.Patch, "what the ticket added") {
		t.Errorf("patch does not carry the ticket's own change:\n%s", d.Patch)
	}
	if strings.Contains(d.Patch, "somebody else's work") {
		t.Errorf("patch carries a commit outside the branch's own change:\n%s", d.Patch)
	}
}

// A patch beyond 60k chars is cut, and the cut is declared -- a criterion the
// model cannot find because it was truncated is missing evidence, not
// missing work, and Diff.Truncated is the only thing that tells the model
// which one it is looking at.
func TestReadDiffTruncatesALargePatchAndSaysSo(t *testing.T) {
	origin := t.TempDir()
	gitRun(t, origin, "init", "--quiet", "--bare", "--initial-branch=develop")

	seed := t.TempDir()
	gitRun(t, seed, "init", "--quiet", "--initial-branch=develop")
	write(t, seed, "base.txt", "base\n")
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "--quiet", "-m", "base")
	gitRun(t, seed, "remote", "add", "origin", origin)
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "develop")

	gitRun(t, seed, "checkout", "--quiet", "-b", "orion/x-2")
	// Comfortably over maxPatch once it is rendered as a diff (every line
	// prefixed with "+" and newlines of its own).
	write(t, seed, "big.txt", strings.Repeat("a line that is part of a very large diff\n", 3000))
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "--quiet", "-m", "a large change")
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "orion/x-2")

	clone := filepath.Join(t.TempDir(), "repo")
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, clone)

	d := readDiff(clone, "develop", "orion/x-2")

	if d.Unreadable != "" {
		t.Fatalf("readDiff could not read the diff: %s", d.Unreadable)
	}
	if !d.Truncated {
		t.Fatal("a patch over maxPatch chars was not marked truncated")
	}
	if len(d.Patch) != maxPatch {
		t.Errorf("patch length = %d, want exactly maxPatch (%d)", len(d.Patch), maxPatch)
	}
}

// A repository readDiff cannot fetch from is unreadable, not evidence of
// anything about the branch -- and it has to say why, or the intent prompt
// silently gets an empty diff and treats the ticket as having no changes.
func TestReadDiffThatCannotFetchIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "--initial-branch=develop")
	gitRun(t, dir, "commit", "--quiet", "--allow-empty", "-m", "base")
	// No "origin" remote at all: the fetch this depends on has nothing to
	// reach.

	d := readDiff(dir, "develop", "orion/x-2")

	if d.Unreadable == "" {
		t.Fatal("a repository with no origin remote was read as though the diff succeeded")
	}
	if d.Patch != "" || len(d.Files) != 0 {
		t.Errorf("an unreadable diff still carried patch/files data: %+v", d)
	}
}
