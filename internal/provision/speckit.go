package provision

// Installing spec-kit into the workspace repository (docs/decisions/0022).
//
// spec-kit is not a skills repository to clone: `specify init` writes its
// commands into the project as .claude/skills/speckit-*/SKILL.md, from
// templates bundled inside the CLI. So it is installed per project, by the
// chain, before the first stage that would read it -- a frame step in
// Orion's own process, the same class of provisioning as creating the
// remote. Once is enough: a resumed chain finds .specify/ and moves on.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SpecKitInstall is how the `specify` CLI is installed on a machine that
// lacks it. Named in every message that finds it missing, so the fix is on
// screen rather than in a manual.
const SpecKitInstall = "uv tool install specify-cli --from git+https://github.com/github/spec-kit.git"

// SpecKitDir is the directory spec-kit keeps its own state in, and the
// signal that a project has been initialised.
const SpecKitDir = ".specify"

// InitSpecKit initialises spec-kit in dir and commits what it wrote.
// Returns whether it did anything: false when the project is already
// initialised, which is the resumed-chain case and not an error.
//
// Non-interactive on purpose. `specify init` chooses an integration by
// prompt when it can, and a chain has nobody at the prompt; --non-interactive
// makes it use the named integration and fail rather than hang when a choice
// has no default. --force is required to initialise a directory that is not
// empty, which a provisioned workspace never is.
func InitSpecKit(dir string) (bool, error) {
	if st, err := os.Stat(filepath.Join(dir, SpecKitDir)); err == nil && st.IsDir() {
		return false, nil
	}
	bin, err := exec.LookPath("specify")
	if err != nil {
		return false, fmt.Errorf("the specify CLI is not on PATH, and this project's stages delegate to spec-kit.\n"+
			"  Install it:  %s\n"+
			"  Then re-run: orion plan", SpecKitInstall)
	}
	cmd := exec.Command(bin, "init", "--here", "--force", "--non-interactive", "--integration", "claude")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("specify init failed in %s: %v\n%s", dir, err, strings.TrimSpace(string(out)))
	}
	if st, err := os.Stat(filepath.Join(dir, SpecKitDir)); err != nil || !st.IsDir() {
		return false, fmt.Errorf("specify init exited 0 but left no %s/ in %s", SpecKitDir, dir)
	}

	// Committed, because the handoff between stages is tracked files and
	// the next stage's agent runs against the repository, not this
	// worktree. Nothing to commit is not an error: an installer that wrote
	// only ignored files has still installed.
	if out, err := git(dir, "add", "-A"); err != nil {
		return true, fmt.Errorf("staging what specify init wrote: %s", out)
	}
	if _, err := git(dir, "diff", "--cached", "--quiet"); err == nil {
		return true, nil
	}
	if out, err := git(dir, "commit", "-q", "-m",
		"chore: install spec-kit into the workspace\n\n"+
			"Written by `specify init --here --integration claude`, run by Orion before\n"+
			"the first stage that reads it (docs/decisions/0022)."); err != nil {
		return true, fmt.Errorf("committing what specify init wrote: %s", out)
	}
	return true, nil
}
