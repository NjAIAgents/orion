package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Real git, for the same reason staleness_test.go uses it: the value of this
// code is that it performs an operation git has already confirmed, and a fake
// git would confirm whatever the test wanted. A rebase with its arguments the
// wrong way round, a lease that protects nothing, an abort that leaves the
// worktree mid-rebase -- all of those compile, run, and look fine against a
// mock. Only a real repository can say the branch actually moved, and that
// the one it was supposed to protect did not.

func writeCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", file)
	gitRun(t, dir, "commit", "-m", msg)
}

func head(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitLine(dir, "rev-parse", ref)
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, dir, err)
	}
	return out
}

// checkout clones origin and checks the branch out, which is the shape of the
// job worktree the rebase runs in.
func checkout(t *testing.T, origin, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wt")
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, dir)
	gitRun(t, dir, "checkout", "--quiet", branch)
	return dir
}

// landOnDevelop is another ticket merging while this one waited on CI -- the
// event that makes every other open branch stale.
func landOnDevelop(t *testing.T, origin, file, content string) {
	t.Helper()
	c := filepath.Join(t.TempDir(), "other")
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, c)
	writeCommit(t, c, file, content, "another ticket lands")
	gitRun(t, c, "push", "--quiet", "origin", "develop")
}

// The whole point: behind, clean, and nobody had to type anything.
func TestABranchThatIsBehindAndCleanIsRebasedAndPushed(t *testing.T) {
	origin, clone := repos(t)
	wt := checkout(t, origin, "orion/x-1")
	before := head(t, wt, "HEAD")
	landOnDevelop(t, origin, "other.txt", "theirs")

	if err := rebaseOnto(wt, "develop", "orion/x-1", before); err != nil {
		t.Fatalf("a clean rebase must succeed: %v", err)
	}

	// The remote branch is what CI will run against, so that is what has to
	// have moved -- a rebase that only happened locally fixes nothing.
	if ok, known := upToDate(clone, "develop", "orion/x-1"); !known || !ok {
		t.Errorf("after rebasing, the branch is still behind (ok=%v known=%v); "+
			"the checks would re-run against the same moved base", ok, known)
	}
	if head(t, wt, "HEAD") == before {
		t.Error("the local branch was not replayed")
	}
	// The ticket's own work must still be there. A rebase that dropped it
	// would also report as up to date.
	log, err := gitLine(wt, "log", "--format=%s", "origin/develop..HEAD")
	if err != nil || !strings.Contains(log, "the ticket's work") {
		t.Errorf("the ticket's commit did not survive the rebase: %q (%v)", log, err)
	}
}

// The lease is the whole safety argument for doing this automatically. A
// human push that landed between the forge reporting a commit and Orion
// pushing must abort the operation, not be overwritten by it.
func TestALeaseBrokenByAHumanPushAbortsWithoutDestroyingIt(t *testing.T) {
	origin, _ := repos(t)
	wt := checkout(t, origin, "orion/x-1")
	stale := head(t, wt, "HEAD")
	landOnDevelop(t, origin, "other.txt", "theirs")

	// Somebody pushes to the branch after the commit Orion read from gh.
	human := checkout(t, origin, "orion/x-1")
	writeCommit(t, human, "by-hand.txt", "a person's work", "a human pushes")
	gitRun(t, human, "push", "--quiet", "origin", "orion/x-1")
	theirs := head(t, human, "HEAD")

	err := rebaseOnto(wt, "develop", "orion/x-1", stale)
	if err == nil {
		t.Fatal("the push went through on a lease that should have been broken")
	}

	if got := head(t, origin, "refs/heads/orion/x-1"); got != theirs {
		t.Errorf("the human's commit was overwritten: remote is %s, theirs was %s", got, theirs)
	}
	// And locally: a rebase that only exists in the worktree is a worse thing
	// to leave behind than the staleness it was fixing.
	if got := head(t, wt, "HEAD"); got != stale {
		t.Errorf("the local branch was left rewritten after a failed push: %s, want %s", got, stale)
	}
}

