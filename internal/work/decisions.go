package work

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/advise"
)

// Decision records.
//
// Committed, on the task branch, so they land in the pull request a reviewer
// reads. A Slack thread cannot do that: it is not in the diff, and it is not
// in the next agent's context.
//
// The compounding property is the point. docs/decisions/ becomes grounding
// for later questions -- the architect reads it alongside spec.md, so a
// decision made on FCIA-6 is available to FCIA-7 without anyone remembering
// it happened. Without that, the same ambiguity is re-litigated, at full
// price, on every ticket that touches it.

// WriteDecision records one question and its answer, and returns the path.
func WriteDecision(repoDir, key string, n int, question string, a advise.Answer) (string, error) {
	dir := filepath.Join(repoDir, "docs", "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%02d.md", strings.ToLower(key), n)
	path := filepath.Join(dir, name)

	status := string(a.Verdict)
	if a.Verdict == advise.VerdictRefused || a.Verdict == advise.VerdictEscalate {
		status = string(a.Verdict) + " (a person must decide, and the artifact should then say so)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s decision %02d\n\n", strings.ToUpper(key), n)
	fmt.Fprintf(&b, "- **asked by** the implementer, while working %s\n", strings.ToUpper(key))
	fmt.Fprintf(&b, "- **answered by** %s (%s)\n", a.Role, a.Model)
	fmt.Fprintf(&b, "- **status** %s\n", status)
	fmt.Fprintf(&b, "- **at** %s\n\n", time.Now().UTC().Format(time.RFC3339))

	fmt.Fprintf(&b, "## Question\n\n%s\n\n", strings.TrimSpace(question))

	if a.Answered() {
		fmt.Fprintf(&b, "## Decision\n\n%s\n\n", strings.TrimSpace(a.Decision))
		fmt.Fprintf(&b, "## Grounding\n\n%s\n\n", strings.TrimSpace(a.Grounding))
	} else {
		fmt.Fprintf(&b, "## Not decided\n\n%s\n\n", strings.TrimSpace(a.Reason))
		b.WriteString("The design does not answer this. Decide it, then amend the " +
			"artifact it belongs in, so the next ticket does not ask again.\n\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// CommitDecision commits the record on the current branch.
//
// A separate commit from the implementation, deliberately: the decision was
// made before the code that follows from it, and squashing them together
// hides which parts of the change the decision actually caused.
func CommitDecision(repoDir, path, key string, a advise.Answer) error {
	rel, err := filepath.Rel(repoDir, path)
	if err != nil {
		rel = path
	}
	if out, err := exec.Command("git", "-C", repoDir, "add", rel).CombinedOutput(); err != nil {
		return fmt.Errorf("staging %s: %s", rel, strings.TrimSpace(string(out)))
	}
	subject := fmt.Sprintf("docs(%s): record the %s decision", strings.ToLower(key), a.Role)
	body := "Asked during implementation and answered from the committed design.\n\n" +
		"Grounding: " + a.Grounding
	if !a.Answered() {
		subject = fmt.Sprintf("docs(%s): record an unanswered question", strings.ToLower(key))
		body = "The design does not decide this. Recorded so it is not re-asked " +
			"silently on the next ticket.\n\n" + a.Reason
	}
	out, err := exec.Command("git", "-C", repoDir, "commit", "-q", "-m", subject, "-m", body).CombinedOutput()
	if err != nil {
		return fmt.Errorf("committing %s: %s", rel, strings.TrimSpace(string(out)))
	}
	return nil
}

// AnswerMessage is what gets sent back into the implementer's session.
//
// It names the source, because an answer that arrives as bare instruction is
// indistinguishable from the supervisor having decided -- and the implementer
// should weigh "the spec says X" differently from "someone told me X".
func AnswerMessage(a advise.Answer, recordPath string) string {
	return strings.Join([]string{
		fmt.Sprintf("The %s answered your question.", a.Role),
		"",
		strings.TrimSpace(a.Decision),
		"",
		"Grounding: " + strings.TrimSpace(a.Grounding),
		"",
		"This is recorded in " + recordPath + ", which is already committed on",
		"your branch. Continue the implementation on that basis. If it",
		"contradicts something you have already written, change what you wrote:",
		"the design decides, not the code.",
	}, "\n")
}
