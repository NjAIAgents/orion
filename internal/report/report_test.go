package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// seed provisions workspaces with recorded runs, so Build has something real
// to summarise rather than a hand-built Digest.
func seed(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	mk := func(idea, stage, status string, runs []workspace.RunRec, resume time.Time) *workspace.Workspace {
		t.Helper()
		ws, err := workspace.New(workspace.NewOptions{Idea: idea})
		if err != nil {
			t.Fatal(err)
		}
		ws.Task.Stage, ws.Task.Status = stage, status
		ws.Task.Runs, ws.Task.ResumeAt = runs, resume
		if err := ws.SaveTask(); err != nil {
			t.Fatal(err)
		}
		return ws
	}
	now := time.Now()
	a := mk("alpha project", "plan", "ready-for-review", []workspace.RunRec{
		{Stage: "intent", StartedAt: now.Add(-2 * time.Hour), ExitCode: 0},
		{Stage: "plan", StartedAt: now.Add(-1 * time.Hour), ExitCode: 1,
			Reason: "breaker tripped: 400 tool calls", Log: "/tmp/a.log"},
	}, time.Time{})
	mk("beta project", "intent", "waiting-on-quota", []workspace.RunRec{
		{Stage: "intent", StartedAt: now.Add(-30 * time.Minute), ExitCode: 0},
	}, now.Add(2*time.Hour))

	if err := registry.Bind(home, registry.Entry{
		Key: "ALPHA", Source: t.TempDir(), Workspace: a.ID,
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestBuildSummarisesWorkspacesAndFailures(t *testing.T) {
	home := seed(t)
	d := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{}, "")

	if len(d.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(d.Workspaces))
	}
	if len(d.Failures) != 1 {
		t.Fatalf("failures = %d, want the one non-zero exit", len(d.Failures))
	}
	if !strings.Contains(d.Failures[0].Reason, "breaker") {
		t.Errorf("failure = %+v; the reason must survive into the digest", d.Failures[0])
	}
	// A workspace parked on a quota wall is the thing most likely to be
	// silently forgotten, so it must be called out by name.
	joined := strings.Join(d.Attention, " ")
	if !strings.Contains(joined, "quota") {
		t.Errorf("attention = %v; a parked workspace was not surfaced", d.Attention)
	}
}

// A failure older than the window is not news. Including it would make every
// report re-report the same incident forever.
func TestBuildHonoursTheWindow(t *testing.T) {
	home := seed(t)
	d := Build(home, time.Now().Add(-10*time.Minute), budget.Limits{}, "")
	if len(d.Failures) != 0 {
		t.Errorf("failures = %+v; all of them predate the window", d.Failures)
	}
	if len(d.Workspaces) != 2 {
		t.Errorf("the window must narrow FAILURES, not hide workspaces: %d", len(d.Workspaces))
	}
}

// Same vocabulary as `orion logs`: project key, issue key or workspace id.
func TestBuildFiltersByProjectIssueOrWorkspace(t *testing.T) {
	home := seed(t)
	full := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{}, "")
	target := ""
	for _, w := range full.Workspaces {
		if strings.HasPrefix(w.ID, "alpha") {
			target = w.ID
		}
	}
	if target == "" {
		t.Fatal("fixture missing")
	}

	for _, only := range []string{"ALPHA", "alpha", "ALPHA-6", target, target[:6]} {
		d := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{}, only)
		if len(d.Workspaces) != 1 {
			t.Errorf("filter %q gave %d workspaces, want 1", only, len(d.Workspaces))
			continue
		}
		if d.Workspaces[0].ID != target {
			t.Errorf("filter %q selected %s, want %s", only, d.Workspaces[0].ID, target)
		}
		if d.Only != only {
			t.Errorf("Only = %q; the digest must remember it is partial", d.Only)
		}
	}
}

// A filter nobody can resolve must match nothing, not everything. Silently
// widening it answers a question the user did not ask, and they will read
// the result as though they had.
func TestAnUnresolvableFilterMatchesNothing(t *testing.T) {
	home := seed(t)
	d := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{}, "NOSUCHPROJECT")
	if len(d.Workspaces) != 0 {
		t.Errorf("an unknown filter returned %d workspaces; it must return none",
			len(d.Workspaces))
	}
}

