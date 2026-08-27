package ciscaffold

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The environment half of "one standard way to run the tests".
//
// scripts/test.sh already looks for a virtualenv in the MAIN worktree, via
// git rev-parse --git-common-dir, precisely because a git worktree does not
// carry the ignored files a normal checkout has. That logic is correct. The
// problem is that there was never anything for it to find: the sandbox clone
// is a fresh clone, a fresh clone has no .venv, and the system python cannot
// import pytest -- so every run fell through to the script's bootstrap
// branch, which builds a virtualenv INSIDE the per-ticket worktree, once per
// ticket, and exits 1 with no environment when it cannot.
//
// That failure is indistinguishable from a test failure. CI runs the same
// script, so the branch goes red for a reason unrelated to the change, and
// with ci.auto_fix on Orion pays an agent to react to an environment problem
// it cannot fix from inside a worktree.
//
// So the virtualenv is built ONCE, here, in the sandbox clone at adoption
// time. Every worktree then finds it through the fallback that already
// exists, and the discovery is paid for once per sandbox instead of once per
// ticket.

// VenvResult reports what EnsureVenv did, so adoption can say it rather than
// leaving a several-hundred-megabyte directory to be discovered later.
type VenvResult struct {
	// Path is the virtualenv that now exists, empty when none was built.
	Path string
	// Action is one of: created, refreshed, current, skipped.
	Action string
	// Reason explains a skip. Empty otherwise.
	Reason string
	// Notes carry what did not work but did not warrant failing.
	Notes []string
}

// venvManifests are the files that mean "this project declares Python
// dependencies". Presence is the whole condition: building an empty
// virtualenv for a repository that never asked for one is a surprise, not a
// help, which is why scripts/test.sh guards its own bootstrap the same way.
var venvManifests = []string{"requirements.txt", "pyproject.toml"}

// depsStamp records when the dependencies were last installed. pyvenv.cfg
// would be the obvious marker and is the wrong one: it is written when the
// virtualenv is CREATED and never touched again, so a refresh would leave it
// claiming the install happened before the dependency change that triggered
// the refresh.
const depsStamp = ".orion-deps-installed"

// EnsureVenv builds, or refreshes, the sandbox clone's virtualenv.
//
// Refreshes when a manifest is newer than the last install. That is the
// cheapest honest answer to "what happens when dependencies change": it does
// not parse a lockfile or hash a dependency set, it just notices that the
// file it installed from has been edited since. It over-installs on a
// touched-but-unchanged manifest, which costs a pip run, and it under-installs
// for nothing -- the right direction for a check whose failure mode is a
// stale environment that looks like a broken test.
func EnsureVenv(dir string) (VenvResult, error) {
	res := VenvResult{Action: "skipped"}

	present := manifestsIn(dir)
	if len(present) == 0 {
		res.Reason = "no requirements.txt or pyproject.toml; this project declares no Python dependencies"
		return res, nil
	}

	py, err := exec.LookPath("python3")
	if err != nil {
		if py, err = exec.LookPath("python"); err != nil {
			res.Reason = "no python3 on PATH"
			return res, nil
		}
	}

	venv := filepath.Join(dir, ".venv")
	res.Path = venv
	venvPy := filepath.Join(venv, "bin", "python")

	switch _, err := os.Stat(venvPy); {
	case err == nil:
		if fresh(dir, filepath.Join(venv, depsStamp), present) {
			res.Action = "current"
			return res, nil
		}
		res.Action = "refreshed"
	default:
		if out, err := exec.Command(py, "-m", "venv", venv).CombinedOutput(); err != nil {
			res.Path = ""
			return res, fmt.Errorf("creating %s: %w\n%s", venv, err, out)
		}
		res.Action = "created"
	}

	// Keep it out of git's way before installing anything. The sandbox clone
	// is fast-forwarded between runs, and that is refused when the tree is
	// dirty -- so an untracked .venv/ in a repository whose .gitignore does
	// not happen to list one would silently freeze the sandbox at the commit
	// it was created from. info/exclude is the right tool: local to this
	// clone, and it never appears in a diff the user has to review.
	//
	// Not redundant with the .gitignore that venv writes inside itself: that
	// arrived in Python 3.11, and the system python3 on a current macOS is
	// 3.9. Relying on it would make the failure depend on the interpreter.
	if err := excludeLocally(dir, ".venv/"); err != nil {
		res.Notes = append(res.Notes, "could not add .venv/ to .git/info/exclude: "+err.Error())
	}

	if err := installDeps(venvPy, dir, present, &res); err != nil {
		return res, err
	}

	if err := os.WriteFile(filepath.Join(venv, depsStamp), nil, 0o644); err != nil {
		res.Notes = append(res.Notes, "could not stamp the install; the next run will reinstall: "+err.Error())
	}

	if err := exec.Command(venvPy, "-c", "import pytest").Run(); err != nil {
		res.Notes = append(res.Notes,
			"pytest is not importable in the virtualenv; scripts/test.sh will try to bootstrap one per worktree "+
				"unless this project declares pytest as a dependency")
	}
	return res, nil
}

