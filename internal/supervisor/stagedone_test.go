package supervisor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

// writeAt writes a repo-relative file, creating its directory.
func writeAt(t *testing.T, repo, rel, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The truth table resume trusts. Each row is a state a real workspace can be
// in after a chain stopped, and the answer decides whether a re-run spends a
// stage or skips it.
func TestStageDoneReadsTheArtifactNotAFlag(t *testing.T) {
	w := gitWorkspace(t, `{}`)
	w.Task.Slug = "thing"
	repo := w.RepoDir()

	// Nothing written: not done.
	if StageDone(w, "spec") {
		t.Error("spec is done with no artifact at all")
	}

	// Written but never committed: not done -- the next stage reads the
	// repository, not this worktree.
	writeAt(t, repo, "specs/thing.spec.md", "# Spec\n\nReal content.\n")
	if StageDone(w, "spec") {
		t.Error("spec is done with an untracked artifact")
	}

	// Committed: done.
	commit(t, repo, "specs/thing.spec.md")
	if !StageDone(w, "spec") {
		t.Error("spec is not done with a committed, non-empty artifact")
	}
	// Both spellings answer the same.
	if !StageDone(w, "design") {
		t.Error("design (the spec alias) disagrees with spec")
	}
}

// Intent is done only when it is committed AND has nothing open: a resume
// that skipped it would only stop at the next stage's discovery gate.
func TestStageDoneIntentRequiresNoOpenQuestions(t *testing.T) {
	w := gitWorkspace(t, `{}`)
	w.Task.Slug = "thing"
	repo := w.RepoDir()

	writeAt(t, repo, "docs/intent/thing.md",
		"# Intent\n\n## Open questions\n- Which region do we ship to first?\n")
	commit(t, repo, "docs/intent/thing.md")
	if StageDone(w, "intent") {
		t.Error("intent is done with an open question the next stage would block on")
	}

	writeAt(t, repo, "docs/intent/thing.md",
		"# Intent\n\n## Open questions\n- None\n")
	commit(t, repo, "docs/intent/thing.md")
	if !StageDone(w, "intent") {
		t.Error("intent is not done once its questions are closed and it is committed")
	}
}

// A stage with no artifact is done when its LAST run completed -- not when
// any run did, and not when a run merely exited.
func TestStageDoneWithoutAnArtifactReadsTheLastRun(t *testing.T) {
	w := &workspace.Workspace{ID: "x", Dir: t.TempDir()}
	w.Task.Slug = "thing"

	if StageDone(w, "scaffold") {
		t.Error("scaffold is done with no run recorded")
	}

	w.Task.Runs = []workspace.RunRec{{Stage: "scaffold", ExitCode: 1, Reason: "claude exited 1"}}
	if StageDone(w, "scaffold") {
		t.Error("scaffold is done after only a failed run")
	}

	w.Task.Runs = append(w.Task.Runs, workspace.RunRec{Stage: "scaffold", ExitCode: 0, Reason: "completed"})
	if !StageDone(w, "scaffold") {
		t.Error("scaffold is not done after a completed run")
	}

	// A later failed run un-does it: the last word wins.
	w.Task.Runs = append(w.Task.Runs, workspace.RunRec{Stage: "scaffold", ExitCode: 0, Reason: "breaker tripped: too many edits"})
	if StageDone(w, "scaffold") {
		t.Error("scaffold is done although its last run tripped a breaker")
	}

	// Another stage's run says nothing about this one.
	w.Task.Runs = []workspace.RunRec{{Stage: "decompose", ExitCode: 0, Reason: "completed"}}
	if StageDone(w, "scaffold") {
		t.Error("scaffold is done on the strength of decompose's run")
	}
}

// "Done" skips work, so it is the answer that must never be given by
// accident: a stage Orion does not know is not done.
func TestStageDoneIsFalseForAnUnknownStage(t *testing.T) {
	w := &workspace.Workspace{ID: "x", Dir: t.TempDir()}
	w.Task.Runs = []workspace.RunRec{{Stage: "bogus", ExitCode: 0, Reason: "completed"}}
	if StageDone(w, "bogus") {
		t.Error("an unknown stage reports done because a run with its name exists")
	}
}