// An empty section under a narrowed report reads as "nothing is running"
// unless the report says it is filtered.
func TestTextSaysWhenItIsFiltered(t *testing.T) {
	home := seed(t)
	full := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{}, "").Text()
	if strings.Contains(full, "filtered to") {
		t.Error("an unfiltered report claimed to be filtered")
	}
	narrow := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{}, "ALPHA").Text()
	if !strings.Contains(narrow, "filtered to ALPHA") {
		t.Errorf("a narrowed report did not say so:\n%s", narrow)
	}
}

// The digest travels through a terminal, cron mail and Slack, so it must be
// plain text that needs no renderer.
func TestTextIsPlainAndSelfDescribing(t *testing.T) {
	home := seed(t)
	got := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{}, "").Text()

	if strings.Contains(got, "\x1b[") {
		t.Error("escape codes would be unreadable in cron mail and Slack")
	}
	if !strings.HasPrefix(got, "orion report") {
		t.Errorf("output should identify itself:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if len(line) > 100 {
			t.Errorf("line of %d chars will wrap badly in Slack: %q", len(line), line)
		}
	}
}

// A digest that refuses to render because one workspace is unreadable is
// worse than one that says so.
func TestUnreadableWorkspaceBecomesAttentionNotAnError(t *testing.T) {
	home := seed(t)
	ids, err := workspace.IDs()
	if err != nil || len(ids) == 0 {
		t.Fatal("fixture missing")
	}
	bad := filepath.Join(home, "projects", ids[0], ".orion", "task.json")
	if err := os.WriteFile(bad, []byte("{{{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{}, "")
	if len(d.Attention) == 0 {
		t.Fatal("an unreadable workspace was silently dropped")
	}
	if !strings.Contains(strings.Join(d.Attention, " "), ids[0]) {
		t.Errorf("attention should name the unreadable workspace: %v", d.Attention)
	}
	if strings.TrimSpace(d.Text()) == "" {
		t.Error("the report refused to render")
	}
}

// An unacknowledged checkpoint stops runs, so a report that omits it hides
// the reason nothing is progressing.
func TestBudgetCheckpointIsSurfaced(t *testing.T) {
	home := seed(t)
	l, _ := budget.Load(home)
	l.Record(budget.Run{At: time.Now().UTC(), InputTokens: 900})
	if err := l.Save(home); err != nil {
		t.Fatal(err)
	}
	d := Build(home, time.Now().Add(-24*time.Hour), budget.Limits{WeeklyTokens: 1000}, "")
	joined := strings.Join(d.Attention, " ")
	if !strings.Contains(joined, "budget ack") {
		t.Errorf("attention = %v; the unblocking command must be named", d.Attention)
	}
}

func TestLogsForAndTailLog(t *testing.T) {
	home := seed(t)
	_ = home
	ids, _ := workspace.IDs()
	ws, err := workspace.Open(ids[0])
	if err != nil {
		t.Fatal(err)
	}

	if logs, err := LogsFor(ws); err != nil || len(logs) != 0 {
		t.Fatalf("a workspace with no runs should have no logs: %v %v", logs, err)
	}

	p := filepath.Join(ws.LogsDir(), "20260826-100000-plan-a1.log")
	body := ""
	for i := 1; i <= 100; i++ {
		body += "line\n"
	}
	body += "THE LAST LINE\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	logs, err := LogsFor(ws)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs = %v, err = %v", logs, err)
	}

	// The tail is what matters: a failure appears at the END of a run.
	tail, err := TailLog(logs[0], 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tail, "THE LAST LINE") {
		t.Error("TailLog returned the head instead of the tail")
	}
	if n := strings.Count(strings.TrimSpace(tail), "\n") + 1; n > 5 {
		t.Errorf("asked for 5 lines, got %d", n)
	}
}

func TestTailLogOnAMissingFile(t *testing.T) {
	if _, err := TailLog(filepath.Join(t.TempDir(), "nope.log"), 10); err == nil {
		t.Error("a missing log should be an error, not empty output")
	}
}

func TestHumanAndTruncate(t *testing.T) {
	if got := truncate("abcdefghij", 5); len(got) > 8 {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("a short string was altered: %q", got)
	}
	// human renders counts for a person, so the units must not disappear at
	// the boundaries where they change.
	for _, n := range []int{0, 999, 1000, 999999, 1000000} {
		if got := human(n); strings.TrimSpace(got) == "" {
			t.Errorf("human(%d) rendered as empty", n)
		}
	}
}
