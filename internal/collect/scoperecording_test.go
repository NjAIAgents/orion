package collect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/queue"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// The done-triage pass is the one place both facts are in hand: the ticket's
// description has just been read for its criteria, and the branch's diff has
// just been read for the verdict (OR-260).
func TestDoneTriageRecordsThePredictedScopeAgainstTheDiff(t *testing.T) {
	home, ws, _ := boundWithAJobWorktree(t)
	jira := newTracker()
	jira.issues = []tracker.Issue{{Key: "FCIA-6", Description: "Files: internal/x"}}
	slack := &slackSpy{}

	collectOnce(t, home, jira, slack, nil,
		PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"})

	s := queue.LoadScopes(ws.Dir)
	if len(s.Predictions) != 1 {
		t.Fatalf("recorded %d predictions, want 1", len(s.Predictions))
	}
	p := s.Predictions[0]
	if p.Key != "FCIA-6" {
		t.Errorf("Key = %q, want FCIA-6", p.Key)
	}
	if len(p.Declared) != 1 || p.Declared[0] != "internal/x" {
		t.Errorf("Declared = %v, want [internal/x]", p.Declared)
	}
}

// A ticket that declared no scope is still recorded -- "how many tickets even
// carry a scope" is a question about the whole population, not only the ones
// that answered it.
func TestATicketThatDeclaredNothingIsRecordedWithAnEmptyDeclaredList(t *testing.T) {
	home, ws, _ := boundWithAJobWorktree(t)
	jira := newTracker()
	jira.issues = []tracker.Issue{{Key: "FCIA-6", Description: "no scope line here"}}
	slack := &slackSpy{}

	collectOnce(t, home, jira, slack, nil,
		PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"})

	s := queue.LoadScopes(ws.Dir)
	if len(s.Predictions) != 1 {
		t.Fatalf("recorded %d predictions, want 1", len(s.Predictions))
	}
	if got := s.Predictions[0].Declared; len(got) != 0 {
		t.Errorf("Declared = %v, want empty: the ticket declared nothing", got)
	}
}

// Recording happens whatever the done-triage pass decides. The evidence
// gathered to reach a verdict is the same evidence the ledger wants, so a NOT
// DONE hand-back still leaves a row -- the prediction was made regardless of
// how the work turned out.
func TestScopeIsRecordedEvenWhenTheVerdictIsNotDone(t *testing.T) {
	home, ws, branch := boundWithAJobWorktree(t)
	wt := jobTree(ws, branch)
	write(t, wt, "internal/x/offbyone_test.go", "package x\n")

	jira := newTracker()
	jira.issues = []tracker.Issue{{Key: "FCIA-6", Description: "Files: internal/x"}}
	slack := &slackSpy{}

	collectOnce(t, home, jira, slack, nil,
		PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"})

	s := queue.LoadScopes(ws.Dir)
	if len(s.Predictions) != 1 || s.Predictions[0].Key != "FCIA-6" {
		t.Fatalf("recorded %v, want one row for FCIA-6 even though the verdict was NOT DONE",
			s.Predictions)
	}
}

// A corrupted or missing ledger costs history, not a verdict: the write is
// dropped rather than allowed to affect the pass in front of it.
func TestACorruptedLedgerDoesNotBlockTheVerdict(t *testing.T) {
	home, ws, _ := boundWithAJobWorktree(t)
	if err := os.MkdirAll(ws.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, "queue-scopes.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	jira := newTracker()
	slack := &slackSpy{}
	collectOnce(t, home, jira, slack, nil,
		PR{Verdict: VerdictPassing, Head: "abc123", URL: "https://example/pr/1"})

	if len(slack.posted) != 1 {
		t.Fatalf("a corrupted ledger changed the verdict: %v", slack.posted)
	}
	// The corrupted file is silently replaced by a fresh, valid one.
	s := queue.LoadScopes(ws.Dir)
	if len(s.Predictions) != 1 {
		t.Fatalf("recorded %d predictions after the corrupted file, want 1", len(s.Predictions))
	}
}
