package collect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// ciApprovalRepo builds a bound project with both the fix loop and Slack
// merge approval switched on -- the exact combination OR-173 hit: a CI
// failure fixed by the loop, then a human approving the merge in Slack.
func ciApprovalRepo(t *testing.T, approvers string) (home, wsDir string) {
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
	cfg := `{"ci":{"auto_fix":true,"max_fix_attempts":3},
	         "slack":{"enabled":true,"require_approval":true,"merge_approvers":[` + approvers + `]},
	         "vcs":{"work_branch":"develop","branch_prefix":"orion/"}}`
	if err := os.WriteFile(filepath.Join(src, "orion.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	ws.Task.Slack = &workspace.SlackChannel{ID: "C1", Name: "fcia"}
	if err := ws.SaveTask(); err != nil {
		t.Fatal(err)
	}
	if err := bindTo(home, ws.ID, src); err != nil {
		t.Fatal(err)
	}
	return home, ws.Dir
}

// statusScript scripts deps.Status across the three passes one episode
// needs: CI red, green-awaiting-approval, and (after the merge command runs)
// actually merged. The mergeSpy's own call count distinguishes the pre-merge
// read from the post-merge recheck that both happen within the same pass.
type statusScript struct {
	phase  int
	m      *mergeSpy
	url    string
	detail string
	head   string
}

func (s *statusScript) status(string, string) (PR, error) {
	switch s.phase {
	case 0:
		return PR{Verdict: VerdictFailing, URL: s.url, Detail: s.detail, Head: s.head}, nil
	case 1:
		return PR{Verdict: VerdictPassing, URL: s.url}, nil
	default:
		if s.m.calls == 0 {
			return PR{Verdict: VerdictPassing, URL: s.url}, nil
		}
		return PR{Verdict: VerdictMerged, URL: s.url}, nil
	}
}

// runEpisode drives one full red -> fixed -> approved -> merged episode and
// returns the console output of the pass that actually merges, which is the
// pass the OR-178 contiguity rule is about.
func runEpisode(t *testing.T, home, approver, failure, head string) string {
	t.Helper()
	m := &mergeSpy{}
	f := &fixSpy{pushed: true}
	s := &slackSpy{fakeSlack: fakeSlack{names: map[string]string{"UNAV": approver}}}
	ss := &statusScript{m: m, url: "https://pr/1", detail: failure, head: head}

	pass := func() string {
		var buf bytes.Buffer
		Run(Options{Home: home, Out: &buf, Keys: []string{"FCIA-6"}}, Deps{
			Jira:    newTracker(),
			Status:  ss.status,
			Refresh: func(string, string) (string, error) { return "", nil },
			Prune:   func(*workspace.Workspace, string) error { return nil },
			Fix:     f.fix,
			Merge:   m.merge,
			Slack:   s,
		})
		return buf.String()
	}

	pass() // phase 0: CI red; the fix loop pushes a fix

	ss.phase = 1
	pass() // phase 1: green; asks for approval in Slack

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"UNAV"}}}
	ss.phase = 2
	return pass() // phase 2: approved -> merges -> re-reads status as merged
}

func indexOfLine(out, substr string) int {
	for i, line := range strings.Split(out, "\n") {
		if strings.Contains(line, substr) {
			return i
		}
	}
	return -1
}

// The exact shape from OR-178: a branch that genuinely failed CI, was fixed,
// approved and merged correctly -- but whose lesson proposal used to print
// between "merged on approval" and "merged into the integration branch", so
// a clean merge read as a merge over a red build.
func TestLessonAnnouncementDoesNotInterruptTheMergeReport(t *testing.T) {
	home, _ := ciApprovalRepo(t, `"navjyot"`)
	failure := `go (ubuntu-latest) (failure)`

	out1 := runEpisode(t, home, "navjyot", failure, "3f38370aaaaaaaa")
	if strings.Contains(out1, "lesson is waiting") {
		t.Fatalf("a single occurrence must not be announced yet -- that is noise, not a pattern:\n%s", out1)
	}

	out2 := runEpisode(t, home, "navjyot", failure, "b1f3bfebbbbbbbb")
	if !strings.Contains(out2, "lesson is waiting") {
		t.Fatalf("the second identical failure should have crossed the two-strike threshold:\n%s", out2)
	}

	approvedLine := indexOfLine(out2, "merged on navjyot's approval")
	mergedIntoLine := indexOfLine(out2, "merged into the")
	if approvedLine < 0 || mergedIntoLine < 0 {
		t.Fatalf("expected both merge report lines, got:\n%s", out2)
	}
	// TEST (OR-178): the merge report for a ticket is contiguous -- no line
	// of another kind may appear between "merged on approval" and "merged
	// into the integration branch".
	if mergedIntoLine != approvedLine+1 {
		t.Errorf("a line of another kind sits between the merge report's two lines:\n%s", out2)
	}

	lessonLine := indexOfLine(out2, "lesson is waiting")
	if lessonLine <= mergedIntoLine {
		t.Errorf("the lesson notice must print after the merge report closes, not before or inside it:\n%s", out2)
	}
	if strings.Contains(out2, "WARNING") && strings.Contains(out2, "lesson is waiting") {
		for _, line := range strings.Split(out2, "\n") {
			if strings.Contains(line, "lesson is waiting") && strings.Contains(line, "WARNING") {
				t.Errorf("a bookkeeping request must not be presented at WARNING level, nothing is wrong: %q", line)
			}
		}
	}
	if !strings.Contains(out2, "an earlier failure on this ticket") {
		t.Errorf("the notice must mark itself as retrospective rather than a bare past-tense clause:\n%s", out2)
	}
	if !strings.Contains(out2, "b1f3bfe") {
		t.Errorf("the notice must name the run it is about (the commit that actually failed):\n%s", out2)
	}
}

// anchorFor is what makes the notice "unmistakably retrospective" (the
// ticket's own phrase): it must name the run, not just say "earlier".
// Missed by the implementer's own test, which only ever exercised a Head
// long enough to truncate -- the empty-Head fallback (an older fix record,
// or a status source that never reported one) never ran once.
func TestAnchorForNamesTheFailingRun(t *testing.T) {
	at := time.Date(2026, 8, 29, 6, 13, 0, 0, time.UTC)
	wantWhen := at.Local().Format("2006-01-02 15:04")

	cases := []struct {
		name string
		head string
		want string
	}{
		{"long head truncates to nine characters", "3f38370aaaaaaaa", "3f38370aa, " + wantWhen},
		{"short head is used as-is, not padded or re-truncated", "abc123", "abc123, " + wantWhen},
		{"no head falls back to the time alone, with no dangling comma", "", wantWhen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := anchorFor(Attempt{At: at, Head: c.head})
			if got != c.want {
				t.Errorf("anchorFor(Head=%q) = %q, want %q", c.head, got, c.want)
			}
			if c.head == "" && strings.Contains(got, ",") {
				t.Errorf("anchor with no head must not carry a dangling comma: %q", got)
			}
		})
	}
}
