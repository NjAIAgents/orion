package work

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fakeSlack records what was asked and answers what was reacted.
type fakeSlack struct {
	posts     []string
	replies   []string
	reactions []slack.Reaction
	ts        string
}

func (f *fakeSlack) PostTS(channel, text string) (string, error) {
	f.posts = append(f.posts, text)
	if f.ts == "" {
		f.ts = "1700000000.0001"
	}
	return f.ts, nil
}
func (f *fakeSlack) Reply(channel, threadTS, text string) error {
	f.replies = append(f.replies, text)
	return nil
}
func (f *fakeSlack) React(channel, ts, emoji string) {}
func (f *fakeSlack) BotID() string                   { return "UBOT" }
func (f *fakeSlack) Reactions(channel, ts string) ([]slack.Reaction, error) {
	return f.reactions, nil
}
func (f *fakeSlack) Replies(channel, ts string) ([]slack.Message, error) { return nil, nil }
func (f *fakeSlack) UserName(id string) string                           { return id }
func (f *fakeSlack) MemberID(who string) (string, error)                 { return who, nil }

var _ SlackAPI = (*fakeSlack)(nil)
var _ collect.SlackAPI = (*fakeSlack)(nil)

// quotaRun is a plan limit reached before a single turn, with no reset time
// the provider was willing to state. Structurally identical to the logged-out
// case: nothing ran, nothing was spent, and waiting is not an option because
// nobody said what to wait for.
func quotaRun() (*supervisor.Result, error) {
	return &supervisor.Result{
		ExitCode: 1, QuotaUnwaitable: true, Started: false,
		Reason: "quota exhausted; the provider did not say when it resets",
	}, errors.New("stage ticket failed: claude exited 1")
}

// The rule the whole ticket rests on: retry only what never started.
func TestAQuotaWallWithNoResetTimeHoldsTheTicketRatherThanFailingIt(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return quotaRun()
			},
			Push:   func(string, string) error { t.Fatal("pushed after a run that never started"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeHeld)
	}
	if res[0].Fault.Kind != FaultQuota {
		t.Errorf("fault = %q, want %q", res[0].Fault.Kind, FaultQuota)
	}
	if strings.Contains(j.labelLog(), "orion-failed") {
		t.Errorf("labelled failed for a run that never began: %s", j.labelLog())
	}
	if !strings.Contains(j.labelLog(), "add:ORION remove:orion-working") {
		t.Errorf("the claim was not handed back to the queue: %s", j.labelLog())
	}
	if len(j.transitions) == 0 || j.transitions[len(j.transitions)-1] != "To Do" {
		t.Errorf("left In Progress after the claim was released: %v", j.transitions)
	}
	if h := Holds(home); len(h) != 1 || h[0].Kind != FaultQuota {
		t.Fatalf("no hold was recorded: %+v", h)
	}
}

// The half that must NOT change. A quota wall reached mid-run is a run that
// did work: somebody has to judge why before it costs that again.
func TestAQuotaWallAfterTurnsWereSpentIsStillAFailedRun(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				r, err := quotaRun()
				r.Started = true // a stream frame: the session existed and spent
				return r, err
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeFailed)
	}
	if !strings.Contains(j.labelLog(), "add:orion-failed remove:orion-working") {
		t.Errorf("a run that spent turns must still be labelled: %s", j.labelLog())
	}
	if len(Holds(home)) != 0 {
		t.Errorf("a run that did work was held instead of failed: %+v", Holds(home))
	}
}

// The residue rule, both halves. An empty worktree and branch must go, or the
// retry cuts orion/fcia-6-2 and an operator is looking at two branches for a
// ticket that has never run.
func TestAHeldRunRemovesItsEmptyWorktreeAndBranch(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return quotaRun()
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})
	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q", res[0].Outcome)
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
		t.Errorf("a held run left %d worktree(s) behind: %+v", len(jobs), jobs)
	}
	branches := git(t, ws.CloneDir(), "branch", "--list", "orion/fcia-6")
	if strings.TrimSpace(branches) != "" {
		t.Errorf("the empty branch was kept: %q", branches)
	}
}

// And the other half: anything in the worktree is KEPT and reported, exactly
// as prune does. Deleting work nobody has looked at is the one outcome worse
// than a stale branch.
func TestAHeldRunKeepsAWorktreeThatHasSomethingInIt(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				// A half-written file, uncommitted, in the job's worktree.
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "spec.md"),
					[]byte("# spec\nhalf a thought\n"), 0o644); err != nil {
					return nil, err
				}
				return quotaRun()
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})
	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if !strings.Contains(out.String(), "it is not empty") {
		t.Errorf("a worktree with work in it was removed silently or not reported:\n%s", out.String())
	}
}

