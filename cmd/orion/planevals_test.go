package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

const agentSpec = "# Spec\n\n## Agentic design\n\n### Eval plan\n\n```json\n" + `{
  "gate": {"pass_rate": 0.9, "min_cases": 20},
  "harness": {"command": "cat", "replays_against": "echo"},
  "cases": [{"name": "names-the-cause", "prompt": "disk full", "must_contain": ["disk"],
             "discriminates": "blames the network"}]
}` + "\n```\n"

// evalsWS is a workspace with a real git repo and, when spec is non-empty, a
// committed spec at the path the artifact gate reads.
func evalsWS(t *testing.T, spec string) *workspace.Workspace {
	t.Helper()
	w := chainWS(t)
	repo := w.RepoDir()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-q")
	mustGit(t, repo, "config", "user.email", "t@example.com")
	mustGit(t, repo, "config", "user.name", "t")
	if spec != "" {
		rel := supervisor.SpecArtifact(config.Load(repo), w.Task.Slug)
		abs := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(spec), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	return w
}

func TestEvalsStepScaffoldsAndCommitsForAnAgentShapedSpec(t *testing.T) {
	w := evalsWS(t, agentSpec)
	if evalsDone(w) {
		t.Fatal("evalsDone reports done before anything was scaffolded")
	}
	var out bytes.Buffer
	if err := evalsStep(&stepIO{Out: &out}, w); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"evals/cases/names-the-cause.json", "evals/run.sh", "evals/gate.json", ".github/workflows/agent-evals.yml"} {
		if _, err := os.Stat(filepath.Join(w.RepoDir(), f)); err != nil {
			t.Errorf("%s not written", f)
		}
	}
	// Committed, or remote would push the branches without it.
	if st, _ := gitIn(w.RepoDir(), "status", "--porcelain", "--", "evals", ".github"); strings.TrimSpace(st) != "" {
		t.Errorf("the eval suite was left uncommitted:\n%s", st)
	}
	if !evalsDone(w) {
		t.Error("evalsDone does not see the suite it just wrote")
	}
}

func TestEvalsStepDoesNothingWithoutAnEvalPlan(t *testing.T) {
	for name, spec := range map[string]string{
		"no spec":                "",
		"spec, not agent-shaped": "# Spec\n\nA REST service.\n",
	} {
		w := evalsWS(t, spec)
		if !evalsDone(w) {
			t.Errorf("%s: evalsDone should be true so the chain skips it without asking", name)
		}
		var out bytes.Buffer
		if err := evalsStep(&stepIO{Out: &out}, w); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(w.RepoDir(), "evals")); err == nil {
			t.Errorf("%s: evals/ was created for a project with no eval plan", name)
		}
	}
}

// A malformed plan warns and leaves the chain running: the project still
// builds without evals, and the spec can be fixed and the step re-run.
func TestEvalsStepWarnsOnAMalformedPlan(t *testing.T) {
	w := evalsWS(t, "## Agentic design\n\n### Eval plan\n\n```json\n{\"cases\": [\n```\n")
	var out bytes.Buffer
	if err := evalsStep(&stepIO{Out: &out}, w); err != nil {
		t.Fatalf("a malformed plan stopped the chain: %v", err)
	}
	if !strings.Contains(out.String(), "evals:") {
		t.Errorf("the malformed plan went unreported:\n%s", out.String())
	}
}
