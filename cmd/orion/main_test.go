package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every external command that acts on a repository must be told WHICH
// repository. Orion runs from wherever the user typed the command -- the
// registry exists precisely so a job can be started from outside the project
// -- so inheriting the process's working directory is never right.
//
// This is not hypothetical. openPR accepted a dir, ignored it, and ran gh in
// Orion's cwd. On the first real run that cwd was ~/.claude: the agent
// finished, two commits pushed cleanly, and then the run was marked FAILED
// with "fatal: not a git repository". The work was fine; only the reporting
// of it was wrong, which is the expensive kind of bug because it sends you
// looking at the agent.

// "orion config --help" used to fall through to the interactive credentials
// wizard, which blocks on stdin reading a Jira URL nobody meant to type --
// the only way out was Ctrl-C. A help flag must never be able to start an
// interactive prompt, whatever else is on the command line (OR-132).
func TestConfigHelpPrintsUsageInsteadOfPromptingForAnything(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"agents", "--help"}} {
		got := captureStdout(t, func() { runConfig(args) })
		if !strings.Contains(got, "orion config agents") {
			t.Errorf("args %v: usage not printed, got: %q", args, got)
		}
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// what it wrote. Only safe for code that does not also need real stdin --
// runConfig's --help path returns before touching it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old

	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

func TestThePullRequestIsOpenedInTheWorktreeNotTheUsersCwd(t *testing.T) {
	const dir = "/tmp/orion-worktree"
	cmd, cancel := prCommand(dir, "orion/fcia-6", "title", "body", "develop")
	defer cancel()

	if cmd.Dir != dir {
		t.Fatalf("gh would run in %q, not the worktree %q.\n"+
			"Without cmd.Dir, gh resolves whatever repository the user happens "+
			"to be standing in, or none at all.", cmd.Dir, dir)
	}

	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"pr create", "--head orion/fcia-6", "--base develop"} {
		if !strings.Contains(args, want) {
			t.Errorf("gh invocation is missing %q: %s", want, args)
		}
	}
}

// pushBranch has always been correct, but it is correct by a DIFFERENT
// mechanism (git -C rather than cmd.Dir), and that difference is exactly why
// the openPR bug survived review: the two calls sit next to each other and
// look equally careful. Pinning both means neither can quietly regress to
// depending on the caller's cwd.
func TestThePushNamesItsRepository(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")

	// No remote is configured, so the push must fail -- but it must fail
	// having FOUND the repository. "not a git repository" would mean it
	// looked in the test process's cwd instead of dir.
	err := pushBranch(dir, "main")
	if err == nil {
		t.Fatal("expected a push to a non-existent remote to fail")
	}
	if strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("pushBranch ignored its dir argument and used the caller's cwd: %v", err)
	}
}