// One message per FAULT, not one per ticket. Three tickets stopped by one
// expired login is one thing to fix, and three messages saying so is a channel
// people mute.
func TestOneSlackMessagePerFaultRatherThanOnePerTicket(t *testing.T) {
	home := project(t, cfg)
	withChannel(t, home)
	fs := &fakeSlack{}
	var out strings.Builder

	for _, key := range []string{"FCIA-6", "FCIA-7", "FCIA-8"} {
		Run(Options{Keys: []string{key}, Out: &out, Home: home},
			Deps{
				Jira:  &fakeJira{},
				Slack: fs,
				Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
					return quotaRun()
				},
				Push:   func(string, string) error { return nil },
				OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
			})
	}

	if len(fs.posts) != 1 {
		t.Fatalf("asked %d times about one fault:\n%s", len(fs.posts), strings.Join(fs.posts, "\n---\n"))
	}
	if !strings.Contains(fs.posts[0], fixQuota) {
		t.Errorf("the message does not name the fix:\n%s", fs.posts[0])
	}
	h := Holds(home)
	if len(h) != 1 || len(h[0].Keys) != 3 {
		t.Fatalf("the hold does not name every ticket it stopped: %+v", h)
	}
}

// A reaction is a claim, not a fact. Releasing on it alone is what starts
// three runs that die the way the first three did.
func TestAConfirmationDoesNotReleaseAHoldTheCheckStillRefuses(t *testing.T) {
	home := t.TempDir()
	fs := &fakeSlack{ts: "1700000000.0001",
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"UHUMAN"}}}}

	seedHold(t, home, fs, []string{"UHUMAN"})

	still := Release(home, ReleaseDeps{
		Slack: fs,
		Recheck: func(FaultKind) (string, string) {
			return "FAIL", "the CLI is not signed in"
		},
	}, &strings.Builder{})

	if len(still) != 1 {
		t.Fatalf("released a hold whose check still fails: %+v", still)
	}
	if len(fs.replies) != 1 || !strings.Contains(fs.replies[0], "the CLI is not signed in") {
		t.Fatalf("the person who confirmed the fix was not told it is still broken: %v", fs.replies)
	}
	// And not on every tick after that. The same news repeated is noise.
	Release(home, ReleaseDeps{Slack: fs,
		Recheck: func(FaultKind) (string, string) { return "FAIL", "the CLI is not signed in" },
	}, &strings.Builder{})
	if len(fs.replies) != 1 {
		t.Errorf("repeated the same refusal %d times: %v", len(fs.replies), fs.replies)
	}
}

// The other side: the check passes, so the hold goes and the tickets are
// workable again.
func TestAHealthyRecheckReleasesTheHold(t *testing.T) {
	home := t.TempDir()
	fs := &fakeSlack{ts: "1700000000.0001"}
	seedHold(t, home, fs, []string{"UHUMAN"})

	still := Release(home, ReleaseDeps{
		Slack:   fs,
		Recheck: func(FaultKind) (string, string) { return "OK", "signed in as a@b" },
	}, &strings.Builder{})

	if len(still) != 0 {
		t.Fatalf("held a ticket after the environment came back: %+v", still)
	}
	if len(Holds(home)) != 0 {
		t.Errorf("the hold was not cleared: %+v", Holds(home))
	}
}

// Slack is detected, never required (A5). With none, the ticket still holds
// and still resumes on the first healthy tick -- the reaction is a convenience
// for the unattended case, not the mechanism.
func TestWithoutSlackTheTicketStillHoldsAndStillResumes(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, // Slack deliberately nil
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return quotaRun()
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})
	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if !strings.Contains(out.String(), "quota exhausted") ||
		!strings.Contains(out.String(), "Wait for the plan limit to reset") {
		t.Errorf("the run output names neither the fault nor the fix:\n%s", out.String())
	}
	if len(Holds(home)) != 1 {
		t.Fatalf("no hold without Slack: %+v", Holds(home))
	}

	still := Release(home, ReleaseDeps{
		Recheck: func(FaultKind) (string, string) { return "OK", "healthy" },
	}, &out)
	if len(still) != 0 {
		t.Errorf("a healthy tick did not release the hold: %+v", still)
	}
}

