package work

import (
	"errors"
	"io"
	"io/fs"
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

// worktreeFiles reads every file in a worktree, git's own directory aside, so
// a test can assert byte-for-byte that nothing touched the agent's work.
func worktreeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.Name() == ".git":
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir():
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// OR-242. When the snapshot commit itself fails, the work is KEPT -- byte for
// byte, exactly where the agent left it -- and the failure is reported loudly
// instead.
//
// There used to be a revert here, as the fallback for exactly this case, on
// the reasoning that a dirty worktree blocks the next rebase and helps nobody.
// That reasoning assumed the commit would normally succeed. OR-241 established
// that CommitAll had never worked in this repository, so the fallback fired on
// every run, and on OR-116 it destroyed cmd/orion/releaseship_cli_test.go: the
// add had failed before staging, so no blob was written and the file could not
// be recovered from anywhere.
//
// A dirty worktree is loud, visible on the next collect tick, and still has
// every file in it. A revert is silent afterwards and leaves nothing to
// recover. They are not comparable outcomes.
func TestResidueIsKeptWhenTheCommitItselfFails(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

	j := &fakeJira{}
	var out strings.Builder
	var jobPath string
	var before map[string]string

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
				// The finished work: an edit to a tracked file and a new test
				// file that exists nowhere else -- OR-116's shape exactly.
				if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package x\n\nfunc F() {}\n"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "impl_test.go"), []byte("package x\n// finished, green, uncommitted\n"), 0o644); err != nil {
					return nil, err
				}
				tripIn(t, dir, "sess-impl", "breaker/loop", "Bash repeated 4 times")
				// Make the commit fail for a reason git owns, rather than
				// stubbing workspace.CommitAll: a hook that rejects the commit
				// is the shape a real refusal arrives in.
				hooks := filepath.Join(dir, "refusing-hooks")
				if err := os.MkdirAll(hooks, 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(hooks, "pre-commit"),
					[]byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
					return nil, err
				}
				git(t, dir, "config", "core.hooksPath", hooks)
				before = worktreeFiles(t, dir)
				return &supervisor.Result{ExitCode: 1, Reason: "tripped"}, errors.New("the agent stopped: breaker tripped")
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	// The acceptance criterion, stated as bytes: a failed commit changes
	// nothing on disk.
	after := worktreeFiles(t, jobPath)
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			t.Errorf("%s was destroyed by the failed commit; it existed nowhere else", name)
			continue
		}
		if got != want {
			t.Errorf("%s was modified by the failed commit:\nwant %q\ngot  %q", name, want, got)
		}
	}
	if len(after) != len(before) {
		t.Errorf("the worktree is not byte-identical: %d file(s) before, %d after", len(before), len(after))
	}
	// And the branch has no snapshot commit pretending it was saved.
	if log := git(t, jobPath, "log", "--oneline"); strings.Contains(log, "wip: snapshot") {
		t.Errorf("the commit failed, so nothing should have been recorded as saved:\n%s", log)
	}

	// Loud, unresolved, and naming the files that are still on disk.
	o := out.String()
	if !strings.Contains(o, "could NOT commit") || !strings.Contains(o, "KEPT") {
		t.Errorf("a run that could not commit must say so plainly:\n%s", o)
	}
	for _, want := range []string{"impl.go", "impl_test.go", "orion settle FCIA-6"} {
		if !strings.Contains(o, want) {
			t.Errorf("the report does not name %q, so the operator cannot act on it:\n%s", want, o)
		}
	}
	if !strings.Contains(sent, "Nothing was reverted") {
		t.Errorf("the operator was not told the work is still there:\n%s", sent)
	}
	// The ticket carries the reason too, not only the count.
	if comments := strings.Join(j.comments, "\n---\n"); !strings.Contains(comments, "KEPT them") {
		t.Errorf("the ticket does not say what became of the work:\n%s", comments)
	}
}

// OR-242. When the worktree holds no dirty tracked files, CommitAll is never
// invoked -- there is nothing for it to fail on, and no snapshot commit
// should appear pretending otherwise.
func TestNoDirtyFilesMeansNoCommitIsAttempted(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

	j := &fakeJira{}
	var out strings.Builder
	var jobPath string
	var before string

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
				before = git(t, dir, "log", "--oneline")
				// Nothing left dirty, tracked or untracked, and no trip.
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://github.com/x/y/pull/12", nil
			},
		})

	after := git(t, jobPath, "log", "--oneline")
	if after != before {
		t.Errorf("a commit happened over a clean worktree, where none was needed:\nbefore: %s\nafter:  %s", before, after)
	}
	if strings.Contains(after, "wip: snapshot") {
		t.Errorf("a snapshot commit was recorded though nothing was dirty:\n%s", after)
	}
	if o := out.String(); strings.Contains(o, "uncommitted work") {
		t.Errorf("residue settlement spoke up though the worktree was already clean:\n%s", o)
	}
}

