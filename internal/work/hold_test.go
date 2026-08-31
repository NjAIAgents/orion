package work

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