// One automatic retry per ticket per fault. A second identical fault means the
// fix did not work, so asking again would make a wrong confirmation a loop.
func TestASecondIdenticalFaultEscalatesRatherThanAskingAgain(t *testing.T) {
	home := project(t, cfg)
	withChannel(t, home)
	fs := &fakeSlack{}
	var out strings.Builder

	run := func() []Result {
		return Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
			Deps{
				Jira:  &fakeJira{},
				Slack: fs,
				Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
					return quotaRun()
				},
				Push:   func(string, string) error { return nil },
				OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
			})
	}

	run()
	// Somebody says it is fixed and the check agrees, so the ticket is workable
	// again -- which is the retry the bound allows.
	if still := Release(home, ReleaseDeps{Slack: fs,
		Recheck: func(FaultKind) (string, string) { return "OK", "healthy" },
	}, &out); len(still) != 0 {
		t.Fatalf("the first hold did not clear: %+v", still)
	}
	asked := len(fs.posts)

	run() // and it faults again, identically

	// It still SAYS something -- silence would be worse -- but what it says is
	// an escalation, not the same question a second time.
	if len(fs.posts) != asked+1 {
		t.Fatalf("posted %d messages, want one escalation: %v", len(fs.posts)-asked, fs.posts)
	}
	last := fs.posts[len(fs.posts)-1]
	if strings.Contains(last, "*What broke*") || strings.Contains(last, "React ✅") {
		t.Errorf("asked the same question again rather than escalating:\n%s", last)
	}
	if !strings.Contains(last, "will not ask again") ||
		!strings.Contains(last, "orion reset --held") {
		t.Errorf("the escalation does not say it has stopped asking, or how to clear it:\n%s", last)
	}
	h := Holds(home)
	if len(h) != 1 || !h[0].Escalated {
		t.Fatalf("the second identical fault did not escalate: %+v", h)
	}
	if !strings.Contains(out.String(), "orion reset --held") {
		t.Errorf("the escalation does not say how a person clears it:\n%s", out.String())
	}
	// And it stays held even where nothing can check it, because the retry that
	// WAS the check has already been spent.
	if still := Release(home, ReleaseDeps{Slack: fs}, &out); len(still) != 1 {
		t.Errorf("an escalated, uncheckable hold released itself: %+v", still)
	}
	// A person at a terminal is the way out.
	if still := Release(home, ReleaseDeps{Slack: fs, Manual: true}, &out); len(still) != 0 {
		t.Errorf("`orion reset --held` could not clear it: %+v", still)
	}
}

