package collect

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real git repositories, not a mock.
//
// The whole value of this check is that it asks git a question git is good
// at. A fake that returns whatever the test wants would verify the plumbing
// and nothing about the question, and the question is the part that has to be
// right -- `merge-base --is-ancestor` with the arguments the wrong way round
// is still a program that compiles, runs, and answers confidently.

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commit(t *testing.T, dir, file, msg string) {
	t.Helper()
	gitRun(t, dir, "commit", "--allow-empty", "-m", msg)
}

// origin holds develop and a branch; clone is what collect would inspect.
// The bare origin is returned too: a second clone must come from IT, because
// git refuses a push to a branch checked out in a non-bare repository.
func repos(t *testing.T) (origin, clone string) {
	t.Helper()
	origin = t.TempDir()
	gitRun(t, origin, "init", "--quiet", "--bare", "--initial-branch=develop")

	seed := t.TempDir()
	gitRun(t, seed, "init", "--quiet", "--initial-branch=develop")
	commit(t, seed, "", "base")
	gitRun(t, seed, "remote", "add", "origin", origin)
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "develop")

	// A feature branch cut from develop, as AddWorktree would.
	gitRun(t, seed, "checkout", "--quiet", "-b", "orion/x-1")
	commit(t, seed, "", "the ticket's work")
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "orion/x-1")

	clone = filepath.Join(t.TempDir(), "repo")
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, clone)
	return origin, clone
}

func TestABranchCutFromTheCurrentTipIsUpToDate(t *testing.T) {
	_, clone := repos(t)

	ok, known := upToDate(clone, "develop", "orion/x-1")
	if !known {
		t.Fatal("git could not answer a question it should have answered")
	}
	if !ok {
		t.Error("a branch containing develop's tip was reported as behind")
	}
}

// The case that matters: another ticket lands on develop after this branch
// was cut. Nothing about the branch changed, and its green checks stopped
// describing what merging it would produce.
func TestABranchIsBehindOnceItsBaseMoves(t *testing.T) {
	origin, clone := repos(t)

	// Somebody else's ticket lands on develop, from a separate clone of the
	// SAME origin -- which is what a second Orion job actually does.
	other := t.TempDir()
	gitRun(t, other, "clone", "--quiet", origin, filepath.Join(other, "c"))
	c := filepath.Join(other, "c")
	gitRun(t, c, "checkout", "--quiet", "develop")
	commit(t, c, "", "another ticket lands")
	gitRun(t, c, "push", "--quiet", "origin", "develop")

	// The branch has not changed. Its checks have not changed. What changed
	// is underneath it -- which is precisely the situation nothing detected.
	ok, known := upToDate(clone, "develop", "orion/x-1")
	if !known {
		t.Fatal("git could not answer")
	}
	if ok {
		t.Error("a branch whose base moved was reported as up to date; " +
			"this is the case that merges an untested combination")
	}
}

// An unanswerable question is not a stale branch.
//
// A missing directory, an unfetched remote, a branch that does not exist:
// none of those mean the merge is unsafe, and reporting them as staleness
// would block every merge in a repository this code merely failed to read.
// The gate exists to catch one detectable situation, not to be the last word.
func TestAnUnreadableRepositoryIsUnknownNotStale(t *testing.T) {
	cases := []struct {
		name, dir, base, branch string
	}{
		{"no directory", "/nonexistent/repo", "develop", "orion/x-1"},
		{"empty arguments", "", "", ""},
		{"branch that does not exist", mustClone(t), "develop", "orion/never"},
		{"base that does not exist", mustClone(t), "nosuchbase", "orion/x-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, known := upToDate(c.dir, c.base, c.branch)
			if known {
				t.Errorf("claimed to know (ok=%v); an unanswerable question must be unknown", ok)
			}
			if ok {
				t.Error("reported up to date without being able to check")
			}
		})
	}
}

// The wording is not the conflict wording, deliberately: this refuses a merge
// git would happily perform, and telling somebody their cleanly-mergeable
// branch "conflicts" makes the tool look confused at its most careful moment.
func TestTheStaleMessageDoesNotClaimAConflict(t *testing.T) {
	msg := staleBranch("FCIA-10", "orion/fcia-10", "develop")
	if strings.Contains(msg, "conflict") {
		t.Errorf("a stale branch is not a conflict: %q", msg)
	}
	for _, want := range []string{"FCIA-10", "orion/fcia-10", "develop", "behind"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must name %q: %q", want, msg)
		}
	}
}

func mustClone(t *testing.T) string {
	t.Helper()
	_, clone := repos(t)
	return clone
}
