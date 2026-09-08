package supervisor

import (
	"strings"
	"testing"
)

func TestAnalyzeVerdictReadsTheCountThroughMarkdown(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{"Critical Issues Count: 2", 2, false},
		{"- **Critical Issues Count**: 0", 0, false},
		{"| Critical Issues Count | 3 |", 3, false},
		{"critical issues count - 1", 1, false},
		{"**Metrics:**\\n- Total Requirements: 12\\n- Critical Issues Count: 4\\n", 4, false},
		{"Critical Issues Count: <N>", 0, true},
		{"no metrics here", 0, true},
		// A label with no count beside it must not borrow one from later.
		{"Critical Issues Count\n\nNext actions: fix 7 things", 0, true},
	}
	for _, c := range cases {
		got, err := analyzeVerdict(c.in)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%q: got %d, err=%v; want %d, err=%v", c.in, got, err, c.want, c.err)
		}
	}
}

// End to end: the agent exits 0 and reports critical issues; the run fails,
// names the count, and the RunRec says so -- a resume must not skip it.
func TestRunBlocksOnAnalyzeCriticalIssues(t *testing.T) {
	w := gitWorkspace(t, `{"toolkit": {"stages": {"analyze": "/speckit-analyze"}}}`)
	claudeWriting(t, w.RepoDir(), "echo '## Report'; echo '- **Critical Issues Count**: 2'")

	res, err := Run(w, Options{Stage: "analyze", MaxMinutes: 1, MaxTurns: 1})
	if err == nil {
		t.Fatal("two critical issues must fail the run")
	}
	for _, want := range []string{"2 critical", "--from analyze"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must say %q, got:\n%v", want, err)
		}
	}
	if res == nil || !strings.Contains(res.Reason, "2 critical") {
		t.Errorf("result = %+v", res)
	}
	if n := len(w.Task.Runs); n == 0 || !strings.Contains(w.Task.Runs[n-1].Reason, "2 critical") {
		t.Errorf("the RunRec must carry the verdict a resume reads: %+v", w.Task.Runs)
	}
	if StageDone(w, "analyze") {
		t.Error("a blocked analyze reports done, so a resume would skip it")
	}
}

func TestRunPassesAnalyzeWithNoCriticalIssues(t *testing.T) {
	w := gitWorkspace(t, `{"toolkit": {"stages": {"analyze": "/speckit-analyze"}}}`)
	claudeWriting(t, w.RepoDir(), "echo 'Critical Issues Count: 0'")

	if _, err := Run(w, Options{Stage: "analyze", MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatalf("zero critical issues failed the run: %v", err)
	}
	if !StageDone(w, "analyze") {
		t.Error("a passed analyze must report done")
	}
}

// A report that never states the count is not a verdict; fail closed.
func TestRunFailsAnalyzeThatStatesNoCount(t *testing.T) {
	w := gitWorkspace(t, `{"toolkit": {"stages": {"analyze": "/speckit-analyze"}}}`)
	claudeWriting(t, w.RepoDir(), "echo 'all good, trust me'")

	_, err := Run(w, Options{Stage: "analyze", MaxMinutes: 1, MaxTurns: 1})
	if err == nil || !strings.Contains(err.Error(), "Critical Issues Count") {
		t.Fatalf("a report without the count must fail naming the line: %v", err)
	}
}