// OR-242. Staged, uncommitted changes must come through a failed commit
// exactly as they were staged -- not merely present on disk, but still
// staged, since that is part of what the agent left behind.
func TestCommitFailureLeavesStagedChangesPreserved(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

	j := &fakeJira{}
	var out strings.Builder
	var jobPath string
	var beforeDiff, beforeStatusLine string

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
				// A staged edit -- `git add` run, never committed.
				if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package x\n\nfunc F() {}\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, dir, "add", "impl.go")
				tripIn(t, dir, "sess-impl", "breaker/loop", "Bash repeated 4 times")

				hooks := filepath.Join(dir, "refusing-hooks")
				if err := os.MkdirAll(hooks, 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(hooks, "pre-commit"),
					[]byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
					return nil, err
				}
				git(t, dir, "config", "core.hooksPath", hooks)
				// Scoped to impl.go alone: CommitAll's own `add -A` also
				// stages the newly-created refusing-hooks/ before the hook
				// rejects the commit, which is beside the point here -- what
				// this case is about is whether the PRE-EXISTING staged edit
				// survives, not the whole index.
				beforeDiff = git(t, dir, "diff", "--cached", "--", "impl.go")
				for _, l := range strings.Split(git(t, dir, "status", "--porcelain"), "\n") {
					if strings.HasSuffix(l, "impl.go") {
						beforeStatusLine = l
					}
				}
				return &supervisor.Result{ExitCode: 1, Reason: "tripped"}, errors.New("the agent stopped: breaker tripped")
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if diff := git(t, jobPath, "diff", "--cached", "--", "impl.go"); diff != beforeDiff {
		t.Errorf("the staged edit to impl.go changed across a failed commit:\nwant %q\ngot  %q", beforeDiff, diff)
	}
	var afterStatusLine string
	for _, l := range strings.Split(git(t, jobPath, "status", "--porcelain"), "\n") {
		if strings.HasSuffix(l, "impl.go") {
			afterStatusLine = l
		}
	}
	if afterStatusLine != beforeStatusLine {
		t.Errorf("impl.go's staged/unstaged status changed across a failed commit:\nwant %q\ngot  %q", beforeStatusLine, afterStatusLine)
	}
	if !strings.HasPrefix(beforeStatusLine, "M  ") {
		t.Fatalf("test setup did not actually leave impl.go staged: %q", beforeStatusLine)
	}
}

// OR-242. A commit can also fail for reasons that have nothing to do with a
// hook refusing it -- a git repository that cannot be written to, for
// instance a permissions problem on .git/objects. The work must be kept
// exactly the same way.
func TestCommitFailureFromGitInfrastructureErrorLeavesFilesUntouched(t *testing.T) {
	home := project(t, cfg)
	var sent string
	bindSlack(t, home, &sent)

	j := &fakeJira{}
	var out strings.Builder
	var jobPath string
	var before map[string]string
	var objectsDir string

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
				if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package x\n\nfunc F() {}\n"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "impl_test.go"), []byte("package x\n// finished, uncommitted\n"), 0o644); err != nil {
					return nil, err
				}
				tripIn(t, dir, "sess-impl", "breaker/loop", "Bash repeated 4 times")

				// A worktree keeps its own .git file pointing at the common
				// git dir's objects; resolve it rather than assuming a
				// simple repo layout.
				top := git(t, dir, "rev-parse", "--git-common-dir")
				if !filepath.IsAbs(top) {
					top = filepath.Join(dir, top)
				}
				objectsDir = filepath.Join(top, "objects")
				before = worktreeFiles(t, dir)
				if err := os.Chmod(objectsDir, 0o500); err != nil {
					t.Fatal(err)
				}
				return &supervisor.Result{ExitCode: 1, Reason: "tripped"}, errors.New("the agent stopped: breaker tripped")
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})
	t.Cleanup(func() {
		if objectsDir != "" {
			_ = os.Chmod(objectsDir, 0o700)
		}
	})

	after := worktreeFiles(t, jobPath)
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			t.Errorf("%s was destroyed by the failed commit; it existed nowhere else", name)
			continue
		}
		if got != want {
			t.Errorf("%s was modified by the failed commit:\nwant %q\ngot  %q", name, want, got)
		}
	}
	if log := git(t, jobPath, "log", "--oneline"); strings.Contains(log, "wip: snapshot") {
		t.Errorf("the commit failed for an infrastructure reason, so nothing should have been recorded as saved:\n%s", log)
	}
	if o := out.String(); !strings.Contains(o, "could NOT commit") || !strings.Contains(o, "KEPT") {
		t.Errorf("a run that could not commit for an infrastructure reason must say so plainly:\n%s", o)
	}
}

// OR-242. workspace.RevertTracked no longer exists: nothing here may discard
// an agent's uncommitted work to tidy a worktree. A source scan, not a
// compile check, because the point being verified is that the function is
// gone from the package's own source, not merely unreferenced.
func TestRevertTrackedFunctionNoLongerExists(t *testing.T) {
	entries, err := os.ReadDir("../workspace")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("../workspace", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "func RevertTracked") {
			t.Errorf("%s still defines RevertTracked; OR-242 requires it be deleted, not merely unused", e.Name())
		}
	}
}

// OR-242. No caller in internal/work or internal/hook may reach for
// RevertTracked in response to a failed commit -- the whole point being that
// a failed commit is handled by keeping the work, never by reverting it.
func TestNoCallerInvokesRevertTrackedAfterCommitFailure(t *testing.T) {
	for _, dir := range []string{".", "../hook"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			// This test file itself names the call as a string to check for;
			// that is not a call site.
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "RevertTracked(") {
				t.Errorf("%s/%s calls RevertTracked; a failed commit must keep the work, not revert it", dir, e.Name())
			}
		}
	}
}
