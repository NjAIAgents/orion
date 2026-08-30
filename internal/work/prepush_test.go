package work

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The branch arrives current (OR-227).
//
// The run itself is what these assert, not the rebase in isolation: the defect
// was that the push happened at whatever base the branch started from, ten to
// forty minutes earlier, so CI ran once against a base that no longer existed
// and again after the landing pass rebased it. The claim only holds if the
// rebase sits between the last commit and deps.Push, which is a fact about the
// ORDER of the run and can only be checked by running it.

// gitAsks puts a yes/no question to git without failing the test on "no".
func gitAsks(dir string, args ...string) error {
	return exec.Command("git", append([]string{"-C", dir}, args...)...).Run()
}

// originOf finds the bare repository the sandbox was cloned from, so a test
// can land somebody else's commit on develop the way another job would.
func originOf(t *testing.T, home string) string {
	t.Helper()
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	return git(t, ws.RepoDir(), "remote", "get-url", "origin")
}

// landOnDevelop is another ticket merging while this agent is still running.
// At concurrency 4 and forty minutes of wall time, this is the usual case
// rather than the unlucky one.
func landOnDevelop(t *testing.T, origin, file, content string) {
	t.Helper()
	c := filepath.Join(t.TempDir(), "other")
	git(t, t.TempDir(), "clone", "-q", origin, c)
	git(t, c, "checkout", "-q", "develop")
	if err := os.WriteFile(filepath.Join(c, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, c, "add", ".")
	git(t, c, "commit", "-q", "-m", "another ticket lands")
	git(t, c, "push", "-q", "origin", "develop")
}

// commits is the agent: it writes a file and commits it, as a real one does.
func commits(t *testing.T, file, content string) func(*workspace.Workspace) error {
	return func(ws *workspace.Workspace) error {
		if err := os.WriteFile(filepath.Join(ws.RepoDir(), file), []byte(content), 0o644); err != nil {
			return err
		}
		git(t, ws.RepoDir(), "add", ".")
		git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
		return nil
	}
}

// The branch that reaches the remote sits on top of develop's current tip, so
// the checks it triggers describe what merging it would actually produce.
// Before this, the first run was stale on arrival and a second one followed
// once the landing pass rebased it.
func TestTheBranchIsRebasedBeforeItsFirstPush(t *testing.T) {
	home := project(t, cfg)
	origin := originOf(t, home)
	impl := commits(t, "impl.go", "package x\n")
	var pushedHead, pushedDir string
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, _ supervisor.Options) (*supervisor.Result, error) {
				if err := impl(ws); err != nil {
					return nil, err
				}
				landOnDevelop(t, origin, "other.txt", "theirs")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(dir, branch string) error {
				// Read at the moment of the push, not after the run: what CI
				// runs on is what the branch was HERE.
				pushedDir, pushedHead = dir, git(t, dir, "rev-parse", "HEAD")
				return nil
			},
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://github.com/x/y/pull/4", nil
			},
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q: %v", res[0].Outcome, res[0].Err)
	}
	if pushedHead == "" {
		t.Fatal("nothing was pushed")
	}
	// develop's newer commit is in the branch that was pushed. Fetched again
	// first: asking against a remote-tracking ref this worktree never updated
	// would answer about the base as it was, which is the very mistake under
	// test.
	git(t, pushedDir, "fetch", "-q", "origin")
	if err := gitAsks(pushedDir, "merge-base", "--is-ancestor", "origin/develop", "HEAD"); err != nil {
		t.Errorf("the branch was pushed behind develop, so its first CI run "+
			"describes a base that has already moved: %v", err)
	}
	if err := gitAsks(pushedDir, "cat-file", "-e", "HEAD:other.txt"); err != nil {
		t.Errorf("the other ticket's commit is not in the pushed branch: %v", err)
	}
	// ...and so is the ticket's own work. A rebase that dropped it would
	// satisfy both checks above.
	if err := gitAsks(pushedDir, "cat-file", "-e", "HEAD:impl.go"); err != nil {
		t.Errorf("the ticket's commit did not survive the rebase: %v", err)
	}
}

