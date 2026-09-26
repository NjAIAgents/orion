package supervisor

// Case 46 of the OR-280 test matrix: the planner has to be able to actually
// revise from what `orion request-plan-changes` wrote, not merely have the
// path named in its prompt. planfeedback_prompt_test.go already pins that
// the path is named and that a REVISION note is attached; this pins that the
// path the prompt names is the file every round of feedback landed in, so a
// planner reading it (as the prompt tells it to) sees the operator's actual
// words for each round rather than only the latest one.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

func TestPlanPromptPathHoldsEveryRoundOfFeedbackForThePlannerToActOn(t *testing.T) {
	w := ws(t, "")
	cfg := config.Load(w.RepoDir())
	body := "# Plan feedback\n\n" +
		"## 2026-09-08T00:00:00Z\n\n```\nsplit the migration into two steps\n```\n\n" +
		"## 2026-09-09T00:00:00Z\n\n```\nthe split regressed, put it back\n```\n"
	rel := writeFeedback(t, w.RepoDir(), cfg, w.Task.Slug, body)

	p, err := stagePrompt(w, "plan", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, rel) {
		t.Fatalf("the prompt does not name %s at all:\n%s", rel, p)
	}

	// What the prompt actually gives the planner to work from: the file at
	// the named path, read from disk exactly as a planner run would.
	onDisk, err := os.ReadFile(filepath.Join(w.RepoDir(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("the path the prompt names does not exist: %v", err)
	}
	for _, want := range []string{
		"split the migration into two steps",
		"the split regressed, put it back",
	} {
		if !strings.Contains(string(onDisk), want) {
			t.Errorf("a round of feedback is missing from the file the prompt points at (%q):\n%s",
				want, onDisk)
		}
	}
	// Round order matters: the planner is told to "address every point", and
	// a plan that undoes round one only to have round two undo it back
	// depends on reading them oldest-first, exactly as written on disk.
	first := strings.Index(string(onDisk), "split the migration into two steps")
	second := strings.Index(string(onDisk), "the split regressed, put it back")
	if first < 0 || second < 0 || first > second {
		t.Errorf("rounds are not in the order a planner would need to read them in:\n%s", onDisk)
	}
}