// A ticket that ran must not carry its old fault count forever, or the next
// environmental fault it meets escalates immediately instead of getting the
// one retry it is owed.
func TestAnEndingThatWasNotAFaultForgetsTheCount(t *testing.T) {
	home := t.TempDir()
	if _, _, err := RecordFault(home, Fault{Kind: FaultQuota, Cause: "c", Fix: "f"},
		"FCIA-6", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ClearHold(home, FaultQuota); err != nil {
		t.Fatal(err)
	}
	forgetFault(home, "FCIA-6")

	_, _, err := RecordFault(home, Fault{Kind: FaultQuota, Cause: "c", Fix: "f"},
		"FCIA-6", "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if h := Holds(home); len(h) != 1 || h[0].Escalated {
		t.Fatalf("a ticket that ran in between was escalated on its next first fault: %+v", h)
	}
}

// Only a connectivity failure is the machine's. Anything the tracker or the
// forge ANSWERS is a failure a person reads, and holding it would replace a
// legible failure with a queue that never moves.
func TestOnlyAConnectivityFailureIsAnEnvironmentalFault(t *testing.T) {
	for _, tc := range []struct {
		err  string
		want bool
	}{
		{"Get \"https://x.atlassian.net\": dial tcp: lookup x: no such host", true},
		{"connection refused", true},
		{"503 Service Unavailable", true},
		{"401 Unauthorized: the token was revoked", false},
		{"issue FCIA-6 does not exist", false},
		{"transition \"To Do\" is not available", false},
	} {
		_, got := unreachableFault(FaultTracker, errors.New(tc.err))
		if got != tc.want {
			t.Errorf("unreachableFault(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// The preflight check runs before the claim is written to the tracker at
// all. A ticket the environment cannot work must never be claimed and then
// handed back -- it must never be claimed in the first place.
func TestPreflightFaultHoldsBeforeTheClaimLands(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Preflight: func() (Fault, bool) {
				return NewFault(FaultClaudeAuth, "the CLI is not signed in"), true
			},
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("an agent was started although preflight already found the environment broken")
				return nil, nil
			},
			Push:   func(string, string) error { t.Fatal("pushed"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeHeld)
	}
	if res[0].Fault.Kind != FaultClaudeAuth {
		t.Errorf("fault = %q, want %q", res[0].Fault.Kind, FaultClaudeAuth)
	}
	if len(j.labelCalls) != 0 {
		t.Errorf("the ticket was claimed even though preflight had already refused it: %v", j.labelCalls)
	}
}

// GetIssue is the very first thing a run does. A tracker nobody can reach
// must hold before the claim and before a worktree is ever cut.
func TestTrackerUnreachableOnGetIssueHoldsBeforeClaimAndWorktree(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{getErr: errors.New("dial tcp: lookup jira.example.com: no such host")}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("an agent ran although the tracker could not even be read")
				return nil, nil
			},
			Push:   func(string, string) error { t.Fatal("pushed"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeHeld)
	}
	if res[0].Fault.Kind != FaultTracker {
		t.Errorf("fault = %q, want %q", res[0].Fault.Kind, FaultTracker)
	}
	if len(j.labelCalls) != 0 {
		t.Errorf("a claim was attempted though the tracker could not be reached: %v", j.labelCalls)
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
		t.Errorf("a worktree was cut before the claim even landed: %+v", jobs)
	}
}

// SetLabels is the claim itself. When it fails on a connectivity error the
// claim never landed, so there is nothing to hand back -- unlike the
// mid-run faults, no rollback (label or transition) should ever fire here.
func TestTrackerUnreachableOnClaimHoldsWithNothingToHandBack(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{labelErr: errors.New("Post \"https://x.atlassian.net\": context deadline exceeded")}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("an agent ran although the claim never landed")
				return nil, nil
			},
			Push:   func(string, string) error { t.Fatal("pushed"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeHeld)
	}
	if res[0].Fault.Kind != FaultTracker {
		t.Errorf("fault = %q, want %q", res[0].Fault.Kind, FaultTracker)
	}
	// Exactly the one failing SetLabels call -- the claim attempt itself --
	// and no requeue call, because a claim that never landed has nothing to
	// hand back.
	if len(j.labelCalls) != 1 {
		t.Errorf("expected exactly the failed claim attempt, got: %v", j.labelCalls)
	}
	if len(j.transitions) != 0 {
		t.Errorf("transitioned a ticket that was never claimed: %v", j.transitions)
	}
}

// The merged check runs before SetLabels claims the ticket (work.go:
// GetIssue -> resolved? -> merged? -> preflight -> claim), so an
// unreachable forge here holds with nothing claimed yet, the same as the
// tracker cases above.
func TestForgeUnreachableOnMergedCheckHolds(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Merged: func(dir, branch string) (bool, string, error) {
				return false, "", errors.New("dial tcp: connection refused")
			},
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("an agent ran although the forge could not be reached")
				return nil, nil
			},
			Push:   func(string, string) error { t.Fatal("pushed"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeHeld)
	}
	if res[0].Fault.Kind != FaultForge {
		t.Errorf("fault = %q, want %q", res[0].Fault.Kind, FaultForge)
	}
	if len(j.labelCalls) != 0 {
		t.Errorf("a claim was made even though the merged check runs before it: %v", j.labelCalls)
	}
}

// A run that spent turns and hit quota is still a failed run, whatever the
// quota fault looks like on its own -- Started is the whole gate.
func TestAQuotaWallWithoutAResetTimeButTurnsSpentIsStillFailed(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 1, QuotaUnwaitable: true, Started: true,
						Reason: "quota exhausted; the provider did not say when it resets"},
					errors.New("stage ticket failed: claude exited 1")
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeFailed)
	}
	if len(Holds(home)) != 0 {
		t.Errorf("a run that spent turns was held instead of failed: %+v", Holds(home))
	}
}