// Behind AND conflicting is a person's, and a rebase that stops must leave
// the worktree exactly as it found it -- never mid-rebase.
func TestARebaseThatConflictsChangesNothing(t *testing.T) {
	origin := t.TempDir()
	gitRun(t, origin, "init", "--quiet", "--bare", "--initial-branch=develop")
	seed := t.TempDir()
	gitRun(t, seed, "init", "--quiet", "--initial-branch=develop")
	writeCommit(t, seed, "shared.txt", "original\n", "base")
	gitRun(t, seed, "remote", "add", "origin", origin)
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "develop")
	gitRun(t, seed, "checkout", "--quiet", "-b", "orion/x-1")
	writeCommit(t, seed, "shared.txt", "the ticket's version\n", "the ticket's work")
	gitRun(t, seed, "push", "--quiet", "-u", "origin", "orion/x-1")

	wt := checkout(t, origin, "orion/x-1")
	before := head(t, wt, "HEAD")
	landOnDevelop(t, origin, "shared.txt", "somebody else's version\n")

	if err := rebaseOnto(wt, "develop", "orion/x-1", before); err == nil {
		t.Fatal("a conflicting rebase must fail rather than resolve anything")
	}
	if got := head(t, wt, "HEAD"); got != before {
		t.Errorf("the branch moved: %s, want %s", got, before)
	}
	// Mid-rebase, HEAD is detached and this reports the commit rather than
	// the branch. Handing somebody a worktree in that state is worse than
	// never having tried.
	if cur, err := gitLine(wt, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || cur != "orion/x-1" {
		t.Errorf("the worktree was left mid-rebase: on %q (%v)", cur, err)
	}
}

// Uncommitted work is somebody standing in the worktree. Refuse rather than
// replay commits around their changes.
func TestADirtyWorktreeIsNotRebased(t *testing.T) {
	origin, _ := repos(t)
	wt := checkout(t, origin, "orion/x-1")
	before := head(t, wt, "HEAD")
	landOnDevelop(t, origin, "other.txt", "theirs")
	writeCommit(t, wt, "wip.txt", "committed\n", "so the file is tracked")
	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty := head(t, wt, "HEAD")

	if err := rebaseOnto(wt, "develop", "orion/x-1", before); err == nil {
		t.Fatal("a worktree with uncommitted changes must not be rebased")
	}
	if got := head(t, wt, "HEAD"); got != dirty {
		t.Error("the branch was rewritten under somebody's uncommitted work")
	}
	b, err := os.ReadFile(filepath.Join(wt, "wip.txt"))
	if err != nil || string(b) != "in progress\n" {
		t.Errorf("the uncommitted change was lost: %q (%v)", b, err)
	}
}

// Nothing to lease against is nothing to push safely under.
func TestWithoutTheForgesCommitThereIsNoLeaseAndNoRebase(t *testing.T) {
	origin, _ := repos(t)
	wt := checkout(t, origin, "orion/x-1")
	before := head(t, wt, "HEAD")
	landOnDevelop(t, origin, "other.txt", "theirs")

	if err := rebaseOnto(wt, "develop", "orion/x-1", ""); err == nil {
		t.Fatal("a force-push with no lease value must never be attempted")
	}
	if got := head(t, wt, "HEAD"); got != before {
		t.Error("the branch moved anyway")
	}
}

// ---------------------------------------------------------------------------
// The whole pass, through Run: a stale ticket in ci-wait, a real worktree, and
// whatever collect decides to do about it.

// staleTicket wires a registered project whose job worktree holds a branch
// that is behind its base, and returns the home, the source checkout and the
// branch's current commit as the forge would report it.
func staleTicket(t *testing.T) (home, source, origin, wtDir, headSHA string) {
	t.Helper()
	home, source = bound(t)

	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}

	origin, _ = repos(t)
	// Exactly where worktreeOrRepo looks: the job's worktree for this branch.
	wtDir = filepath.Join(ws.Dir, "worktrees", "orion-fcia-6")
	if err := os.MkdirAll(filepath.Dir(wtDir), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, wtDir)
	gitRun(t, wtDir, "checkout", "--quiet", "orion/x-1")
	// The branch collect will look for is derived from the ticket key.
	gitRun(t, wtDir, "branch", "--quiet", "-m", "orion/fcia-6")
	gitRun(t, wtDir, "push", "--quiet", "-u", "origin", "orion/fcia-6")
	headSHA = head(t, wtDir, "HEAD")

	landOnDevelop(t, origin, "other.txt", "theirs")
	return home, source, origin, wtDir, headSHA
}

