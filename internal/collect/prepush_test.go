package collect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The pre-push rebase, against real git (OR-227).
//
// Real repositories for the same reason the rest of this package uses them: a
// fake would confirm that a function was called, and every claim here is about
// what git actually did to a branch -- whether the base's commit is now in it,
// whether the ticket's own work survived, and whether a refusal left the
// branch exactly as it was.

// prepushWorktree builds the situation the work path is in at push time: a
// job worktree holding a committed branch that has never been pushed, and a
// base that moved while the agent was running.
func prepushWorktree(t *testing.T, branch string) (ws *workspace.Workspace, origin, wt string) {
	t.Helper()
	home, _ := bound(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err = workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}

	origin, _ = repos(t)
	// Exactly where AddWorktree puts it, so rebaseSteps names a directory
	// that exists.
	wt = filepath.Join(ws.Dir, "worktrees", strings.ReplaceAll(branch, "/", "-"))
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, wt)
	gitRun(t, wt, "checkout", "--quiet", "-b", branch, "origin/develop")
	return ws, origin, wt
}

func prepushLog(t *testing.T, ws *workspace.Workspace) *events.Log {
	t.Helper()
	log, err := events.Open(events.Path(ws.Dir), events.Event{
		Project: "FCIA", Key: "FCIA-6", Run: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	return log
}

const prepushCfg = `develop`

// The whole point: the branch arrives current, so the first CI run is the one
// that counts rather than one describing a base that no longer exists.
func TestABranchBehindItsBaseIsRebasedBeforeItsFirstPush(t *testing.T) {
	ws, origin, wt := prepushWorktree(t, "orion/fcia-6")
	writeCommit(t, wt, "impl.go", "package x", "the ticket's work")
	before := head(t, wt, "HEAD")

	// Another ticket merges while the agent is still running, which at
	// concurrency 4 is the usual case rather than the unlucky one.
	landOnDevelop(t, origin, "other.txt", "theirs")

	var buf bytes.Buffer
	RebaseBeforePush("FCIA-6", wt, "orion/fcia-6",
		config.Config{VCS: config.VCS{WorkBranch: prepushCfg}}, ws, prepushLog(t, ws), &buf)

	if head(t, wt, "HEAD") == before {
		t.Fatal("the branch was pushed at the base it started from; " +
			"its first CI run would describe a base that has already moved")
	}
	// The base's commit is now in the branch, and the ticket's own work
	// survived the replay. A rebase that dropped the work would also satisfy
	// the check above.
	if err := gitQuiet(wt, "merge-base", "--is-ancestor", "origin/develop", "HEAD"); err != nil {
		t.Errorf("develop's tip is still not in the branch: %v", err)
	}
	if err := gitQuiet(wt, "cat-file", "-e", "HEAD:impl.go"); err != nil {
		t.Errorf("the ticket's own commit did not survive the rebase: %v", err)
	}
	if !strings.Contains(buf.String(), "rebase") {
		t.Errorf("a rewritten branch was not reported: %q", buf.String())
	}
}

// Nothing new on a clean path. A branch already sitting on the tip costs one
// fetch and prints nothing, because a line printed on every run is a line
// nobody reads on the run that matters.
func TestABranchAlreadyOnItsBaseIsLeftAloneAndSaysNothing(t *testing.T) {
	ws, _, wt := prepushWorktree(t, "orion/fcia-6")
	writeCommit(t, wt, "impl.go", "package x", "the ticket's work")
	before := head(t, wt, "HEAD")

	var buf bytes.Buffer
	RebaseBeforePush("FCIA-6", wt, "orion/fcia-6",
		config.Config{VCS: config.VCS{WorkBranch: prepushCfg}}, ws, prepushLog(t, ws), &buf)

	if got := head(t, wt, "HEAD"); got != before {
		t.Errorf("a current branch was rewritten: %s -> %s", before, got)
	}
	if buf.String() != "" {
		t.Errorf("the common case printed something: %q", buf.String())
	}
}

// A conflict must not block the push. It needs a person, and refusing to push
// would hide finished work behind it and leave the operator with nothing to
// look at -- so the branch is left exactly as it was and the caller carries
// on, with the commands conflict.go already prints.
func TestAConflictingRebaseLeavesTheBranchAloneAndPrintsTheCommands(t *testing.T) {
	ws, origin, wt := prepushWorktree(t, "orion/fcia-6")
	writeCommit(t, wt, "shared.txt", "ours", "the ticket's work")
	before := head(t, wt, "HEAD")

	// The same file, different content: git cannot replay one onto the other.
	landOnDevelop(t, origin, "shared.txt", "theirs")

	var buf bytes.Buffer
	RebaseBeforePush("FCIA-6", wt, "orion/fcia-6",
		config.Config{VCS: config.VCS{WorkBranch: prepushCfg}}, ws, prepushLog(t, ws), &buf)

	if got := head(t, wt, "HEAD"); got != before {
		t.Errorf("the branch was changed by a rebase that did not apply: %s -> %s", before, got)
	}
	// An aborted rebase, not a worktree left mid-operation for a person to
	// find.
	if _, err := os.Stat(filepath.Join(wt, ".git", "rebase-merge")); err == nil {
		t.Error("the worktree was left mid-rebase")
	}
	out := buf.String()
	if !strings.Contains(out, "git rebase origin/develop") {
		t.Errorf("the exact commands were not printed: %q", out)
	}
	if !strings.Contains(out, "pushing it as it stands") {
		t.Errorf("the push was not reported as going ahead unrebased: %q", out)
	}
}

// A person holding the worktree by hand outranks this, exactly as on the
// landing path (OR-130). Two processes rewriting one branch is luck, not
// design.
func TestAManuallyLockedWorktreeIsNeverRewritten(t *testing.T) {
	ws, origin, wt := prepushWorktree(t, "orion/fcia-6")
	writeCommit(t, wt, "impl.go", "package x", "the ticket's work")
	before := head(t, wt, "HEAD")
	landOnDevelop(t, origin, "other.txt", "theirs")

	if err := os.WriteFile(filepath.Join(wt, manualLockName), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	RebaseBeforePush("FCIA-6", wt, "orion/fcia-6",
		config.Config{VCS: config.VCS{WorkBranch: prepushCfg}}, ws, prepushLog(t, ws), &buf)

	if got := head(t, wt, "HEAD"); got != before {
		t.Errorf("a branch a person had taken was rewritten underneath them: %s -> %s", before, got)
	}
	if !strings.Contains(buf.String(), "locked for manual work") {
		t.Errorf("the lock was not reported: %q", buf.String())
	}
}

// The base comes from baseOf, the one answer collect already uses (OR-112) --
// never from a branch picked for what it is CALLED. A repository whose work
// branch is `trunk` while an abandoned `develop` still exists is where the
// difference shows: rebasing onto the wrong one succeeds, quietly, and buries
// the change in a large unrelated diff.
func TestTheBaseIsResolvedThroughTheSameCodeCollectUses(t *testing.T) {
	ws, origin, wt := prepushWorktree(t, "orion/fcia-6")
	// An abandoned develop that moves, and the real work branch that does not.
	gitRun(t, wt, "push", "--quiet", "origin", "HEAD:refs/heads/trunk")
	writeCommit(t, wt, "impl.go", "package x", "the ticket's work")
	before := head(t, wt, "HEAD")
	landOnDevelop(t, origin, "other.txt", "theirs")

	var buf bytes.Buffer
	RebaseBeforePush("FCIA-6", wt, "orion/fcia-6",
		config.Config{VCS: config.VCS{WorkBranch: "trunk"}}, ws, prepushLog(t, ws), &buf)

	if got := head(t, wt, "HEAD"); got != before {
		t.Errorf("the branch was rebased onto something other than its configured base "+
			"(%s -> %s); develop is not the base here, trunk is", before, got)
	}
	if err := gitQuiet(wt, "cat-file", "-e", "HEAD:other.txt"); err == nil {
		t.Error("develop's commit was pulled in; the base was chosen by name")
	}
	if buf.String() != "" {
		t.Errorf("a branch current with its real base printed something: %q", buf.String())
	}
}

// No base, nothing to rebase onto. Reported rather than guessed, and above
// all not attempted: baseOf returning nothing is the case that used to end in
// a rebase onto a branch chosen for its name.
func TestNoDeterminableBaseDoesNothingAtAll(t *testing.T) {
	ws, origin, wt := prepushWorktree(t, "orion/fcia-6")
	writeCommit(t, wt, "impl.go", "package x", "the ticket's work")
	before := head(t, wt, "HEAD")
	landOnDevelop(t, origin, "other.txt", "theirs")

	var buf bytes.Buffer
	RebaseBeforePush("FCIA-6", wt, "orion/fcia-6", config.Config{}, ws, prepushLog(t, ws), &buf)

	if got := head(t, wt, "HEAD"); got != before {
		t.Errorf("a branch with no determinable base was rebased anyway: %s -> %s", before, got)
	}
}

// An unreachable remote is a circumstance, not a decision. It degrades to the
// behaviour that existed before this ran at all -- a stale first CI run --
// rather than to a lost push.
func TestAFailedFetchLeavesTheBranchAloneAndSaysSo(t *testing.T) {
	ws, _, wt := prepushWorktree(t, "orion/fcia-6")
	writeCommit(t, wt, "impl.go", "package x", "the ticket's work")
	before := head(t, wt, "HEAD")
	gitRun(t, wt, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	var buf bytes.Buffer
	RebaseBeforePush("FCIA-6", wt, "orion/fcia-6",
		config.Config{VCS: config.VCS{WorkBranch: prepushCfg}}, ws, prepushLog(t, ws), &buf)

	if got := head(t, wt, "HEAD"); got != before {
		t.Errorf("the branch moved despite the fetch failing: %s -> %s", before, got)
	}
	if !strings.Contains(buf.String(), "pushing it as it stands") {
		t.Errorf("the degrade was not reported: %q", buf.String())
	}
}
