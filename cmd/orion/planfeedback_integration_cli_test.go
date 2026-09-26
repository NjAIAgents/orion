package main

// Remaining OR-280 test-matrix coverage for `orion request-plan-changes`,
// cases 39-41: identical behavior from either caller of this CLI, the
// commit-success/commit-failure report, and the exit-code contract. Same
// fake-repo-subprocess approach as planfeedback_test.go and
// planfeedback_more_cli_test.go; nothing here reaches a real Jira or a real
// git remote.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/testproc"
)

// timestampHeaderRE matches the per-entry UTC ISO8601 header line
// (`## 2026-09-10T00:44:18Z`) so the two invocations under comparison, made
// a real wall-clock second or two apart, aren't flagged as different for a
// reason that has nothing to do with addressing form.
var timestampHeaderRE = regexp.MustCompile(`## \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`)

// runRequestPlanChangesRaw runs `orion request-plan-changes` with whatever
// target the caller passes (a workspace id, a registered tracker key, ...)
// against the given ORION_HOME, without asserting success -- callers that
// want the identical-behavior comparison need the raw stdout/stderr/exit.
func runRequestPlanChangesRaw(t *testing.T, home, target, feedback string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "request-plan-changes", target, feedback)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "NO_COLOR=1")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running orion request-plan-changes %s %q: %v", target, feedback, err)
	}
	return out.String(), errb.String(), code
}

// Case 39: a terminal invocation names the workspace by the tracker key it
// is registered under; the web UI, which already resolved the workspace once
// to render the gate, shells out with the workspace id directly. Both are
// the SAME command with a different argument, and OR-280's whole point --
// "neither gets a chained agent run it did not ask for" -- only holds if
// they behave identically: same file written, same commit made, same next
// command printed.
func TestRequestPlanChangesBehavesIdenticallyByKeyOrByWorkspaceID(t *testing.T) {
	home := t.TempDir()
	const feedback = "tighten the retry backoff before it ships"

	// Terminal: the repository is registered under a tracker key, and that
	// is what gets typed.
	byKeyRepo := planFeedbackWorkspace(t, home, "byakey")
	if err := registry.Bind(home, registry.Entry{
		Key: "PROJ", Source: t.TempDir(), Workspace: "byakey",
	}); err != nil {
		t.Fatal(err)
	}
	outKey, errKey, codeKey := runRequestPlanChangesRaw(t, home, "PROJ", feedback)

	// Web UI: an identical workspace, addressed by the id the UI already
	// holds, with no registry entry at all.
	byIDRepo := planFeedbackWorkspace(t, home, "byid")
	outID, errID, codeID := runRequestPlanChangesRaw(t, home, "byid", feedback)

	if codeKey != 0 || codeID != 0 {
		t.Fatalf("expected both to succeed: by-key exit %d (%s%s), by-id exit %d (%s%s)",
			codeKey, outKey, errKey, codeID, outID, errID)
	}

	normalize := func(s string) string {
		s = strings.ReplaceAll(s, "byakey", "SLUG")
		s = strings.ReplaceAll(s, "byid", "SLUG")
		return timestampHeaderRE.ReplaceAllString(s, "## TIMESTAMP")
	}
	if normalize(outKey) != normalize(outID) {
		t.Errorf("stdout differs by addressing form:\nby key: %s\nby id:  %s", outKey, outID)
	}
	if errKey != errID {
		t.Errorf("stderr differs by addressing form:\nby key: %s\nby id:  %s", errKey, errID)
	}

	keyBody, err := os.ReadFile(filepath.Join(byKeyRepo, "plans", "byakey.plan-feedback.md"))
	if err != nil {
		t.Fatal(err)
	}
	idBody, err := os.ReadFile(filepath.Join(byIDRepo, "plans", "byid.plan-feedback.md"))
	if err != nil {
		t.Fatal(err)
	}
	if normalize(string(keyBody)) != normalize(string(idBody)) {
		t.Errorf("the written feedback differs by addressing form:\nby key: %s\nby id:  %s", keyBody, idBody)
	}
}

// Case 40, success half: a commit that succeeds is reported as such.
func TestRequestPlanChangesReportsCommitSuccess(t *testing.T) {
	home := t.TempDir()
	repo := planFeedbackWorkspace(t, home, "thing")
	out, _, code := runRequestPlanChangesRaw(t, home, "thing", "name the rollback test")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s", code, out)
	}
	if !strings.Contains(out, "committed") {
		t.Errorf("a successful commit is not reported: %s", out)
	}
	tracked, err := exec.Command("git", "-C", repo, "ls-files", "--error-unmatch", "--",
		"plans/thing.plan-feedback.md").CombinedOutput()
	if err != nil {
		t.Errorf("reported committed but the file is not tracked: %s", tracked)
	}
}

// Case 40, failure half: a repository that cannot take a commit (no git
// repository at all, here -- the same failure shape as a busy or misconfigured
// git) still writes the feedback, still reports why it was not committed, and
// still exits 0. The text is not lost, and a script driving this command does
// not have to treat "committed" as the only path to success.
func TestRequestPlanChangesReportsCommitFailureButSucceedsAnyway(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "nogit")
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(dir, ".orion"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	task := []byte(`{"id":"nogit","slug":"nogit","idea":"an idea"}`)
	if err := os.WriteFile(filepath.Join(dir, ".orion", "task.json"), task, 0o644); err != nil {
		t.Fatal(err)
	}
	// Deliberately no `git init`: there is nothing for `git add`/`git commit`
	// to succeed against.

	out, errOut, code := runRequestPlanChangesRaw(t, home, "nogit", "add a rollback test")
	if code != 0 {
		t.Fatalf("a commit failure must not be fatal, got exit %d: %s%s", code, out, errOut)
	}
	if !strings.Contains(out, "feedback written but not committed") {
		t.Errorf("the commit failure is not reported: %s", out)
	}
	body, err := os.ReadFile(filepath.Join(repo, "plans", "nogit.plan-feedback.md"))
	if err != nil {
		t.Fatalf("the feedback was not written despite the commit failure: %v", err)
	}
	if !strings.Contains(string(body), "add a rollback test") {
		t.Errorf("the written file does not hold the feedback text: %s", body)
	}
}

// Case 41: the exit code is a contract a script can check without parsing
// prose -- 0 on success, non-zero on failure -- checked together so a change
// that breaks one direction shows up here.
func TestRequestPlanChangesExitCodeContract(t *testing.T) {
	home := t.TempDir()

	t.Run("success", func(t *testing.T) {
		planFeedbackWorkspace(t, home, "ok")
		_, _, code := runRequestPlanChangesRaw(t, home, "ok", "make the retry exponential")
		if code != 0 {
			t.Errorf("a successful request exited %d, want 0", code)
		}
	})

	t.Run("failure", func(t *testing.T) {
		_, _, code := runRequestPlanChangesRaw(t, home, "no-such-workspace", "anything")
		if code == 0 {
			t.Errorf("a request against an unknown workspace exited 0, want non-zero")
		}
	})
}
