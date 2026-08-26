package collect

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// slackSpy is a full SlackAPI: it both answers and records.
type slackSpy struct {
	fakeSlack
	posted   []string
	reacted  []string
	ts       string
	postErr  error
	postedTo string
}

func (s *slackSpy) PostTS(channel, text string) (string, error) {
	if s.postErr != nil {
		return "", s.postErr
	}
	s.posted = append(s.posted, text)
	s.postedTo = channel
	if s.ts == "" {
		s.ts = "1700000000.000100"
	}
	return s.ts, nil
}
func (s *slackSpy) React(_, _, emoji string) { s.reacted = append(s.reacted, emoji) }
func (s *slackSpy) BotID() string            { return bot }

type mergeSpy struct {
	calls  int
	reason string
	err    error
}

func (m *mergeSpy) merge(_, _, reason string) error {
	m.calls++
	m.reason = reason
	return m.err
}

// approvalRepo builds a bound project whose orion.json requires approval.
func approvalRepo(t *testing.T, approvers string) (home string, wsDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("ORION_HOME", home)

	src := filepath.Join(t.TempDir(), "fcia")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(workspace.NewOptions{Idea: "fcia"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := `{"slack":{"enabled":true,"require_approval":true,"merge_approvers":[` + approvers + `]},
	         "vcs":{"work_branch":"develop","branch_prefix":"orion/"}}`
	if err := os.WriteFile(filepath.Join(ws.RepoDir(), "orion.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// A channel on the workspace, or the approval path has nowhere to ask.
	ws.Task.Slack = &workspace.SlackChannel{ID: "C1", Name: "fcia"}
	if err := ws.SaveTask(); err != nil {
		t.Fatal(err)
	}
	if err := bindTo(home, ws.ID, src); err != nil {
		t.Fatal(err)
	}
	return home, ws.Dir
}

func runApproval(t *testing.T, home string, s *slackSpy, m *mergeSpy, pr PR, opts Options) (Result, string) {
	t.Helper()
	var buf bytes.Buffer
	opts.Home, opts.Out = home, &buf
	if len(opts.Keys) == 0 {
		opts.Keys = []string{"FCIA-6"}
	}
	res := Run(opts, Deps{
		Jira:    newTracker(),
		Status:  func(string, string) (PR, error) { return pr, nil },
		Refresh: func(string, string) (string, error) { return "", nil },
		Prune:   func(*workspace.Workspace, string) error { return nil },
		Merge:   m.merge,
		Slack:   s,
	})
	return res[0], buf.String()
}

// The first pass asks and stops. Waiting inside the collector for a human is
// the blocking design that made `orion work` release the job slot at all.
func TestTheFirstPassAsksAndDoesNotMerge(t *testing.T) {
	home, _ := approvalRepo(t, `"navjyot"`)
	s, m := &slackSpy{}, &mergeSpy{}

	_, out := runApproval(t, home, s, m, PR{Verdict: VerdictPassing, URL: "https://pr/1"}, Options{})

	if len(s.posted) != 1 {
		t.Fatalf("expected exactly one approval request, got %d", len(s.posted))
	}
	if m.calls != 0 {
		t.Fatal("merged without waiting for an answer")
	}
	if !strings.Contains(out, "asked for approval") {
		t.Errorf("got: %s", out)
	}
	// The request must be capable of being refused: it names the diff and
	// says who may answer.
	if !strings.Contains(s.posted[0], "https://pr/1") {
		t.Error("the request does not link the diff, so approving is easier than checking")
	}
	if !strings.Contains(s.posted[0], "navjyot") {
		t.Error("the request does not say who may approve")
	}
	if len(s.reacted) != 2 {
		t.Errorf("expected tick and cross affordances, got %v", s.reacted)
	}
}

// Asking twice would put two requests in the channel and leave two messages
// that could each carry an approval.
func TestASecondPassDoesNotAskAgain(t *testing.T) {
	home, _ := approvalRepo(t, `"navjyot"`)
	s, m := &slackSpy{}, &mergeSpy{}
	pr := PR{Verdict: VerdictPassing, URL: "https://pr/1"}

	runApproval(t, home, s, m, pr, Options{})
	runApproval(t, home, s, m, pr, Options{})

	if len(s.posted) != 1 {
		t.Fatalf("asked %d times; the request must be remembered between passes", len(s.posted))
	}
}

// The whole point: an allowlisted tick merges.
func TestAnApprovedRequestMerges(t *testing.T) {
	home, _ := approvalRepo(t, `"navjyot"`)
	s, m := &slackSpy{}, &mergeSpy{}
	s.names = map[string]string{"UNAV": "navjyot", bot: "orion"}
	pr := PR{Verdict: VerdictPassing, URL: "https://pr/1"}

	runApproval(t, home, s, m, pr, Options{}) // asks

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"UNAV"}}}
	_, out := runApproval(t, home, s, m, pr, Options{})

	if m.calls != 1 {
		t.Fatalf("expected one merge, got %d", m.calls)
	}
	// Who authorised it must survive into the repository's history: the
	// Slack message is not part of it, and the commit is where anyone will
	// look six months later.
	if !strings.Contains(m.reason, "navjyot") {
		t.Errorf("the merge commit does not record who approved: %q", m.reason)
	}
	if !strings.Contains(out, "navjyot") {
		t.Errorf("got: %s", out)
	}
}

// A tick from the bot itself is the affordance it added. Counting it would
// make every request self-approving.
func TestOrionsOwnAffordanceDoesNotMerge(t *testing.T) {
	home, _ := approvalRepo(t, `"navjyot"`)
	s, m := &slackSpy{}, &mergeSpy{}
	pr := PR{Verdict: VerdictPassing, URL: "https://pr/1"}

	runApproval(t, home, s, m, pr, Options{})
	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{bot}}}
	runApproval(t, home, s, m, pr, Options{})

	if m.calls != 0 {
		t.Fatal("Orion merged on the strength of its own reaction")
	}
}