// installDeps runs the same installs scripts/test.sh would, so the sandbox
// environment is the one the script expects rather than a second opinion.
func installDeps(venvPy, dir string, present []string, res *VenvResult) error {
	pip := func(args ...string) ([]byte, error) {
		cmd := exec.Command(venvPy, append([]string{"-m", "pip", "install", "--quiet"}, args...)...)
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}

	for _, m := range present {
		switch m {
		case "requirements.txt":
			if out, err := pip("-r", "requirements.txt"); err != nil {
				return fmt.Errorf("installing requirements.txt: %w\n%s", err, out)
			}
		case "pyproject.toml":
			// Dev extras first, then the bare project. A repository with no
			// [dev] extra is normal, and failing on it would refuse an
			// environment that would have worked.
			if _, err := pip("-e", ".[dev]"); err == nil {
				continue
			}
			if out, err := pip("-e", "."); err != nil {
				// Not fatal: a pyproject.toml that is not installable (no build
				// backend, a metadata-only file) is common, and the virtualenv
				// plus requirements.txt may still be a working environment.
				res.Notes = append(res.Notes,
					"pyproject.toml is present but not installable: "+firstLine(string(out)))
			}
		}
	}
	return nil
}

// manifestsIn returns the dependency manifests this project actually has, in
// install order: requirements.txt first, because a pyproject install may
// depend on what it pins.
func manifestsIn(dir string) []string {
	var out []string
	for _, m := range venvManifests {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// fresh reports whether the last install is newer than every manifest.
// A missing stamp is stale: it means the virtualenv came from somewhere
// other than this code, and reinstalling is the safe direction.
func fresh(dir, stamp string, manifests []string) bool {
	st, err := os.Stat(stamp)
	if err != nil {
		return false
	}
	for _, m := range manifests {
		mi, err := os.Stat(filepath.Join(dir, m))
		if err != nil {
			continue
		}
		if mi.ModTime().After(st.ModTime()) {
			return false
		}
	}
	return true
}

// excludeLocally adds a pattern to .git/info/exclude, once.
func excludeLocally(dir, pattern string) error {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Output()
	if err != nil {
		return nil // not a git repository; nothing to exclude it from
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	info := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return err
	}
	path := filepath.Join(info, "exclude")
	if b, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == pattern {
				return nil
			}
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# Orion's sandbox virtualenv, built once per sandbox.\n%s\n", pattern)
	return err
}

// DescribeVenv renders what EnsureVenv did, for the adoption summary.
func DescribeVenv(r VenvResult) []string {
	var out []string
	switch r.Action {
	case "created":
		out = append(out, "created "+r.Path+" so every worktree finds one instead of building its own")
	case "refreshed":
		out = append(out, "reinstalled dependencies in "+r.Path+" (a manifest changed since the last install)")
	case "current":
		out = append(out, r.Path+" is current")
	case "skipped":
		if r.Reason != "" {
			out = append(out, "no virtualenv: "+r.Reason)
		}
	}
	return append(out, r.Notes...)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
