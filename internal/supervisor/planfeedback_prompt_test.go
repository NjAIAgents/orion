package supervisor

// The plan stage is where an operator's requested changes have to land
// (OR-280). `orion request-plan-changes` writes them into the repository; if
// the prompt never names that file, the text was recorded and the planner
// never reads it -- a gate response that changes nothing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// writeFeedback puts a feedback file where PlanFeedbackArtifact says it goes.
func writeFeedback(t *testing.T, repoDir string, cfg config.Config, slug, body string) string {
	t.Helper()
	rel := PlanFeedbackArtifact(cfg, slug)
	abs := filepath.Join(repoDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestPlanPromptSendsTheOperatorsRequestedChangesToThePlanner(t *testing.T) {
	w := ws(t, "")
	rel := writeFeedback(t, w.RepoDir(), config.Load(w.RepoDir()), w.Task.Slug,
		"# Plan feedback\n\n## 2026-09-09T00:00:00Z\n\n```\nthe refund flow needs its own stage\n```\n")

	p, err := stagePrompt(w, "plan", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, rel) {
		t.Fatalf("the plan prompt never names %s, so the feedback reaches nobody:\n%s", rel, p)
	}
	for _, want := range []string{"ASKED FOR CHANGES", "REVISION"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt does not tell the planner this run is a revision (%q):\n%s", want, p)
		}
	}
}

// No feedback, no note: a first plan run must be exactly the prompt it always
// was, which is what the golden snapshot pins. This says the same thing in
// terms of the file, so a failure here names the cause.
func TestPlanPromptSaysNothingAboutFeedbackWhenNoneWasGiven(t *testing.T) {
	w := ws(t, "")
	p, err := stagePrompt(w, "plan", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "plan-feedback") || strings.Contains(strings.ToLower(p), "asked for changes") {
		t.Errorf("the plan prompt mentions feedback that does not exist:\n%s", p)
	}
}

// An empty file is what a half-finished write leaves. Pointing the planner at
// it would spend a revision run reading nothing.
func TestPlanPromptIgnoresAnEmptyFeedbackFile(t *testing.T) {
	w := ws(t, "")
	writeFeedback(t, w.RepoDir(), config.Load(w.RepoDir()), w.Task.Slug, "")

	p, err := stagePrompt(w, "plan", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "plan-feedback") {
		t.Errorf("an empty feedback file was sent to the planner anyway:\n%s", p)
	}
}

// The feedback follows the plan's own layout, both of them: a delegated plan
// stage writes into the feature directory, and feedback left under the
// built-in path would be feedback that stage never reads.
func TestPlanFeedbackArtifactFollowsWhicheverLayoutThePlanUses(t *testing.T) {
	builtin := config.Load(t.TempDir())
	if got, want := PlanFeedbackArtifact(builtin, "thing"), "plans/thing.plan-feedback.md"; got != want {
		t.Errorf("built-in layout: got %q, want %q", got, want)
	}

	delegated := config.Load(t.TempDir())
	delegated.Toolkit.Stages = map[string]string{"plan": "/speckit-plan"}
	if got, want := PlanFeedbackArtifact(delegated, "thing"), "specs/001-thing/plan-feedback.md"; got != want {
		t.Errorf("delegated layout: got %q, want %q", got, want)
	}
}
