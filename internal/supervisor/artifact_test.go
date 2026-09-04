package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// defaults is the config an unconfigured project loads: no orion.json, so the
// artifact directories are the built-in ones.
func defaults(t *testing.T) config.Config {
	t.Helper()
	return config.Load(t.TempDir())
}

// The mapping is per stage, and it is the whole point of the story that it is
// decided in Go: a stage that owes a file must owe the SAME file whatever
// orion.json says about the command.
func TestStageArtifactIsOneFilePerStage(t *testing.T) {
	cfg := defaults(t)
	for _, tc := range []struct{ stage, want string }{
		{"intent", filepath.Join("docs/intent", "thing.md")},
		{"spec", filepath.Join("specs", "thing.spec.md")},
		{"design", filepath.Join("specs", "thing.spec.md")},
		{"plan", filepath.Join("plans", "thing.plan.md")},
		{"SPEC", filepath.Join("specs", "thing.spec.md")},
		{" Plan ", filepath.Join("plans", "thing.plan.md")},
	} {
		if got := stageArtifact(cfg, tc.stage, "thing"); got != tc.want {
			t.Errorf("stageArtifact(%q) = %q, want %q", tc.stage, got, tc.want)
		}
	}
}

// A stage with no single committed file owes nothing, and a gate that
// demanded one would fail every run of it.
func TestStagesThatProduceNoFileAreSkipped(t *testing.T) {
	cfg := defaults(t)
	for _, stage := range []string{
		"verify", "test", "review", "pr", "ship",
		"build", "implement", "scaffold", "decompose", "ticket", "",
	} {
		if got := stageArtifact(cfg, stage, "thing"); got != "" {
			t.Errorf("stage %q must owe no artifact, got %q", stage, got)
		}
		if err := checkStageArtifact(t.TempDir(), cfg, stage, "thing"); err != nil {
			t.Errorf("stage %q must be skipped, got: %v", stage, err)
		}
	}
}

