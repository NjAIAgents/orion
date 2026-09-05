package collect

// One property of the plan-conformance pass (OR-158) that conform_test.go
// does not exercise: it runs immediately after done triage, ON THE SAME
// GREEN RUN, off the diff done triage already fetched -- not a second one it
// goes and gets for itself. Two fetches of the same branch to answer two
// questions about the same commit would be paying twice for one fact, and
// worse, the two answers could then be about different commits.
//
// Proved here by making "git ... fetch ... origin" for this ticket's own
// checkout observable: a fake git on PATH logs every invocation that names
// "fetch" before handing the call on to the real binary. If reviewConformance
// read its own diff rather than reusing done triage's, this branch's job
// worktree would show two fetches for one collect pass; it shows one.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/done"
	"github.com/orion-sdlc/orion/internal/fakebin"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fetchSpy puts a fake git on PATH that forwards every call to the real
// binary, and additionally logs the ones that name "fetch" -- one line per
// call, arguments and all -- so a test can count how many times a given
// checkout was fetched.
func fetchSpy(t *testing.T) (logPath string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH to wrap")
	}
	log := filepath.Join(t.TempDir(), "fetches.log")
	t.Setenv("REALGIT", fakebin.ShPath(real))
	t.Setenv("FAKEGIT_FETCH_LOG", fakebin.ShPath(log))
	fakebin.Install(t, t.TempDir(), "git", `#!/bin/bash
for a in "$@"; do
  if [ "$a" = "fetch" ]; then
    echo "$@" >> "$FAKEGIT_FETCH_LOG"
    break
  fi
done
exec "$REALGIT" "$@"
`)
	return log
}

// fetchesAgainst counts the logged fetch calls that named dir as their
// checkout -- git -C <dir> fetch ... -- so a fetch against some other
// directory (the shared sandbox clone, another ticket) does not get counted
// as a fetch of this one.
func fetchesAgainst(t *testing.T, logPath, dir string) int {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		for _, field := range strings.Fields(line) {
			if field == dir {
				n++
				break
			}
		}
	}
	return n
}

// The pass reuses the diff done triage already fetched rather than fetching
// its own: one green run of `orion collect` produces exactly one fetch of
// the ticket's checkout, not two.
func TestConformanceReusesDoneTriagesDiffRatherThanRefetching(t *testing.T) {
	home, ws, branch := boundWithAJobWorktree(t)
	dir := jobTree(ws, branch)

	logPath := fetchSpy(t)

	origin := t.TempDir()
	// HEAD is set after init rather than with --initial-branch, which git
	// only learned in 2.28 and which the fake git wrapping this test does
	// not pass through cleanly on every platform (OR-346). symbolic-ref is
	// as old as bare repositories are.
	gitRun(t, origin, "init", "--quiet", "--bare")
	gitRun(t, origin, "symbolic-ref", "HEAD", "refs/heads/develop")
	gitRun(t, dir, "remote", "add", "origin", origin)
	gitRun(t, dir, "push", "--quiet", "origin", "develop")
	write(t, dir, "internal/ledger/index.go", "package ledger\n\n// composite index\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "add a composite index")
	gitRun(t, dir, "push", "--quiet", "-u", "origin", branch)

	if err := os.MkdirAll(filepath.Join(dir, config.ConfirmedDir), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, config.ConfirmedDir+"/FCIA-6.md", "one index per issuer")

	jira := newTracker()
	var order []string
	judge := func(*workspace.Workspace, string, string) (string, error) {
		order = append(order, "judge")
		return done.ReplyDone, nil
	}
	var conformPrompt string
	conform := func(_ *workspace.Workspace, _ string, prompt string) (string, error) {
		order = append(order, "conform")
		conformPrompt = prompt
		return "CONFORMS", nil
	}

	var buf bytes.Buffer
	Run(Options{Keys: []string{"FCIA-6"}, Home: home, Out: &buf}, Deps{
		Jira: jira,
		Status: func(string, string) (PR, error) {
			return PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"}, nil
		},
		Refresh: func(string, string) (string, error) { return "", nil },
		Prune:   func(*workspace.Workspace, string) error { return nil },
		Judge:   judge,
		Conform: conform,
	})

	if n := fetchesAgainst(t, logPath, dir); n != 1 {
		t.Errorf("this ticket's checkout was fetched %d times in one collect pass, want 1 -- "+
			"the conformance pass fetched its own diff instead of reusing done triage's", n)
	}
	if got := strings.Join(order, ","); got != "judge,conform" {
		t.Errorf("call order = %q, want done triage's question asked before the conformance one", got)
	}
	// Not just that it ran once -- what it was given is the SAME diff done
	// triage read, not a coincidentally identical second read of it.
	if !strings.Contains(conformPrompt, "composite index") {
		t.Errorf("the conformance prompt did not carry the diff done triage fetched:\n%s", conformPrompt)
	}
}
