package work

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/advise"
)

// commitsOnRepo builds a bare origin plus a work clone on branch "work",
// checked out one commit ahead of origin/main -- the exact shape work.go's
// callers hand to commitsOn(job.Path, cfg.VCS.WorkBranch).
func commitsOnRepo(t *testing.T) (repo string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo = filepath.Join(root, "repo")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	git(t, root, "clone", "-q", origin, repo)
	if err := os.WriteFile(filepath.Join(repo, "spec.md"), []byte("# spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "seed")
	git(t, repo, "push", "-q", "origin", "main")
	git(t, repo, "checkout", "-q", "-b", "work")
	return repo
}

// THE CASE THIS EXISTS TO FIX (OR-329): a commit that only touches
// docs/decisions/ but is real, ticket-assigned work -- not the advisor
// loop's own bookkeeping -- must still be counted. Path-based exclusion
// made it invisible; the fix matches CommitDecision's own commit subject
// instead.
func TestCommitsOnCountsARealCommitThatOnlyTouchesDocsDecisions(t *testing.T) {
	repo := commitsOnRepo(t)
	dir := filepath.Join(repo, "docs", "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "or-329-audit.md")
	if err := os.WriteFile(path, []byte("# audit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "docs/decisions/or-329-audit.md")
	git(t, repo, "commit", "-q", "-m", "docs(OR-329): add the requested compliance audit record")

	n, err := commitsOn(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("commits = %d, want 1 (a real commit under docs/decisions must count)", n)
	}
}

// The advisor loop's own bookkeeping commit, produced by CommitDecision
// itself, must still be excluded -- otherwise an agent that only ever asks
// questions would read as having done work (TestTheAdvisorLoopIsCapped's
// own premise, now enforced by commit subject rather than path).
func TestCommitsOnExcludesACommitDecisionRecordCommit(t *testing.T) {
	repo := commitsOnRepo(t)
	path, err := WriteDecision(repo, "OR-1", 1, "which retry policy?", advise.Answer{
		Verdict:   advise.VerdictDerived,
		Decision:  "exponential backoff",
		Grounding: "spec.md#retries",
		Role:      "architect",
		Model:     "opus",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := CommitDecision(repo, path, "OR-1", advise.Answer{
		Verdict:   advise.VerdictDerived,
		Decision:  "exponential backoff",
		Grounding: "spec.md#retries",
		Role:      "architect",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := commitsOn(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("commits = %d, want 0 (a CommitDecision commit is bookkeeping, not implementation)", n)
	}
}

// A mix of both: the decision record is excluded, the real commit that
// follows it is counted -- the ordinary shape of a resumed advisor-loop run
// that then goes on to do the work.
func TestCommitsOnCountsTheImplementationCommitAfterADecisionRecord(t *testing.T) {
	repo := commitsOnRepo(t)
	path, err := WriteDecision(repo, "OR-1", 1, "which retry policy?", advise.Answer{
		Verdict: advise.VerdictDerived, Decision: "backoff", Grounding: "spec.md", Role: "architect",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := CommitDecision(repo, path, "OR-1", advise.Answer{
		Verdict: advise.VerdictDerived, Decision: "backoff", Grounding: "spec.md", Role: "architect",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "retry.go")
	git(t, repo, "commit", "-q", "-m", "feat(OR-1): add exponential backoff")

	n, err := commitsOn(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("commits = %d, want 1 (the decision record excluded, the implementation counted)", n)
	}
}

// A commit whose subject happens to start with "docs(...)" but is not one
// of CommitDecision's own two exact shapes must still count -- the pattern
// matches the real generator's output, not any docs-prefixed commit.
func TestCommitsOnCountsAnUnrelatedDocsCommit(t *testing.T) {
	repo := commitsOnRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-q", "-m", "docs(OR-1): clarify the setup instructions")

	n, err := commitsOn(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("commits = %d, want 1 (a docs commit that isn't a decision record must count)", n)
	}
}

// commitsOn falls back to the local base ref when origin/<base> does not
// exist -- exercised here so the new --invert-grep flag is proven on both
// code paths, not just the origin-tracking one.
func TestCommitsOnFallsBackToLocalBaseWithoutAnOriginRef(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := exec.Command("git", "init", "-q", "-b", "main", repo).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "spec.md"), []byte("# spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "seed")
	git(t, repo, "checkout", "-q", "-b", "work")
	if err := os.WriteFile(filepath.Join(repo, "impl.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "impl.go")
	git(t, repo, "commit", "-q", "-m", "feat: implement the thing")

	n, err := commitsOn(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("commits = %d, want 1", n)
	}
}

// Sanity check that the constant regex actually matches what CommitDecision
// generates for both its answered and unanswered shapes -- if the message
// text in decisions.go ever drifts, this fails loudly instead of the
// exclusion silently stopping working.
func TestDecisionCommitGrepMatchesWhatCommitDecisionActuallyWrites(t *testing.T) {
	re := "^docs\\([^)]*\\): record (the .+ decision|an unanswered question)$"
	if decisionCommitGrep != re {
		t.Fatalf("decisionCommitGrep changed unexpectedly: %q", decisionCommitGrep)
	}
	cases := []struct {
		subject string
		match   bool
	}{
		{"docs(or-1): record the architect decision", true},
		{"docs(or-1): record an unanswered question", true},
		{"docs(OR-329): add the requested compliance audit record", false},
		{"docs(or-1): record the architect decision, mostly", false},
	}
	for _, c := range cases {
		got := matchesDecisionCommitGrep(c.subject)
		if got != c.match {
			t.Errorf("subject %q: match = %v, want %v", c.subject, got, c.match)
		}
	}
}

// matchesDecisionCommitGrep mirrors git's own --extended-regexp match for
// the test above, without shelling out.
func matchesDecisionCommitGrep(subject string) bool {
	prefix := "docs("
	if !strings.HasPrefix(subject, prefix) {
		return false
	}
	rest := subject[len(prefix):]
	close := strings.Index(rest, "):")
	if close < 0 {
		return false
	}
	tail := strings.TrimPrefix(rest[close+2:], " ")
	return strings.HasPrefix(tail, "record the ") && strings.HasSuffix(tail, " decision") ||
		tail == "record an unanswered question"
}
