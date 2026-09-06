package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// scaffoldRepo makes an empty git repo and scaffolds it, the way provisioning
// does.
func scaffoldRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@x"}, {"config", "user.name", "t"},
	} {
		if out, err := gitCmd(repo, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := scaffoldChain(&Workspace{RepoPath: repo}); err != nil {
		t.Fatalf("scaffoldChain: %v", err)
	}
	return repo
}

// The scaffold wrote files and never committed them, so every stage ran
// against a repository where NOTHING was tracked -- and the first stage that
// owed a committed artifact failed on exactly that. The comment in the code
// claimed the directories were "visible from the first commit"; they were not.
func TestTheScaffoldIsCommitted(t *testing.T) {
	repo := scaffoldRepo(t)

	out, err := gitCmd(repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if out != "" {
		t.Errorf("the scaffold left untracked files:\n%s", out)
	}
	if tracked, _ := gitCmd(repo, "ls-files"); !strings.Contains(tracked, "orion.json") {
		t.Errorf("orion.json is not tracked; ls-files:\n%s", tracked)
	}
}

// The directory names were hardcoded as "intent" while the config this same
// function writes says docs/intent -- so provisioning made a directory no
// stage would use, and the stage that read the config made a second one.
func TestTheScaffoldMakesTheDirectoriesTheConfigNames(t *testing.T) {
	repo := scaffoldRepo(t)
	cfg := config.Load(repo)

	for _, dir := range []string{cfg.Paths.Intent, cfg.Paths.Specs, cfg.Paths.Plans, cfg.Paths.Evals} {
		if _, err := os.Stat(filepath.Join(repo, dir)); err != nil {
			t.Errorf("the config names %s and the scaffold did not make it: %v", dir, err)
		}
	}
	// And no stray directory beside the configured one.
	if cfg.Paths.Intent != "intent" {
		if _, err := os.Stat(filepath.Join(repo, "intent")); err == nil {
			t.Errorf("a bare intent/ was made beside the configured %s", cfg.Paths.Intent)
		}
	}
}

// Provisioning an existing workspace rewrites nothing, and `git commit` exits
// non-zero on an empty index -- so a second call must not report a failure.
func TestScaffoldingTwiceIsNotAnError(t *testing.T) {
	repo := scaffoldRepo(t)
	if err := scaffoldChain(&Workspace{RepoPath: repo}); err != nil {
		t.Fatalf("a second scaffold failed: %v", err)
	}
	n, err := exec.Command("git", "-C", repo, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(n)) != "1" {
		t.Errorf("scaffolding twice made %s commits, want 1", strings.TrimSpace(string(n)))
	}
}

// An existing orion.json is a project's own configuration and must survive
// provisioning.
func TestAnExistingConfigIsNotOverwritten(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@x"}, {"config", "user.name", "t"},
	} {
		if out, err := gitCmd(repo, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mine := `{"paths":{"intent":"my-intent","specs":"my-specs","plans":"p","evals":"e"}}`
	if err := os.WriteFile(filepath.Join(repo, "orion.json"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := scaffoldChain(&Workspace{RepoPath: repo}); err != nil {
		t.Fatalf("scaffoldChain: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(repo, "orion.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != mine {
		t.Errorf("the project's own config was overwritten:\n%s", got)
	}
	// And the scaffold followed IT, not the default.
	if _, err := os.Stat(filepath.Join(repo, "my-intent")); err != nil {
		t.Errorf("the scaffold ignored the project's own intent path: %v", err)
	}
}
