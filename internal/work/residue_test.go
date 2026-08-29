package work

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// tripIn writes the state a tripped breaker leaves in a job's worktree.
//
// Written by hand rather than by driving the hook, because what is under
// test here is what ORION does about a trip -- the agent side of it lives in
// internal/hook. The shape is state.Session's, deliberately as JSON: if that
// file format changes, this must fail.
func tripIn(t *testing.T, worktree, session, kind, detail string) {
	t.Helper()
	dir := filepath.Join(worktree, ".orion", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + session + `","tripped":"` + kind + `","tripped_detail":"` + detail + `"}`
	if err := os.WriteFile(filepath.Join(dir, session+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bindSlack gives the workspace a channel and captures what would be posted,
// so a test can assert the operator was told rather than only the run log.
func bindSlack(t *testing.T, home string, sent *string) string {
	t.Helper()
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	ws.Task.Slack = &workspace.SlackChannel{ID: "C-TEST", Name: "orion-fcia"}
	if err := ws.SaveTask(); err != nil {
		t.Fatal(err)
	}

	prevSender := notify.SetSlackSender(func(_, text string, _ notify.Level) error {
		*sent = text
		return nil
	})
	t.Cleanup(func() { notify.SetSlackSender(prevSender) })
	prevOut := notify.Out
	notify.Out = io.Discard
	t.Cleanup(func() { notify.Out = prevOut })
	return ws.RepoDir()
}

// OR-194. A run that ends with its breaker tripped must not leave
// uncommitted tracked changes behind.
//
// The agent has a cleanup allowance now, but an allowance only helps while
// the agent is still there to spend it -- and on OR-192 the worktree was
// only rescued because a later stage happened to exist and happened to read
// plans/BLOCKED.md. Orion does not depend on that: this runs when the RUN
// ends, whatever ended it.
func TestATrippedRunLeavesNoUncommittedTrackedChanges(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

	j := &fakeJira{}
	var out strings.Builder
	var jobPath string

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				dir := ws.RepoDir()
				jobPath = dir
				// Real work, committed.
				if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, dir, "add", ".")
				git(t, dir, "commit", "-q", "-m", "feat: implement")
				// Then the risky edit to a tracked file, and the trip on the
				// very next call -- no turn left in which to revert it.
				if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# spec\nrisky\n"), 0o644); err != nil {
					return nil, err
				}
				tripIn(t, dir, "sess-qa", "breaker/loop", "Bash repeated 4 times")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://github.com/x/y/pull/9", nil
			},
		})

	if len(res) != 1 {
		t.Fatalf("result = %+v", res)
	}
	// The condition collect's rebaseOnto actually tests before it will
	// replay a branch. Residue from a trip must not be what stops it.
	dirty, err := workspace.DirtyTracked(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if dirty != "" {
		t.Errorf("the worktree is still dirty, so the next rebase of this branch will refuse:\n%s", dirty)
	}
	// What the run COMMITTED is not what gets reverted.
	if log := git(t, jobPath, "log", "--oneline"); !strings.Contains(log, "feat: implement") {
		t.Errorf("the run's commits were destroyed:\n%s", log)
	}

	if o := out.String(); !strings.Contains(o, "breaker tripped") {
		t.Errorf("a run that ends tripped and dirty must say so:\n%s", o)
	}
	// And where somebody who was not watching will see it. A blocked rebase
	// found on the next collect tick is a slow way to learn this.
	if !strings.Contains(sent, "left the worktree dirty") {
		t.Errorf("the operator was not told; only the run log was:\n%s", sent)
	}
}

// The inverse, which matters just as much: a trip that left the tree clean
// is not an occasion to page anybody.
func TestATripThatLeftNothingBehindIsNotReported(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

	j := &fakeJira{}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				dir := ws.RepoDir()
				if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, dir, "add", ".")
				git(t, dir, "commit", "-q", "-m", "feat: implement")
				// Tripped, but the agent spent its allowance and committed.
				tripIn(t, dir, "sess-impl", "breaker/tool-budget", "400 tool calls")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://github.com/x/y/pull/10", nil
			},
		})

	if o := out.String(); strings.Contains(o, "breaker tripped") {
		t.Errorf("a tripped run that cleaned up after itself needs no warning:\n%s", o)
	}
	if strings.Contains(sent, "left the worktree dirty") {
		t.Errorf("the operator was paged about nothing:\n%s", sent)
	}
}

// An untouched worktree left dirty by a run that did NOT trip is somebody's
// work in progress, not residue. Reverting it would destroy the thing the
// deletion guard in workspace.Dirty exists to protect.
func TestUncommittedWorkIsKeptWhenNothingTripped(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder
	var jobPath string

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				dir := ws.RepoDir()
				jobPath = dir
				if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, dir, "add", ".")
				git(t, dir, "commit", "-q", "-m", "feat: implement")
				if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# spec\nin progress\n"), 0o644); err != nil {
					return nil, err
				}
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://github.com/x/y/pull/11", nil
			},
		})

	b, err := os.ReadFile(filepath.Join(jobPath, "spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "in progress") {
		t.Error("uncommitted work was reverted without a trip to justify it")
	}
}