func TestAStaleTicketIsRebasedAndReturnedToCIWait(t *testing.T) {
	home, _, origin, wtDir, sha := staleTicket(t)
	jira := newTracker()

	res, out, _ := run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1", Detail: "3 passed",
	}, Options{})

	if strings.Contains(out, "git push --force-with-lease") {
		t.Errorf("three commands were printed for a human to type:\n%s", out)
	}
	if !strings.Contains(out, "rebased and pushed") {
		t.Errorf("the rebase must be reported, got:\n%s", out)
	}
	// Pending, not stale: the checks are running again, against what would
	// actually be merged.
	if res[0].Verdict != VerdictPending {
		t.Errorf("verdict = %q, want pending", res[0].Verdict)
	}
	if !res[0].Changed {
		t.Error("a force-push is a change")
	}
	// The ticket keeps ci-wait, so the next pass reads the new run's verdict.
	if strings.Contains(strings.Join(jira.removed["FCIA-6"], ","), "ci-wait") {
		t.Error("released from ci-wait; the re-run would then be read by nothing")
	}
	// The remote is what CI reads.
	if got := head(t, origin, "refs/heads/orion/fcia-6"); got == sha {
		t.Error("the branch on the remote did not move")
	}
	if ok, known := upToDate(wtDir, "develop", "orion/fcia-6"); !known || !ok {
		t.Errorf("still behind after the pass (ok=%v known=%v)", ok, known)
	}
	if !strings.Contains(strings.Join(jira.comments["FCIA-6"], " "), "rebased it onto") {
		t.Errorf("the rebase must be on the ticket: %v", jira.comments["FCIA-6"])
	}
}

// The escape hatch. Somebody running Orion against a repository they do not
// own may not want it force-pushing anything, however safely.
func TestAutoRebaseOffKeepsTheOldBehaviour(t *testing.T) {
	home, source, origin, _, sha := staleTicket(t)
	if err := os.WriteFile(filepath.Join(source, "orion.json"),
		[]byte(`{"collect":{"auto_rebase":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	jira := newTracker()

	res, out, _ := run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if res[0].Verdict != VerdictStale {
		t.Errorf("verdict = %q, want stale", res[0].Verdict)
	}
	if !strings.Contains(out, "git push --force-with-lease") {
		t.Errorf("with the automation off, the commands must still be printed:\n%s", out)
	}
	if got := head(t, origin, "refs/heads/orion/fcia-6"); got != sha {
		t.Error("the branch was pushed with auto_rebase off")
	}
}

// A ticket already rebased to the cap is in a queue moving faster than it can
// land. A third push absorbs that silently; a person's attention is the
// useful output.
func TestTheCapEscalatesRatherThanRebasingForever(t *testing.T) {
	home, _, origin, wtDir, sha := staleTicket(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxAutoRebases; i++ {
		if _, err := countRebase(ws.Dir, "FCIA-6"); err != nil {
			t.Fatal(err)
		}
	}
	jira := newTracker()

	res, out, _ := run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if res[0].Verdict != VerdictStale {
		t.Errorf("verdict = %q, want stale once the cap is reached", res[0].Verdict)
	}
	if !strings.Contains(out, "leaving this one to you") {
		t.Errorf("hitting the cap must escalate in words:\n%s", out)
	}
	if !strings.Contains(out, "git push --force-with-lease") {
		t.Errorf("and hand over the commands:\n%s", out)
	}
	if got := head(t, origin, "refs/heads/orion/fcia-6"); got != sha {
		t.Error("it rebased anyway, past its own bound")
	}
	_ = wtDir
}

// A dry run that force-pushes is not a dry run.
func TestDryRunSaysWhatItWouldRebaseAndPushesNothing(t *testing.T) {
	home, _, origin, _, sha := staleTicket(t)
	jira := newTracker()

	_, out, _ := run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{DryRun: true})

	if !strings.Contains(out, "would") || !strings.Contains(out, "rebase") {
		t.Errorf("a dry run must say what it would do:\n%s", out)
	}
	if got := head(t, origin, "refs/heads/orion/fcia-6"); got != sha {
		t.Error("a dry run pushed")
	}
	if len(jira.comments["FCIA-6"]) != 0 {
		t.Errorf("a dry run commented: %v", jira.comments["FCIA-6"])
	}
}
