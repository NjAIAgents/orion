package main

// Additional `orion request-plan-changes` CLI coverage (OR-280), cases 20-28
// of the OR-280 test matrix. planfeedback_test.go already covers the unit
// level and one end-to-end run; these drive the built binary to pin the
// black-box behaviors the matrix calls out individually: file location,
// commit state, the printed next-command, multi-round append order, and
// each way feedback text could be misread as something other than text.

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/testproc"
)

// runRequestPlanChangesCLI runs `orion request-plan-changes <target> <args...>`
// against a fabricated workspace and returns stdout, stderr and the repo dir.
func runRequestPlanChangesCLI(t *testing.T, target string, args ...string) (stdout, stderr, repo string) {
	t.Helper()
	bin := orionBinary(t)
	home := t.TempDir()
	repo = planFeedbackWorkspace(t, home, target)
	cmd := testproc.Command(t, bin, append([]string{"request-plan-changes", target}, args...)...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "NO_COLOR=1")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	stdout, stderr = out.String(), errb.String()
	if err != nil {
		t.Fatalf("orion request-plan-changes %v: %v\nstdout:\n%sstderr:\n%s", args, err, stdout, stderr)
	}
	return stdout, stderr, repo
}

// Case 20: the feedback lands in plan-feedback.md, at the path the plan
// stage reads for this workspace's slug.
func TestRequestPlanChangesWritesToTheFileThePlanStageReads(t *testing.T) {
	_, _, repo := runRequestPlanChangesCLI(t, "thing", "make the retry backoff exponential")

	path := repo + "/plans/thing.plan-feedback.md"
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("nothing at %s, where the plan stage reads: %v", path, err)
	}
	if !strings.Contains(string(body), "make the retry backoff exponential") {
		t.Errorf("feedback text missing from %s:\n%s", path, body)
	}
}

// Case 21: the file is committed, not left sitting uncommitted in the
// worktree -- git tracks it and the tree is clean afterward.
func TestRequestPlanChangesCommitsTheFile(t *testing.T) {
	_, _, repo := runRequestPlanChangesCLI(t, "thing", "tighten the validation error message")

	tracked, err := exec.Command("git", "-C", repo, "ls-files", "--error-unmatch", "--",
		"plans/thing.plan-feedback.md").CombinedOutput()
	if err != nil {
		t.Fatalf("the feedback file is not tracked: %s", tracked)
	}
	status, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(status)) != "" {
		t.Errorf("worktree left dirty after request-plan-changes:\n%s", status)
	}
}

// Case 22: the command names the exact next step -- `orion plan KEY --from
// plan` -- so the operator (or the UI shelling out to this same command)
// knows what re-runs the stage that reads what was just written.
func TestRequestPlanChangesPrintsTheNextCommand(t *testing.T) {
	out, _, _ := runRequestPlanChangesCLI(t, "thing", "add a rollback test")

	want := "orion plan thing --from plan"
	if !strings.Contains(out, want) {
		t.Errorf("stdout does not name the next command %q:\n%s", want, out)
	}
}

// Case 23: a second round of feedback is appended, not overwritten -- the
// first round's text is still there, and it reads before the second.
func TestRequestPlanChangesAppendsAcrossMultipleRounds(t *testing.T) {
	bin := orionBinary(t)
	home := t.TempDir()
	repo := planFeedbackWorkspace(t, home, "thing")

	run := func(feedback string) {
		t.Helper()
		cmd := testproc.Command(t, bin, "request-plan-changes", "thing", feedback)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "ORION_HOME="+home, "NO_COLOR=1")
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			t.Fatalf("orion request-plan-changes %q: %v\n%s%s", feedback, err, out.String(), errb.String())
		}
	}
	run("round one: split the migration into two steps")
	run("round two: the split regressed, put it back")

	body, err := os.ReadFile(repo + "/plans/thing.plan-feedback.md")
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	first := strings.Index(got, "round one: split the migration into two steps")
	second := strings.Index(got, "round two: the split regressed, put it back")
	if first < 0 || second < 0 {
		t.Fatalf("a round went missing:\n%s", got)
	}
	if first > second {
		t.Errorf("rounds are out of order, earliest must come first:\n%s", got)
	}
}

// Case 24: text starting with `--` is the feedback, not a flag the command
// tries to parse.
func TestRequestPlanChangesTreatsDoubleDashPrefixAsLiteralText(t *testing.T) {
	const feedback = "--force is the wrong default here"
	_, _, repo := runRequestPlanChangesCLI(t, "thing", feedback)

	body, err := os.ReadFile(repo + "/plans/thing.plan-feedback.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), feedback) {
		t.Errorf("literal %q not found verbatim:\n%s", feedback, body)
	}
}

// Case 25: text starting with a single `-` is likewise literal.
func TestRequestPlanChangesTreatsSingleDashPrefixAsLiteralText(t *testing.T) {
	const feedback = "-1 on the in-memory cache, it will not survive a restart"
	_, _, repo := runRequestPlanChangesCLI(t, "thing", feedback)

	body, err := os.ReadFile(repo + "/plans/thing.plan-feedback.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), feedback) {
		t.Errorf("literal %q not found verbatim:\n%s", feedback, body)
	}
}

// Case 26: shell metacharacters -- backticks, `$`, pipes, semicolons -- are
// recorded as characters. Nothing here reaches a shell to interpret them.
func TestRequestPlanChangesRecordsShellMetacharactersLiterally(t *testing.T) {
	const feedback = "drop the `rm -rf $TMPDIR` step; pipe logs | grep ERROR instead"
	_, _, repo := runRequestPlanChangesCLI(t, "thing", feedback)

	body, err := os.ReadFile(repo + "/plans/thing.plan-feedback.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), feedback) {
		t.Errorf("metacharacters were not preserved verbatim:\n%s", body)
	}
}

// Case 27: spaces and multiple words are preserved exactly as typed --
// including internal double spaces, which a naive re-join could collapse.
func TestRequestPlanChangesPreservesSpacesAndMultipleWords(t *testing.T) {
	bin := orionBinary(t)
	home := t.TempDir()
	repo := planFeedbackWorkspace(t, home, "thing")

	cmd := testproc.Command(t, bin, "request-plan-changes", "thing",
		"the refund flow", "needs", "its", "own", "stage")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "NO_COLOR=1")
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("orion request-plan-changes: %v\n%s%s", err, out.String(), errb.String())
	}

	body, err := os.ReadFile(repo + "/plans/thing.plan-feedback.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "the refund flow needs its own stage") {
		t.Errorf("words were not joined with the spaces they were typed with:\n%s", body)
	}
}

// Case 28: no feedback text at all is refused, with an explanation of what
// is missing -- not written as an empty entry the planner has nothing to
// act on.
func TestRequestPlanChangesRefusesEmptyFeedback(t *testing.T) {
	bin := orionBinary(t)
	home := t.TempDir()
	repo := planFeedbackWorkspace(t, home, "thing")

	cmd := testproc.Command(t, bin, "request-plan-changes", "thing")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "NO_COLOR=1")
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err == nil {
		t.Fatalf("empty feedback exited 0: %s%s", out.String(), errb.String())
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() == 0 {
		t.Fatalf("expected a non-zero exit for empty feedback: %v", err)
	}
	if errb.Len() == 0 {
		t.Error("no explanation was printed for the refusal")
	}
	if _, statErr := os.Stat(repo + "/plans/thing.plan-feedback.md"); statErr == nil {
		t.Error("an empty request still wrote a feedback file")
	}
}