// Nothing new on a clean path. A run whose base never moved costs one extra
// fetch, rewrites nothing, and says nothing about rebasing.
func TestARunWhoseBaseNeverMovedIsPushedUntouchedAndSilently(t *testing.T) {
	home := project(t, cfg)
	impl := commits(t, "impl.go", "package x\n")
	var committed, pushed string
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, _ supervisor.Options) (*supervisor.Result, error) {
				if err := impl(ws); err != nil {
					return nil, err
				}
				committed = git(t, ws.RepoDir(), "rev-parse", "HEAD")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(dir, branch string) error {
				pushed = git(t, dir, "rev-parse", "HEAD")
				return nil
			},
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://github.com/x/y/pull/4", nil
			},
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q: %v", res[0].Outcome, res[0].Err)
	}
	if pushed != committed {
		t.Errorf("a current branch was rewritten before pushing: %s -> %s", committed, pushed)
	}
	if strings.Contains(out.String(), "rebase") {
		t.Errorf("the common case said something about rebasing:\n%s", out.String())
	}
}

// A conflict must not block the push. Refusing would hide finished work behind
// an unresolved conflict and leave the operator with nothing to look at, so
// the original branch is pushed, the pull request opens, and the conflict is
// reported with the commands that resolve it.
func TestAConflictingRebaseStillPushesAndOpensThePullRequest(t *testing.T) {
	home := project(t, cfg)
	origin := originOf(t, home)
	impl := commits(t, "shared.txt", "ours\n")
	var committed, pushed string
	prOpened := false
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, _ supervisor.Options) (*supervisor.Result, error) {
				if err := impl(ws); err != nil {
					return nil, err
				}
				committed = git(t, ws.RepoDir(), "rev-parse", "HEAD")
				// The same file, different content: git cannot replay one onto
				// the other, and only a person can decide what it should say.
				landOnDevelop(t, origin, "shared.txt", "theirs\n")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(dir, branch string) error {
				pushed = git(t, dir, "rev-parse", "HEAD")
				return nil
			},
			OpenPR: func(string, string, string, string, string) (string, error) {
				prOpened = true
				return "https://github.com/x/y/pull/4", nil
			},
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("a conflict blocked the push; finished work is now invisible: %q %v",
			res[0].Outcome, res[0].Err)
	}
	if !prOpened {
		t.Error("no pull request was opened, so there is nothing for the operator to look at")
	}
	if pushed != committed {
		t.Errorf("the branch changed despite the rebase not applying: %s -> %s", committed, pushed)
	}
	if !strings.Contains(out.String(), "git rebase origin/develop") {
		t.Errorf("the conflict was not reported with the commands that fix it:\n%s", out.String())
	}
}

// A remote that cannot be reached is a circumstance, not a decision. It
// degrades to what happened before this existed -- a stale first CI run --
// rather than failing a run whose work is finished.
func TestAFailedFetchDoesNotFailTheRun(t *testing.T) {
	home := project(t, cfg)
	impl := commits(t, "impl.go", "package x\n")
	var committed, pushed string
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, _ supervisor.Options) (*supervisor.Result, error) {
				if err := impl(ws); err != nil {
					return nil, err
				}
				committed = git(t, ws.RepoDir(), "rev-parse", "HEAD")
				git(t, ws.RepoDir(), "remote", "set-url", "origin",
					filepath.Join(t.TempDir(), "gone.git"))
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(dir, branch string) error {
				pushed = git(t, dir, "rev-parse", "HEAD")
				return nil
			},
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://github.com/x/y/pull/4", nil
			},
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("an unreachable remote failed a finished run: %q %v", res[0].Outcome, res[0].Err)
	}
	if pushed != committed {
		t.Errorf("the branch moved despite the fetch failing: %s -> %s", committed, pushed)
	}
}
