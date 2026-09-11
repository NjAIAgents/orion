package main

// Additional coverage for `orion request-plan-changes` (OR-280) beyond what
// planfeedback_test.go already asserts: the delegated-toolkit layout, the
// unknown-project refusal's next step, the timestamp format, and that the
// command records input without ever running the stage itself.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/testproc"
)

// A key naming no workspace must fail with a next step a person can act on,
// not just a bare error.
func TestCLIRequestPlanChangesUnknownProjectNamesTheNextStep(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "request-plan-changes", "NOSUCH", "please change the plan")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir(), "NO_COLOR=1")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		t.Fatalf("an unknown project exited 0: %s%s", out.String(), errb.String())
	}
	combined := out.String() + errb.String()
	if !strings.Contains(combined, "orion repos") || !strings.Contains(combined, "orion ls") {
		t.Errorf("no next step for an unresolved project: %s", combined)
	}
}

// A delegated plan stage (spec-kit and the like) keeps its feedback beside
// its own plan, under FeatureDir -- not under plans/, which is where a
// built-in stage's plan lives.
func TestCLIRequestPlanChangesWritesToFeatureDirForDelegatedPlanStage(t *testing.T) {
	bin := orionBinary(t)
	home := t.TempDir()
	repo := planFeedbackWorkspace(t, home, "thing")

	orionJSON, err := json.Marshal(map[string]any{
		"toolkit": map[string]any{
			"stages": map[string]string{"plan": "/speckit-plan"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "orion.json"), orionJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "add", "orion.json").CombinedOutput(); err != nil {
		t.Fatalf("git add orion.json: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "commit", "-qm", "delegate the plan stage").CombinedOutput(); err != nil {
		t.Fatalf("git commit orion.json: %v\n%s", err, out)
	}

	const feedback = "the spec-kit plan needs a rollback section"
	cmd := testproc.Command(t, bin, "request-plan-changes", "thing", feedback)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "NO_COLOR=1")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("orion request-plan-changes: %v\n%s%s", err, out.String(), errb.String())
	}

	wantPath := filepath.Join(repo, "specs", "001-thing", "plan-feedback.md")
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("nothing written at the delegated layout's path %s: %v\n%s", wantPath, err, out.String())
	}
	if !strings.Contains(string(body), feedback) {
		t.Errorf("the feedback was not stored verbatim:\n%s", body)
	}

	// Not also under plans/, which is the built-in stage's layout.
	if _, err := os.Stat(filepath.Join(repo, "plans", "thing.plan-feedback.md")); !os.IsNotExist(err) {
		t.Errorf("feedback also landed under the built-in layout's path")
	}
}

// The heading on every entry is UTC and RFC3339 (ISO 8601), regardless of
// what time.Time the caller passes in -- so several rounds recorded from
// different machines still sort and read the same way.
func TestPlanFeedbackEntryTimestampIsUTCISO8601(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	at := time.Date(2026, 9, 9, 8, 0, 0, 0, loc)

	entry := planFeedbackEntry(at, "some feedback")
	lines := strings.SplitN(entry, "\n", 2)
	heading := strings.TrimPrefix(lines[0], "## ")

	if !strings.HasSuffix(heading, "Z") {
		t.Fatalf("heading %q is not UTC (no trailing Z)", heading)
	}
	parsed, err := time.Parse(time.RFC3339, heading)
	if err != nil {
		t.Fatalf("heading %q does not parse as RFC3339: %v", heading, err)
	}
	if !parsed.Equal(at) {
		t.Errorf("heading timestamp %v does not represent the same instant as %v", parsed, at)
	}
	if want := at.UTC().Format(time.RFC3339); heading != want {
		t.Errorf("heading is %q, want %q -- a non-UTC input was not converted before formatting", heading, want)
	}
}

// The command's job is to record the operator's words and name the command
// that acts on them -- never to run the plan stage itself. Running it would
// make the terminal and the web UI (which shells out to this same command)
// behave differently depending on whether an agent run happened to be
// triggered underneath.
func TestCLIRequestPlanChangesDoesNotRunThePlanStage(t *testing.T) {
	bin := orionBinary(t)
	home := t.TempDir()
	repo := planFeedbackWorkspace(t, home, "thing")

	cmd := testproc.Command(t, bin, "request-plan-changes", "thing", "make the plan better")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "NO_COLOR=1")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("orion request-plan-changes: %v\n%s%s", err, out.String(), errb.String())
	}

	// No plan artifact was produced -- the only thing this command writes is
	// the feedback file, never a (re)plan.
	if _, err := os.Stat(filepath.Join(repo, "plans", "thing.plan.md")); !os.IsNotExist(err) {
		t.Errorf("a plan was written; the command must only record feedback, not run the stage")
	}

	// The next step is NAMED, not executed: the exact command and flag
	// appear in the output as text for the operator to run themselves.
	if !strings.Contains(out.String(), "orion plan thing --from plan") {
		t.Errorf("the exact re-run command is not named verbatim: %s", out.String())
	}
}