// Declining is not a failure. The build was fine and the agent did nothing
// wrong; a person made a decision about the change.
func TestADeclineStopsTheMergeAndDoesNotMarkTheTicketFailed(t *testing.T) {
	home, _ := approvalRepo(t, `"navjyot"`)
	s, m := &slackSpy{}, &mergeSpy{}
	s.names = map[string]string{"UNAV": "navjyot"}
	pr := PR{Verdict: VerdictPassing, URL: "https://pr/1"}

	runApproval(t, home, s, m, pr, Options{})
	s.reactions = []slack.Reaction{{Name: "x", Users: []string{"UNAV"}}}
	_, out := runApproval(t, home, s, m, pr, Options{})

	if m.calls != 0 {
		t.Fatal("merged despite a decline")
	}
	if !strings.Contains(out, "declined") {
		t.Errorf("got: %s", out)
	}
}

// An approval that cannot be acted on must not be consumed. The person said
// yes; a transient merge failure should retry, not ask them again.
func TestAFailedMergeKeepsTheApprovalForTheNextPass(t *testing.T) {
	home, wsDir := approvalRepo(t, `"navjyot"`)
	s := &slackSpy{fakeSlack: fakeSlack{names: map[string]string{"UNAV": "navjyot"}}}
	m := &mergeSpy{err: errors.New("base branch was modified")}
	pr := PR{Verdict: VerdictPassing, URL: "https://pr/1"}

	runApproval(t, home, s, m, pr, Options{})
	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"UNAV"}}}
	res, _ := runApproval(t, home, s, m, pr, Options{})

	if res.Err == nil {
		t.Fatal("a failed merge must be reported")
	}
	if _, still := loadRequests(wsDir).Requests["FCIA-6"]; !still {
		t.Fatal("the request was cleared, so the next pass will ask the same person again")
	}
}

// Dry run must not post. A request in a channel is visible to people and
// cannot be taken back.
func TestDryRunDoesNotAskAnybody(t *testing.T) {
	home, _ := approvalRepo(t, `"navjyot"`)
	s, m := &slackSpy{}, &mergeSpy{}

	_, out := runApproval(t, home, s, m,
		PR{Verdict: VerdictPassing, URL: "https://pr/1"}, Options{DryRun: true})

	if len(s.posted) != 0 {
		t.Fatal("dry run posted a real approval request")
	}
	if !strings.Contains(out, "would") {
		t.Errorf("got: %s", out)
	}
}

// With approval off, the collector reports and waits. That is the default,
// and it needs none of the extra OAuth scopes.
func TestApprovalDisabledFallsBackToWaiting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	src := filepath.Join(t.TempDir(), "fcia")
	_ = os.MkdirAll(src, 0o755)
	ws, err := workspace.New(workspace.NewOptions{Idea: "fcia"})
	if err != nil {
		t.Fatal(err)
	}
	if err := bindTo(home, ws.ID, src); err != nil {
		t.Fatal(err)
	}
	s, m := &slackSpy{}, &mergeSpy{}

	_, out := runApproval(t, home, s, m, PR{Verdict: VerdictPassing, URL: "u"}, Options{})

	if len(s.posted) != 0 || m.calls != 0 {
		t.Fatal("approval is off; nothing should have been asked or merged")
	}
	if !strings.Contains(out, "waiting for you to merge") {
		t.Errorf("got: %s", out)
	}
}