// The literal connectivity wordings the ticket lists. Some of these --
// bare "502"/"503"/"504" with no descriptive body text -- are NOT matched
// by the current connectivity table, which requires the status code to be
// followed by its reason phrase ("502 bad gateway", "503 service
// unavailable", "504 gateway"). A real Jira/gh error is formatted as
// "fetching KEY: <code> <body-snippet>", and an empty or terse gateway body
// leaves only the bare code -- which this test shows is classified as
// "not a fault" today, contrary to the ticket's claim that 502/503/504
// alone are enough.
func TestConnectivityWordingsTheTicketListsAsClassifying(t *testing.T) {
	for _, tc := range []struct {
		err  string
		want bool
	}{
		{"fetching FCIA-6: 502 ", true},
		{"fetching FCIA-6: 503 ", true},
		{"fetching FCIA-6: 504 ", true},
		{"connection refused", true},
		{"no such host", true},
		{"context deadline exceeded", true},
		{"i/o timeout", true},
		{"tls handshake timeout", true},
	} {
		_, got := unreachableFault(FaultTracker, errors.New(tc.err))
		if got != tc.want {
			t.Errorf("unreachableFault(%q) = %v, want %v (per the ticket's connectivity list)",
				tc.err, got, tc.want)
		}
	}
}

// A ticket that ran to a normal ending in between two faults must not carry
// the earlier fault's count into the next one -- tested here through a real
// Run(), not by calling forgetFault directly, so the actual wiring in
// Run()'s loop is what is under test.
func TestARunThatFinishesNormallyForgetsThePriorFaultCount(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder

	// First: the ticket hits quota and is held.
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return quotaRun()
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})
	if len(Holds(home)) != 1 {
		t.Fatal("the first run did not hold")
	}
	if still := Release(home, ReleaseDeps{
		Recheck: func(FaultKind) (string, string) { return "OK", "healthy" },
	}, &out); len(still) != 0 {
		t.Fatalf("the hold did not clear: %+v", still)
	}

	// Second: the ticket runs to completion normally.
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"),
					[]byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://x/pr/1", nil },
		})

	// Third: quota again. Without the reset this would escalate immediately.
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return quotaRun()
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})
	if res[0].Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if h := Holds(home); len(h) != 1 || h[0].Escalated {
		t.Fatalf("a ticket that ran normally in between was escalated on its next first fault: %+v", h)
	}
}

// A person at a terminal without Slack connected still gets a clean reset:
// there is no thread to reply to, and there must not need to be one.
func TestManualResetWithoutSlackStillClears(t *testing.T) {
	home := t.TempDir()
	if _, _, err := RecordFault(home,
		Fault{Kind: FaultClaudeAuth, Cause: "not signed in", Fix: fixClaudeAuth},
		"FCIA-6", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}

	still := Release(home, ReleaseDeps{
		Recheck: func(FaultKind) (string, string) { return "OK", "signed in as a@b" },
		Manual:  true,
	}, &strings.Builder{})

	if len(still) != 0 {
		t.Fatalf("a manual reset without Slack did not clear: %+v", still)
	}
	if len(Holds(home)) != 0 {
		t.Errorf("the hold survived a manual reset with no Slack: %+v", Holds(home))
	}
}

// A hold recorded with no channel or TS -- Slack wasn't connected, or the
// ask itself failed -- has no thread to reply to, but must still clear on a
// healthy manual recheck.
func TestManualResetWithUnrecordedMessageStillClears(t *testing.T) {
	home := t.TempDir()
	fs := &fakeSlack{}
	if _, _, err := RecordFault(home, Fault{Kind: FaultQuota, Cause: "c", Fix: fixQuota},
		"FCIA-6", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}

	still := Release(home, ReleaseDeps{
		Slack:   fs,
		Recheck: func(FaultKind) (string, string) { return "OK", "healthy" },
		Manual:  true,
	}, &strings.Builder{})

	if len(still) != 0 {
		t.Fatalf("did not clear a hold with no recorded Slack message: %+v", still)
	}
	if len(fs.replies) != 0 {
		t.Errorf("replied to a thread that was never created: %v", fs.replies)
	}
}

