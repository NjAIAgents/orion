package work

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/advise"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fakeJira records every mutation so a test can assert the ORDER of state
// changes, not merely the final state. Order is the design here: claiming
// before running is what stops two runs taking one ticket.
type fakeJira struct {
	issue       *tracker.Issue
	getErr      error
	labelErr    error
	labelCalls  []string // "add:X remove:Y"
	transitions []string
	comments    []string
	// children maps a key to its sub-tasks. Nil in every existing test,
	// which is the flat ticket the rest of this file describes.
	children map[string][]tracker.Issue
	childErr error
}

func (f *fakeJira) GetIssue(key string) (*tracker.Issue, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.issue != nil {
		return f.issue, nil
	}
	return &tracker.Issue{Key: key, Summary: "do the thing",
		Description: "details", URL: "https://x/browse/" + key}, nil
}
func (f *fakeJira) Children(key string) ([]tracker.Issue, error) {
	return f.children[key], f.childErr
}
func (f *fakeJira) SetLabels(key string, add, remove []string) error {
	f.labelCalls = append(f.labelCalls,
		"add:"+strings.Join(add, ",")+" remove:"+strings.Join(remove, ","))
	return f.labelErr
}
func (f *fakeJira) TransitionTo(key, status string) error {
	f.transitions = append(f.transitions, status)
	return nil
}
func (f *fakeJira) Comment(key, text string) error {
	f.comments = append(f.comments, text)
	return nil
}
func (f *fakeJira) labelLog() string { return strings.Join(f.labelCalls, " | ") }

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// project sets up ORION_HOME with a sandbox bound to project FCIA, mirroring
// what orion init produces.
func project(t *testing.T, cfgJSON string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	git(t, root, "clone", "-q", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "spec.md"), []byte("# spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfgJSON != "" {
		if err := os.WriteFile(filepath.Join(seed, "orion.json"), []byte(cfgJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-q", "-m", "seed")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, seed, "checkout", "-q", "-b", "develop")
	git(t, seed, "push", "-q", "origin", "develop")

	ws, err := workspace.Bind(workspace.BindOptions{
		SourcePath: seed, DefaultBranch: "main", WorkBranch: "develop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Bind(home, registry.Entry{
		Key: "FCIA", Source: seed, Workspace: ws.ID,
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

// QA is switched off here so that these tests describe the claim, advise,
// push and pull-request flow and nothing else. On, it adds a supervised run
// to every one of them, and a test asserting "the implementer was resumed
// exactly once" would be counting QA's runs as well. The stage's own
// behaviour -- including that it is ON when a project says nothing -- is in
// qa_test.go.
const cfg = `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
              "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
              "qa":{"enabled":false}}`

// The happy path, and the order that makes it safe.
func TestSuccessfulRunClaimsRunsPushesAndHandsOffToCI(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var pushed, prBase string
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				// A real agent commits. Do the same, in the worktree.
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(dir, branch string) error { pushed = branch; return nil },
			OpenPR: func(dir, branch, title, body, base string) (string, error) {
				prBase = base
				return "https://github.com/x/y/pull/4", nil
			},
		})

	if len(res) != 1 || res[0].Outcome != OutcomeCIWait {
		t.Fatalf("result = %+v", res)
	}
	if res[0].Branch != "orion/fcia-6" {
		t.Errorf("branch = %q", res[0].Branch)
	}
	if pushed != "orion/fcia-6" || prBase != "develop" {
		t.Errorf("pushed %q into %q", pushed, prBase)
	}

	// Claim BEFORE the run, ci-wait after. If the claim came second, two
	// runs could take one ticket; if ci-wait came first, a crash would lose
	// the fact that a PR exists.
	log := j.labelLog()
	if !strings.Contains(log, "add:orion-working remove:ORION") {
		t.Errorf("the ticket was never claimed: %s", log)
	}
	if !strings.Contains(log, "add:orion-ci-wait remove:orion-working") {
		t.Errorf("the ticket was not handed to CI: %s", log)
	}
	if strings.Index(log, "orion-working") > strings.Index(log, "orion-ci-wait") {
		t.Errorf("ci-wait was set before the claim: %s", log)
	}
}

// AddWorktree suffixes a retry's branch (orion/fcia-6 -> orion/fcia-6-2) to
// keep it off a prior attempt's still-open pull request. `orion work` must
// record THAT name, not the one it originally asked for -- otherwise collect
// keeps looking up a branch the retry never used (OR-173).
func TestARetriedTicketsSuffixedBranchIsRecordedForCollect(t *testing.T) {
	home := project(t, cfg)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a first attempt: orion/fcia-6 already exists, so uniqueBranch
	// must suffix the retry's branch.
	git(t, ws.RepoDir(), "branch", "orion/fcia-6")

	j := &fakeJira{}
	var out strings.Builder
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(dir, branch string) error { return nil },
			OpenPR: func(dir, branch, title, body, base string) (string, error) {
				return "https://github.com/x/y/pull/5", nil
			},
		})

	if res[0].Branch != "orion/fcia-6-2" {
		t.Fatalf("branch = %q, want the suffixed orion/fcia-6-2", res[0].Branch)
	}
	recorded, ok := workspace.BranchOf(ws, "FCIA-6")
	if !ok || recorded != "orion/fcia-6-2" {
		t.Errorf("BranchOf = (%q, %v), want the suffixed branch collect must read (OR-173)", recorded, ok)
	}
}

// Exit 0 does not mean work happened. An agent that stops to ask a question
// exits cleanly with nothing to show, and pushing that would open a pull
// request describing no change.
func TestCleanExitWithNoCommitsIsBlockedNotDone(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	pushed := false
	var out strings.Builder

	question := "Are segments keyed by MCC or by issuer? spec.md does not say."
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 0, Reason: question}, nil
			},
			Push:   func(string, string) error { pushed = true; return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", res[0].Outcome)
	}
	if pushed {
		t.Error("an empty branch was pushed; the pull request would describe no change")
	}
	if !strings.Contains(res[0].Question, "MCC") {
		t.Errorf("the question was lost: %q", res[0].Question)
	}
	// The ticket must not be left claimed, or nothing will ever pick it up.
	if !strings.Contains(j.labelLog(), "add:orion-failed remove:orion-working") {
		t.Errorf("the ticket is stuck in orion-working: %s", j.labelLog())
	}
	if len(j.comments) == 0 || !strings.Contains(strings.Join(j.comments, " "), "MCC") {
		t.Errorf("the question was not recorded on the ticket: %v", j.comments)
	}
}

