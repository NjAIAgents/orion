package work

import (
	"errors"
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

// clearedTripIn writes what a session looks like AFTER its unverified-edits
// trip self-cleared on a passing verify (internal/hook/breaker.go).
//
// Written as the JSON that shape actually produces, omitempty and all: the
// session file is still there, still readable, and no longer says anything
// tripped. That is the state OR-217 was in -- session 4b6af93d had cleared
// itself, so a cleanup gated on the flag saw a healthy run and left 163 lines
// of staged work to block the rebase.
func clearedTripIn(t *testing.T, worktree, session string) {
	t.Helper()
	dir := filepath.Join(worktree, ".orion", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + session + `","tool_calls":41,"edits_since_verify":0}`
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

// OR-207. A tripped run that is still holding work must COMMIT it, and must
// say on the ticket that it did.
//
// OR-189 and OR-191 both finished their implementation, both had it green,
// and both ended orion-failed with every line uncommitted -- 258 and 439
// lines, recovered by hand. Both looked like ordinary failures until someone
// opened the worktree, which is why the ticket has to carry the fact.
func TestATrippedRunCommitsTheWorkItWasHoldingAndSaysSoOnTheTicket(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

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
				// The finished work: an edit to a tracked file and a NEW test
				// file, neither committed, and the trip on the next call.
				if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package x\n\nfunc F() {}\n"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "impl_test.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				// The breaker's own stop-note, written at the moment of the
				// trip (OR-194).
				if err := os.MkdirAll(filepath.Join(dir, "plans"), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "plans", "BLOCKED.md"),
					[]byte("## breaker/loop tripped\n"), 0o644); err != nil {
					return nil, err
				}
				tripIn(t, dir, "sess-impl", "breaker/loop", "Read repeated 4 times")
				return &supervisor.Result{ExitCode: 1, Reason: "tripped"}, errors.New("the agent stopped: breaker tripped")
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	// The work is on the branch, not in the bin.
	if log := git(t, jobPath, "log", "--oneline"); !strings.Contains(log, "wip: snapshot") {
		t.Errorf("the work the run was holding was not committed:\n%s", log)
	}
	files := git(t, jobPath, "show", "--name-only", "--format=", "HEAD")
	for _, want := range []string{"impl.go", "impl_test.go"} {
		if !strings.Contains(files, want) {
			t.Errorf("the snapshot is missing %q:\n%s", want, files)
		}
	}
	// Not the breaker's own note about the trip: that is written for whoever
	// opens the worktree and is not part of the change (OR-194).
	if strings.Contains(files, "BLOCKED.md") {
		t.Errorf("plans/BLOCKED.md rode along on the branch:\n%s", files)
	}
	if strings.Contains(files, ".orion") {
		t.Errorf("Orion's own runtime directory was committed:\n%s", files)
	}
	// And the rebase collect will attempt is still possible.
	if dirty, _ := workspace.DirtyTracked(jobPath); dirty != "" {
		t.Errorf("the worktree is still dirty:\n%s", dirty)
	}

	// Said in the run output, and on the TICKET, in those words.
	if o := out.String(); !strings.Contains(o, "uncommitted work") {
		t.Errorf("the run output does not say what it was holding:\n%s", o)
	}
	comments := strings.Join(j.comments, "\n---\n")
	for _, want := range []string{"orion-failed", "uncommitted file(s)", "breaker/loop"} {
		if !strings.Contains(comments, want) {
			t.Errorf("the ticket does not say %q:\n%s", want, comments)
		}
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

// OR-233. A run with NO trip on record that still ends dirty is settled too.
//
// The uncommitted tree is what breaks the system: rebaseOnto refuses it
// whatever ended the run, so a branch left this way holds its place in the
// landing queue and never takes a turn. Whether a breaker caused it is
// incidental, and gating the cleanup on that produced OR-217.
//
// Settled means COMMITTED, not reverted -- the work is still there to read,
// which is the whole reason the revert stopped being unconditional (OR-207).
func TestUncommittedWorkIsSettledWhenNothingEverTripped(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

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
				// Left uncommitted, and no state file anywhere: nothing ever
				// tripped in this run.
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

	// The condition rebaseOnto actually tests. This is the whole point.
	if dirty, _ := workspace.DirtyTracked(jobPath); dirty != "" {
		t.Errorf("no trip was recorded, so the residue was left and the next rebase will refuse:\n%s", dirty)
	}
	// Committed, not reverted: the work still exists to be read.
	b, err := os.ReadFile(filepath.Join(jobPath, "spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "in progress") {
		t.Error("the uncommitted work was reverted rather than committed")
	}
	if files := git(t, jobPath, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "spec.md") {
		t.Errorf("spec.md is not in the snapshot commit:\n%s", files)
	}
	// The trip decides the WORDING, and there was none, so the message must
	// not claim one.
	msg := git(t, jobPath, "log", "-1", "--format=%B")
	if !strings.Contains(msg, "wip: snapshot") {
		t.Errorf("the residue was not snapshotted:\n%s", msg)
	}
	if strings.Contains(msg, "breaker tripped") {
		t.Errorf("the commit message invents a trip that never happened:\n%s", msg)
	}
	if o := out.String(); !strings.Contains(o, "no breaker trip was on record") ||
		!strings.Contains(o, "uncommitted work") {
		t.Errorf("the run output does not say what it settled or why:\n%s", o)
	}
}

// OR-233, the case that actually happened. A trip that SELF-CLEARED on a
// passing verify leaves a worktree indistinguishable from a healthy one to
// anything reading the breaker flag -- and still full of staged work.
//
// On OR-217, session 4b6af93d had cleared itself, the worktree kept 163 lines
// of staged QA tests, rebaseOnto refused the branch on every poll for over
// fifteen minutes while two healthy branches starved behind it, and recovery
// took an operator running git by hand in a hashed path under ORION_HOME.
//
// Every mechanism that erases that flag is individually correct, which is why
// this must not be conditioned on it: the next one added would reintroduce
// the bug in a new way.
func TestASelfClearedTripStillHasItsResidueSettled(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

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
				// The QA tests, written and STAGED -- as on OR-217 -- and
				// never committed.
				if err := os.WriteFile(filepath.Join(dir, "qa_test.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, dir, "add", "qa_test.go")
				// The unverified-edits trip fired, then the verify passed and
				// cleared it. The session file survives, saying nothing.
				clearedTripIn(t, dir, "4b6af93d")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if dirty, _ := workspace.DirtyTracked(jobPath); dirty != "" {
		t.Errorf("the self-cleared trip left its residue, so every rebase of this branch refuses:\n%s", dirty)
	}
	if files := git(t, jobPath, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "qa_test.go") {
		t.Errorf("the staged QA tests were not committed:\n%s", files)
	}
	// And said where somebody who was not watching will see it -- the half of
	// OR-217 that meant nobody knew for fifteen minutes.
	if !strings.Contains(sent, "left the worktree dirty") {
		t.Errorf("the operator was not told:\n%s", sent)
	}
}

// The revert fallback, unchanged: when the commit itself fails, the residue is
// still cleared so the branch can be rebased, and the loss is stated rather
// than implied.
//
// Reverting is the worse outcome of the two and always was. It survives only
// because a worktree left dirty helps nobody, and a run whose commit failed
// must not be the one case that silently reintroduces the block.
func TestResidueIsRevertedWhenTheCommitItselfFails(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

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
				if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# spec\nrisky\n"), 0o644); err != nil {
					return nil, err
				}
				tripIn(t, dir, "sess-impl", "breaker/loop", "Bash repeated 4 times")
				// Make the commit fail for a reason git owns, rather than
				// stubbing workspace.CommitAll: what is under test is the
				// fallback, and a hook that rejects the commit is the shape a
				// real refusal arrives in.
				hooks := filepath.Join(dir, "refusing-hooks")
				if err := os.MkdirAll(hooks, 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(hooks, "pre-commit"),
					[]byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
					return nil, err
				}
				git(t, dir, "config", "core.hooksPath", hooks)
				return &supervisor.Result{ExitCode: 1, Reason: "tripped"}, errors.New("the agent stopped: breaker tripped")
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	// Cleared either way: a branch nobody can rebase is the outcome this
	// exists to prevent, and it is the one the failed commit would leave.
	if dirty, _ := workspace.DirtyTracked(jobPath); dirty != "" {
		t.Errorf("the commit failed and nothing reverted, so the branch is still blocked:\n%s", dirty)
	}
	// And said plainly, in the words that distinguish destroyed from filed
	// away.
	if o := out.String(); !strings.Contains(o, "could NOT commit") || !strings.Contains(o, "reverted") {
		t.Errorf("a run that destroyed work must say so:\n%s", o)
	}
	if !strings.Contains(sent, "could *not* preserve it") {
		t.Errorf("the operator was not told the work may be lost:\n%s", sent)
	}
}