// `orion reset --held claude-auth` must clear only claude-auth, leaving a
// standing quota hold exactly as it was.
func TestOnlyFlagClearsJustThatFaultAndLeavesOthersStanding(t *testing.T) {
	home := t.TempDir()
	if _, _, err := RecordFault(home, Fault{Kind: FaultClaudeAuth, Cause: "c1", Fix: "f1"},
		"FCIA-6", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecordFault(home, Fault{Kind: FaultQuota, Cause: "c2", Fix: "f2"},
		"FCIA-7", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}

	checked := map[FaultKind]bool{}
	still := Release(home, ReleaseDeps{
		Only: FaultClaudeAuth,
		Recheck: func(k FaultKind) (string, string) {
			checked[k] = true
			return "OK", "healthy"
		},
	}, &strings.Builder{})

	if len(still) != 1 || still[0].Kind != FaultQuota {
		t.Fatalf("the untargeted hold was not left standing: %+v", still)
	}
	if checked[FaultQuota] {
		t.Errorf("the quota hold was re-checked even though --held only asked about claude-auth")
	}
	h := Holds(home)
	if len(h) != 1 || h[0].Kind != FaultQuota {
		t.Fatalf("only claude-auth should have cleared: %+v", h)
	}
}

// The ask names what broke, the fix, and which tickets are waiting -- but a
// ticket is only ever named in the "held" line, not in the title, because
// the question is about the machine.
func TestTheFaultAskNamesKindCauseFixAndOnlyListsTicketsInTheHeldLine(t *testing.T) {
	h := Hold{Kind: FaultQuota, Cause: "quota exhausted; no reset time given",
		Fix: fixQuota, Keys: []string{"FCIA-6", "FCIA-7"}}
	title, body := msgFaultAsk(h)

	if !strings.Contains(title, "quota") {
		t.Errorf("title does not name the fault kind: %q", title)
	}
	for _, want := range []string{"quota exhausted", fixQuota, "FCIA-6, FCIA-7"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(title, "FCIA") {
		t.Errorf("the title named a ticket instead of the fault: %q", title)
	}
}

// Two runs hitting the same fault at once must not both believe they were
// first: RecordFault promises exactly one true, one false, and exactly one
// Slack message. There is no lock in the load-modify-write path (loadHolds
// / writeHolds), so this exercises that promise directly rather than taking
// it on faith.
func TestConcurrentRecordFaultOfTheSameFaultCreatesExactlyOnce(t *testing.T) {
	home := t.TempDir()
	const n = 20
	start := make(chan struct{})
	firsts := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, first, err := RecordFault(home, Fault{Kind: FaultQuota, Cause: "c", Fix: "f"},
				fmt.Sprintf("FCIA-%d", i), "", nil, time.Now())
			if err != nil {
				t.Error(err)
			}
			firsts[i] = first
		}(i)
	}
	close(start)
	wg.Wait()

	count := 0
	for _, f := range firsts {
		if f {
			count++
		}
	}
	if count != 1 {
		t.Errorf("existing=false %d time(s) across %d concurrent RecordFault calls on the same fault, "+
			"want exactly 1 -- otherwise more than one Slack message is posted for one fault", count, n)
	}
	if h := Holds(home); len(h) != 1 || len(h[0].Keys) != n {
		t.Fatalf("the hold does not name every ticket that raced into it: %+v", h)
	}
}

// A corrupted holds.json must be treated as empty, not fatal, and the next
// fault must write a fresh, valid file rather than compounding the damage.
func TestCorruptHoldFileIsTreatedAsEmpty(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(holdPath(home), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if h := Holds(home); len(h) != 0 {
		t.Fatalf("a corrupt hold file was not treated as empty: %+v", h)
	}

	if _, _, err := RecordFault(home, Fault{Kind: FaultQuota, Cause: "c", Fix: "f"},
		"FCIA-6", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if h := Holds(home); len(h) != 1 {
		t.Fatalf("recording after corruption did not produce a fresh, usable file: %+v", h)
	}
}

// withChannel gives the project a Slack room, which is what `orion init`
// records and what makes the question have somewhere to go.
func withChannel(t *testing.T, home string) {
	t.Helper()
	ws, err := workspace.Open(mustWorkspaceID(t, home))
	if err != nil {
		t.Fatal(err)
	}
	ws.Task.Slack = &workspace.SlackChannel{ID: "C-TEST", Name: "orion-fcia"}
	if err := ws.SaveTask(); err != nil {
		t.Fatal(err)
	}
}

// seedHold puts one asked-about fault on disk, as a run that hit it would.
func seedHold(t *testing.T, home string, fs *fakeSlack, approvers []string) {
	t.Helper()
	h, _, err := RecordFault(home,
		Fault{Kind: FaultClaudeAuth, Cause: "the CLI is not signed in", Fix: fixClaudeAuth},
		"FCIA-6", "C123", approvers, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := loadHolds(home)
	h = f.Holds[FaultClaudeAuth]
	h.TS = fs.ts
	f.Holds[FaultClaudeAuth] = h
	if err := writeHolds(home, f); err != nil {
		t.Fatal(err)
	}
}