// A failed run must hand the ticket back. Left in orion-working it is
// invisible to the queue and no later run will retry it.
func TestAFailedRunReleasesTheTicket(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 1, Reason: "breaker tripped: 400 tool calls"}, nil
			},
			Push:   func(string, string) error { t.Fatal("pushed after a failed run"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if !strings.Contains(j.labelLog(), "add:orion-failed remove:orion-working") {
		t.Errorf("ticket left claimed: %s", j.labelLog())
	}
	if !strings.Contains(strings.Join(j.comments, " "), "breaker") {
		t.Errorf("the cause was not recorded on the ticket: %v", j.comments)
	}
}

// A push that fails must not be reported as a pull request, and must still
// release the ticket.
func TestAFailedPushDoesNotOpenAPullRequest(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	prOpened := false
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "x.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "work")
				return &supervisor.Result{ExitCode: 0}, nil
			},
			Push: func(string, string) error { return errors.New("permission denied") },
			OpenPR: func(string, string, string, string, string) (string, error) {
				prOpened = true
				return "", nil
			},
		})

	if prOpened {
		t.Error("a pull request was opened for a branch that never reached the remote")
	}
	if res[0].Outcome != OutcomeFailed {
		t.Errorf("outcome = %q", res[0].Outcome)
	}
}

// A crossed budget must stop before the tracker is touched at all. Claiming
// and then rolling back leaves noise on a ticket for a run that never began.
func TestACrossedBudgetStopsBeforeClaiming(t *testing.T) {
	home := project(t, `{"vcs":{"work_branch":"develop"},
	  "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
	  "budget":{"weekly_tokens":1000}}`)

	// Book spend past the first checkpoint.
	spendTo(t, home, 900)

	j := &fakeJira{}
	var out strings.Builder
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("the agent ran despite a crossed budget checkpoint")
				return nil, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeSkipped {
		t.Errorf("outcome = %q, want skipped", res[0].Outcome)
	}
	if len(j.labelCalls) != 0 {
		t.Errorf("the tracker was mutated for a run that never started: %v", j.labelCalls)
	}
	if !strings.Contains(out.String(), "BUDGET CHECKPOINT") {
		t.Errorf("the reason was not explained:\n%s", out.String())
	}
}

// A batch must stop after a hard failure rather than spending on the next
// ticket while the cause is still true.
func TestABatchStopsAtTheFirstFailure(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	runs := 0
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6", "FCIA-7", "FCIA-8"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				runs++
				return &supervisor.Result{ExitCode: 1, Reason: "boom"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if runs != 1 {
		t.Errorf("%d agents ran; the batch should stop at the first failure", runs)
	}
	if len(res) != 1 {
		t.Errorf("results = %d, want the one attempt", len(res))
	}
}

// --dry-run proves everything free without spending anything.
func TestDryRunStopsBeforeTheAgent(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home, DryRun: true},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("the agent ran during a dry run")
				return nil, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})
	if res[0].Outcome != OutcomeSkipped {
		t.Errorf("outcome = %q", res[0].Outcome)
	}
	// The branch is cut to prove it can be, then removed: a rehearsal that
	// consumed a branch name would push the real run onto orion/fcia-6-2,
	// whose name claims it is a second attempt at work nobody has started.
	if res[0].Branch == "" {
		t.Error("a dry run should still prove the worktree can be created")
	}
	ws, err := workspace.Open(mustWorkspaceID(t, home))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := workspace.ListWorktrees(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Errorf("a dry run left %d worktree(s) behind: %+v", len(jobs), jobs)
	}
}