// The artifact directories follow cfg.Paths -- a project's own chain layout,
// which is Orion's setting -- while nothing a TOOLKIT declares can move them.
func TestNoToolkitCommandCanChangeWhichArtifactAStageOwes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(`{
	  "paths": {"intent": "capture", "specs": "design", "plans": "steps"},
	  "toolkit": {"stages": {"spec": "/some-other-skill", "plan": "/and-another"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Load(dir)
	for _, tc := range []struct{ stage, want string }{
		{"intent", filepath.Join("capture", "thing.md")},
		{"spec", filepath.Join("design", "thing.spec.md")},
		{"plan", filepath.Join("steps", "thing.plan.md")},
	} {
		if got := stageArtifact(cfg, tc.stage, "thing"); got != tc.want {
			t.Errorf("stageArtifact(%q) = %q, want %q", tc.stage, got, tc.want)
		}
	}
}

// gitRepo makes a repository a test can commit into.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	d := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@x")
	run("config", "user.name", "t")
	return d
}

func writeSpec(t *testing.T, repo, body string) string {
	t.Helper()
	rel := filepath.Join("specs", "thing.spec.md")
	if err := os.MkdirAll(filepath.Join(repo, "specs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

// All four states the check distinguishes. The middle two are the ones worth
// having: a half-run command leaves an empty file, and an uncommitted file is
// not a handoff because the next stage reads the repository, not this
// worktree.
func TestArtifactCheckCoversAbsentEmptyUntrackedAndCommitted(t *testing.T) {
	cfg := defaults(t)

	t.Run("absent", func(t *testing.T) {
		repo := gitRepo(t)
		err := checkStageArtifact(repo, cfg, "spec", "thing")
		if err == nil {
			t.Fatal("a stage that wrote nothing must fail")
		}
		if !strings.Contains(err.Error(), "wrote no file") {
			t.Errorf("message must say nothing was written, got: %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		repo := gitRepo(t)
		writeSpec(t, repo, "   \n\t\n")
		err := checkStageArtifact(repo, cfg, "spec", "thing")
		if err == nil {
			t.Fatal("a whitespace-only artifact must fail: the command half-ran")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("message must say the file is empty, got: %v", err)
		}
	})

	t.Run("untracked", func(t *testing.T) {
		repo := gitRepo(t)
		writeSpec(t, repo, "# Spec\n\nreal content\n")
		err := checkStageArtifact(repo, cfg, "spec", "thing")
		if err == nil {
			t.Fatal("an uncommitted artifact must fail: the handoff is the committed file")
		}
		if !strings.Contains(err.Error(), "never committed") {
			t.Errorf("message must say it was never committed, got: %v", err)
		}
	})

	t.Run("committed", func(t *testing.T) {
		repo := gitRepo(t)
		rel := writeSpec(t, repo, "# Spec\n\nreal content\n")
		for _, args := range [][]string{{"add", rel}, {"commit", "-qm", "spec"}} {
			cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		if err := checkStageArtifact(repo, cfg, "spec", "thing"); err != nil {
			t.Fatalf("a committed, non-empty artifact must pass: %v", err)
		}
	})
}

// The message exists to point at the orion.json line that caused the failure,
// so it must name the artifact, the stage AND the configured command. Any one
// of them missing turns the message into a search.
func TestFailureNamesTheArtifactTheStageAndTheCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"toolkit": {"stages": {"spec": "/skil-that-does-not-exist"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkStageArtifact(gitRepo(t), config.Load(dir), "spec", "thing")
	if err == nil {
		t.Fatal("a misconfigured stage must fail")
	}
	for _, want := range []string{
		filepath.Join("specs", "thing.spec.md"), // the artifact
		"spec stage",                            // the stage
		"/skil-that-does-not-exist",             // the configured command
		"toolkit.stages.spec",                   // where to correct it
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must name %q, got:\n%v", want, err)
		}
	}
}

// A project with no toolkit block gets the same check, not an exemption --
// and the message says which prompt ran rather than quoting an empty string.
func TestBuiltInPromptsGetTheSameCheck(t *testing.T) {
	err := checkStageArtifact(gitRepo(t), defaults(t), "plan", "thing")
	if err == nil {
		t.Fatal("a built-in stage that wrote nothing must fail too")
	}
	for _, want := range []string{
		filepath.Join("plans", "thing.plan.md"),
		"plan stage",
		"built-in plan prompt",
		"no toolkit.stages.plan",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must name %q, got:\n%v", want, err)
		}
	}
}

// claudeWriting puts a `claude` on PATH that runs sh -c script inside the
// repo before exiting 0, so a test can make the agent produce -- or fail to
// produce -- its artifact.
func claudeWriting(t *testing.T, repo, script string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	result := `{"type":"result","is_error":false,"terminal_reason":"success","num_turns":1}`
	body := "#!/bin/sh\ncd " + shPath(repo) + "\n" + script + "\necho '" + result + "'\nexit 0\n"
	writeFakeBin(t, "claude", body)
}

// gitWorkspace is ws() with the repo made a real repository, so the committed
// half of the check has something to read.
func gitWorkspace(t *testing.T, cfgJSON string) *workspace.Workspace {
	t.Helper()
	w := ws(t, cfgJSON)
	repo := w.RepoDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@x"}, {"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unusable here: %v\n%s", err, out)
		}
	}
	return w
}

// End to end through Run: the stage's agent exits 0, writes nothing, and the
// run fails at the stage rather than several stages downstream.
func TestRunFailsAStageWhoseCommandLeftNoArtifact(t *testing.T) {
	w := gitWorkspace(t, `{"toolkit": {"stages": {"spec": "/wrong-skill-name"}}}`)
	claudeWriting(t, w.RepoDir(), "true")

	res, err := Run(w, Options{Stage: "spec", MaxMinutes: 1, MaxTurns: 1})
	if err == nil {
		t.Fatal("a stage that left no artifact must fail the run")
	}
	for _, want := range []string{"specs/thing.spec.md", "spec stage", "/wrong-skill-name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must name %q, got:\n%v", want, err)
		}
	}
	if res == nil || res.Reason != "the stage left no artifact" {
		t.Errorf("result = %+v", res)
	}
	if w.Task.Status != "failed" {
		t.Errorf("the task must not be handed on as ready, status = %q", w.Task.Status)
	}
}

// The other half of the same wiring: a stage that DOES commit its artifact
// still passes. Without this the gate could be satisfied by failing always.
func TestRunPassesAStageThatCommittedItsArtifact(t *testing.T) {
	w := gitWorkspace(t, `{"toolkit": {"stages": {"spec": "/some-skill"}}}`)
	claudeWriting(t, w.RepoDir(),
		"mkdir -p specs && printf '# Spec\\n\\nreal content\\n' > specs/thing.spec.md && "+
			"git add specs/thing.spec.md && git commit -qm spec")

	res, err := Run(w, Options{Stage: "spec", MaxMinutes: 1, MaxTurns: 1})
	if err != nil {
		t.Fatalf("a stage that committed its artifact must pass: %v", err)
	}
	if res == nil || res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
}

// A caller that supplied its own prompt asked for something other than the
// stage's artifact -- fix, triage, done, aiops and dba all do -- so the
// artifact contract, which lives in stagePrompt's text, does not apply.
func TestACustomPromptIsNotHeldToTheStageArtifact(t *testing.T) {
	w := gitWorkspace(t, "")
	claudeWriting(t, w.RepoDir(), "true")

	if _, err := Run(w, Options{Stage: "spec", Prompt: "answer a question",
		MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatalf("a custom prompt must not be gated on the stage artifact: %v", err)
	}
}

// The plan stage's artifact path follows the slug, not a fixed name --
// default paths, an arbitrary slug.
func TestPlanStageArtifactPathForDefaultPaths(t *testing.T) {
	cfg := defaults(t)
	want := filepath.Join("plans", "foo.plan.md")
	if got := stageArtifact(cfg, "plan", "foo"); got != want {
		t.Errorf("stageArtifact(plan, foo) = %q, want %q", got, want)
	}
}

// A directory sitting at the artifact's path is not the file the stage owes,
// and must be reported as exactly that rather than folded into "absent" or
// "cannot be read".
func TestArtifactPathThatIsADirectoryFails(t *testing.T) {
	cfg := defaults(t)
	repo := gitRepo(t)
	rel := filepath.Join("specs", "thing.spec.md")
	if err := os.MkdirAll(filepath.Join(repo, rel), 0o755); err != nil {
		t.Fatal(err)
	}

	err := checkStageArtifact(repo, cfg, "spec", "thing")
	if err == nil {
		t.Fatal("a directory at the artifact's path must fail")
	}
	if !strings.Contains(err.Error(), "that path is a directory") {
		t.Errorf("message must say that path is a directory, got: %v", err)
	}
}

// A file that exists but cannot be read (e.g. a permission error) must
// surface os.ReadFile's own error rather than being reported as absent or
// empty.
func TestArtifactThatCannotBeReadFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions, so the unreadable case can't be produced")
	}
	cfg := defaults(t)
	repo := gitRepo(t)
	rel := writeSpec(t, repo, "# Spec\n\nreal content\n")
	full := filepath.Join(repo, rel)
	if err := os.Chmod(full, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(full, 0o644) })

	_, statErr := os.ReadFile(full)
	if statErr == nil {
		t.Skip("this environment does not enforce file permissions")
	}

	err := checkStageArtifact(repo, cfg, "spec", "thing")
	if err == nil {
		t.Fatal("an unreadable artifact must fail")
	}
	if !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("message must say the file cannot be read, got: %v", err)
	}
	if !strings.Contains(err.Error(), statErr.Error()) {
		t.Errorf("message must include os.ReadFile's own error, got: %v", err)
	}
}

// The "design" alias must fail against the canonical "spec" key -- the
// message points at toolkit.stages.spec, the key a reader will actually find
// in orion.json, not a toolkit.stages.design that does not exist.
func TestFailureNamesCanonicalStageForDesignAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"toolkit": {"stages": {"spec": "/some-skill"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkStageArtifact(gitRepo(t), config.Load(dir), "design", "thing")
	if err == nil {
		t.Fatal("a design stage that wrote nothing must fail")
	}
	if !strings.Contains(err.Error(), "toolkit.stages.spec") {
		t.Errorf("message must name the canonical toolkit.stages.spec, got: %v", err)
	}
	if strings.Contains(err.Error(), "toolkit.stages.design") {
		t.Errorf("message must not name a toolkit.stages.design key, got: %v", err)
	}
}

// Whitespace-only content -- spaces, tabs and newlines with nothing else --
// is exactly as empty as a zero-byte file, since strings.TrimSpace strips all
// of it to nothing.
func TestWhitespaceOnlyArtifactFails(t *testing.T) {
	cfg := defaults(t)
	repo := gitRepo(t)
	writeSpec(t, repo, "   \t\t\n\n   \n\t \n")
	err := checkStageArtifact(repo, cfg, "spec", "thing")
	if err == nil {
		t.Fatal("a whitespace-only artifact must fail")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("message must say the file is empty, got: %v", err)
	}
}

// A file written to the worktree but never `git add`-ed is exactly what `git
// ls-files --error-unmatch` is there to catch, independent of the "committed"
// case already covered: this one never even reaches the index.
func TestGitLsFilesCatchesAnUnstagedFile(t *testing.T) {
	cfg := defaults(t)
	repo := gitRepo(t)
	writeSpec(t, repo, "# Spec\n\nreal content\n")

	out, err := exec.Command("git", "-C", repo, "ls-files", "--error-unmatch", "--",
		filepath.Join("specs", "thing.spec.md")).CombinedOutput()
	if err == nil {
		t.Fatalf("git ls-files must report the unstaged file as unmatched, got: %s", out)
	}

	checkErr := checkStageArtifact(repo, cfg, "spec", "thing")
	if checkErr == nil {
		t.Fatal("an unstaged artifact must fail")
	}
	if !strings.Contains(checkErr.Error(), "never committed") {
		t.Errorf("message must say it was never committed, got: %v", checkErr)
	}
}

// An artifact that WAS committed, then removed with `git rm`, leaves no file
// at the path -- the same "absent" branch as a stage that never wrote
// anything, not a fourth state of its own.
func TestArtifactCommittedThenGitRmedFails(t *testing.T) {
	cfg := defaults(t)
	repo := gitRepo(t)
	rel := writeSpec(t, repo, "# Spec\n\nreal content\n")
	for _, args := range [][]string{
		{"add", rel}, {"commit", "-qm", "spec"}, {"rm", "-q", rel}, {"commit", "-qm", "remove spec"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	if _, err := os.Stat(filepath.Join(repo, rel)); !os.IsNotExist(err) {
		t.Fatalf("the file must no longer exist on disk, stat err = %v", err)
	}

	err := checkStageArtifact(repo, cfg, "spec", "thing")
	if err == nil {
		t.Fatal("a committed-then-removed artifact must fail")
	}
	if !strings.Contains(err.Error(), "wrote no file") {
		t.Errorf("message must say the command wrote no file, got: %v", err)
	}
}

// Case does not matter in a toolkit.stages key or a CLI flag, so it must not
// matter here either: upper case, mixed case and surrounding whitespace all
// resolve to the same artifact as the canonical lower-case stage name.
func TestStageArtifactIsCaseAndWhitespaceInsensitive(t *testing.T) {
	cfg := defaults(t)
	for _, tc := range []struct{ stage, want string }{
		{"SPEC", filepath.Join("specs", "thing.spec.md")},
		{"Plan ", filepath.Join("plans", "thing.plan.md")},
		{" INTENT ", filepath.Join("docs/intent", "thing.md")},
	} {
		if got := stageArtifact(cfg, tc.stage, "thing"); got != tc.want {
			t.Errorf("stageArtifact(%q) = %q, want %q", tc.stage, got, tc.want)
		}
	}
}

// An empty stage name owes no artifact and the check must skip it rather than
// fail -- same contract as any other stage with no single committed file.
func TestEmptyStageNameOwesNoArtifact(t *testing.T) {
	cfg := defaults(t)
	if got := stageArtifact(cfg, "", "thing"); got != "" {
		t.Errorf("stageArtifact(\"\") = %q, want \"\"", got)
	}
	if err := checkStageArtifact(t.TempDir(), cfg, "", "thing"); err != nil {
		t.Errorf("an empty stage name must be skipped, got: %v", err)
	}
}