func TestUnknownProjectFailsWithoutTouchingAnything(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder
	res := Run(Options{Keys: []string{"NOPE-1"}, Out: &out, Home: home},
		Deps{Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("ran for an unregistered project")
				return nil, nil
			}})
	if res[0].Outcome != OutcomeFailed {
		t.Errorf("outcome = %q", res[0].Outcome)
	}
	if len(j.labelCalls) != 0 {
		t.Errorf("labels were written for an unknown project: %v", j.labelCalls)
	}
}

func TestBranchFor(t *testing.T) {
	for _, tc := range []struct{ prefix, key, want string }{
		{"orion/", "FCIA-6", "orion/fcia-6"},
		{"", "FCIA-6", "orion/fcia-6"},
		{"ai/", "ABC-123", "ai/abc-123"},
	} {
		if got := branchFor(tc.prefix, tc.key); got != tc.want {
			t.Errorf("branchFor(%q,%q) = %q, want %q", tc.prefix, tc.key, got, tc.want)
		}
	}
}

func spendTo(t *testing.T, home string, tokens int) {
	t.Helper()
	l, err := budget.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	l.Record(budget.Run{At: time.Now().UTC(), InputTokens: tokens})
	if err := l.Save(home); err != nil {
		t.Fatal(err)
	}
}

// A rehearsal must not mutate a shared system. The first version of this
// code claimed during a dry run and left the ticket in orion-working with no
// rollback: out of the queue, retried by nobody, for a run that never began.
func TestDryRunDoesNotTouchTheTracker(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home, DryRun: true},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("the agent ran during a dry run")
				return nil, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if len(j.labelCalls) != 0 {
		t.Errorf("a dry run wrote labels: %v", j.labelCalls)
	}
	if len(j.transitions) != 0 {
		t.Errorf("a dry run moved the ticket: %v", j.transitions)
	}
	if len(j.comments) != 0 {
		t.Errorf("a dry run commented: %v", j.comments)
	}
}

func mustWorkspaceID(t *testing.T, home string) string {
	t.Helper()
	e, err := registry.Lookup(home, "FCIA")
	if err != nil {
		t.Fatal(err)
	}
	return e.Workspace
}

// advisor builds a Runner that answers the router and then the advisor.
func advisor(route string, replies ...string) (advise.Runner, *int) {
	n := 0
	i := 0
	return func(dir, model, prompt string) (string, error) {
		n++
		if model == advise.ModelRouter {
			return route, nil
		}
		if i < len(replies) {
			r := replies[i]
			i++
			return r, nil
		}
		return `{"verdict":"refused","reason":"no more replies"}`, nil
	}, &n
}

// The loop this whole design exists for: the implementer stops with a
// question, an advisor answers it from the committed design, the decision is
// recorded, and the SAME session continues rather than starting over.
func TestTheImplementerIsResumedWithTheAdvisorsAnswer(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	run, adviceCalls := advisor("TECHNICAL",
		`{"verdict":"derived","decision":"By issuer.","grounding":"spec.md section 4"}`)

	runs := 0
	var resumedWith, resumedSession string
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira:   j,
			Advise: run,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				runs++
				if runs == 1 {
					// Stops to ask, having produced nothing.
					return &supervisor.Result{ExitCode: 0, SessionID: "sess-1",
						Final: "Are segments keyed by MCC or by issuer?"}, nil
				}
				resumedWith, resumedSession = o.Prompt, o.Resume
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: segment by issuer")
				return &supervisor.Result{ExitCode: 0, SessionID: "sess-1", Final: "done"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q, want the run to have finished: %+v", res[0].Outcome, res[0])
	}
	if runs != 2 {
		t.Errorf("%d runs; expected the implementer to be resumed once", runs)
	}
	// Resumed, not restarted: restarting would re-read the spec, re-explore
	// the code and pay for the whole context a second time.
	if resumedSession != "sess-1" {
		t.Errorf("resume session = %q, want the original session", resumedSession)
	}
	if !strings.Contains(resumedWith, "By issuer") || !strings.Contains(resumedWith, "spec.md section 4") {
		t.Errorf("the answer did not reach the implementer:\n%s", resumedWith)
	}
	if *adviceCalls < 2 {
		t.Errorf("advice calls = %d; expected a route and an ask", *adviceCalls)
	}
}

// The decision has to land in the diff a reviewer reads, and become grounding
// for the next ticket. A Slack thread cannot do either.
func TestTheDecisionIsCommittedOnTheBranch(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder
	run, _ := advisor("TECHNICAL",
		`{"verdict":"derived","decision":"By issuer.","grounding":"spec.md section 4"}`)

	var worktree string
	runs := 0
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				runs++
				worktree = ws.RepoDir()
				if runs == 1 {
					return &supervisor.Result{ExitCode: 0, SessionID: "s", Final: "MCC or issuer?"}, nil
				}
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: done")
				return &supervisor.Result{ExitCode: 0, SessionID: "s", Final: "done"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	rec := filepath.Join(worktree, "docs", "decisions", "fcia-6-01.md")
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("no decision record was written: %v", err)
	}
	body := string(b)
	for _, want := range []string{"MCC or issuer?", "By issuer.", "spec.md section 4", "architect"} {
		if !strings.Contains(body, want) {
			t.Errorf("the record is missing %q:\n%s", want, body)
		}
	}
	// Committed, and as its own commit: squashing it with the implementation
	// would hide which part of the change the decision caused.
	log := git(t, worktree, "log", "--oneline")
	if !strings.Contains(log, "record the architect decision") {
		t.Errorf("the decision was not committed separately:\n%s", log)
	}
	if st := git(t, worktree, "status", "--porcelain"); strings.Contains(st, "decisions") {
		t.Errorf("the record was left uncommitted: %s", st)
	}
}

// A refusal means the DESIGN is silent. That is a human's decision and then
// an amendment, so it must not be dressed up as a failed run.
func TestARefusalBlocksAndSaysTheDesignIsIncomplete(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder
	run, _ := advisor("PRODUCT",
		`{"verdict":"refused","reason":"intent.md does not mention fees on declines"}`)

	runs := 0
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Advise: run,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				runs++
				return &supervisor.Result{ExitCode: 0, SessionID: "s",
					Final: "Do we charge a fee on declined transactions?"}, nil
			},
			Push:   func(string, string) error { t.Fatal("pushed after a refusal"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", res[0].Outcome)
	}
	if runs != 1 {
		t.Errorf("%d runs; a refusal must not resume the implementer", runs)
	}
	comments := strings.Join(j.comments, "\n")
	if !strings.Contains(comments, "amend the artifact") {
		t.Errorf("the ticket does not say the design is incomplete:\n%s", comments)
	}
	if !strings.Contains(comments, "fees on declines") {
		t.Errorf("the advisor's reason was lost:\n%s", comments)
	}
}

// Two agents can converse indefinitely at full price while producing nothing.
func TestTheAdvisorLoopIsCapped(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder
	run := advise.Runner(func(dir, model, prompt string) (string, error) {
		if model == advise.ModelRouter {
			return "TECHNICAL", nil
		}
		return `{"verdict":"derived","decision":"Do it this way.","grounding":"spec.md 1"}`, nil
	})

	runs := 0
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				runs++ // never commits, always asks again
				return &supervisor.Result{ExitCode: 0, SessionID: "s",
					Final: "But what about the other case?"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeBlocked {
		t.Errorf("outcome = %q, want blocked once the cap is hit", res[0].Outcome)
	}
	if runs > maxQuestions+1 {
		t.Errorf("%d runs; the loop is not capped at %d questions", runs, maxQuestions)
	}
}

// Without a session there is nothing to continue. Restarting would pay for
// the whole context again and might make different choices, so stopping and
// keeping the answer is better than silently re-running.
func TestNoSessionMeansStopRatherThanRestart(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder
	run, _ := advisor("TECHNICAL",
		`{"verdict":"derived","decision":"By issuer.","grounding":"spec.md 4"}`)

	runs := 0
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				runs++
				return &supervisor.Result{ExitCode: 0, SessionID: "", Final: "MCC or issuer?"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if runs != 1 {
		t.Errorf("%d runs; without a session it must not restart", runs)
	}
	if res[0].Outcome != OutcomeBlocked {
		t.Errorf("outcome = %q", res[0].Outcome)
	}
}

// With no advisor configured the old behaviour must survive: stop, record
// the question, hand the ticket back.
func TestWithoutAnAdvisorItStillBlocksCleanly(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, // Advise deliberately nil
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 0, Final: "MCC or issuer?"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})
	if res[0].Outcome != OutcomeBlocked {
		t.Errorf("outcome = %q", res[0].Outcome)
	}
	if !strings.Contains(res[0].Question, "MCC") {
		t.Errorf("question = %q", res[0].Question)
	}
}
