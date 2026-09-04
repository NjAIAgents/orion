package work

// Red before green (OR-156).
//
// QA writes its tests AFTER the implementer has already committed, which is
// the gap: a test written against code that already does the right thing can
// pass without ever exercising the behaviour it claims to check, and a
// regression later slips straight through it. The only way to know a test
// would actually have caught something is to watch it fail first.
//
// Orion already knows the commit this ticket's branch started from -- it cut
// the branch from there -- so the proof is mechanical rather than aspirational:
// take each test file QA added or changed, lay it onto the pre-change commit
// in an ephemeral worktree, and run this repository's own suite. A test that
// is still green there proves nothing about the change and is reported as
// such; nothing here blocks the run, because QA does not get that authority
// either (see qa.go).
//
// Scoped to whole files, not individual test functions: Orion has one
// contract for running a repository's tests -- scripts/test.sh -- and no
// language-general way to ask it for a single test's verdict. A file QA
// touched is the finest unit this can name without inventing a per-framework
// test selector, and it is named as such in what gets reported.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/suite"
)

// redGreenTimeout bounds one run of the repository's suite against the
// pre-change checkout. A var, not a const, so a test can shrink it.
var redGreenTimeout = 10 * time.Minute

// redGreenResult is what checking one QA stage's tests against the pre-change
// commit found.
type redGreenResult struct {
	// Skipped is why nothing was checked at all -- no base commit, no
	// scripts/test.sh, or QA touched no test file. Set means Proven, Unproven
	// and Unclear are all empty.
	Skipped string
	// Proven are the test files that failed at the pre-change commit, as the
	// red-green discipline requires.
	Proven []string
	// Unproven are test files that already passed at the pre-change commit --
	// so they do not exercise this ticket's change and would not catch a
	// regression in it either.
	Unproven []string
	// Unclear are test files the check could not resolve either way, with why.
	// Reported rather than dropped: an inconclusive check is not a pass.
	Unclear []redGreenUnclear
}

type redGreenUnclear struct {
	File   string
	Reason string
}

// checkRedBeforeGreen finds the test files QA introduced or changed since
// preQA, overlays each -- alone -- onto the pre-change commit, and runs the
// repository's own suite against that composite state.
func checkRedBeforeGreen(repoDir, baseSHA, preQA string) redGreenResult {
	if strings.TrimSpace(baseSHA) == "" {
		return redGreenResult{Skipped: "no base commit was recorded for this branch"}
	}
	if strings.TrimSpace(preQA) == "" {
		return redGreenResult{Skipped: "could not tell which commits are QA's own"}
	}
	if _, err := os.Stat(filepath.Join(repoDir, "scripts", "test.sh")); err != nil {
		return redGreenResult{Skipped: "this repository has no scripts/test.sh to run the check with"}
	}

	files, err := changedTestFiles(repoDir, preQA)
	if err != nil {
		return redGreenResult{Skipped: "could not tell which test files QA touched: " + err.Error()}
	}
	if len(files) == 0 {
		return redGreenResult{Skipped: "QA did not add or change a test file"}
	}

	tmp, err := os.MkdirTemp("", "orion-redgreen-")
	if err != nil {
		return redGreenResult{Skipped: "could not prepare a checkout of the pre-change commit: " + err.Error()}
	}
	defer os.RemoveAll(tmp)
	if _, err := runGit(repoDir, "worktree", "add", "--detach", "--quiet", tmp, baseSHA); err != nil {
		return redGreenResult{Skipped: "could not check out the pre-change commit: " + err.Error()}
	}
	defer func() { _, _ = runGit(repoDir, "worktree", "remove", "--force", tmp) }()

	var res redGreenResult
	for _, f := range files {
		// Reset to a clean pre-change tree before every file: the previous
		// iteration's overlay must not leak into this one, or a file that
		// only passes alongside another new test would be misjudged.
		if err := resetToBase(tmp, baseSHA); err != nil {
			res.Unclear = append(res.Unclear, redGreenUnclear{f,
				"could not reset the pre-change checkout: " + err.Error()})
			continue
		}
		if err := overlayFile(repoDir, tmp, f); err != nil {
			res.Unclear = append(res.Unclear, redGreenUnclear{f,
				"could not bring the test onto the pre-change checkout: " + err.Error()})
			continue
		}
		ok, err := runSuite(tmp)
		if err != nil {
			res.Unclear = append(res.Unclear, redGreenUnclear{f, "could not run the suite: " + err.Error()})
			continue
		}
		if ok {
			res.Unproven = append(res.Unproven, f)
		} else {
			res.Proven = append(res.Proven, f)
		}
	}
	return res
}

// changedTestFiles lists the test files added or modified between since and
// HEAD. Deletions are excluded (--diff-filter=ACMR): a test QA removed is not
// one it needs to prove failed.
func changedTestFiles(dir, since string) ([]string, error) {
	out, err := runGit(dir, "diff", "--name-only", "--diff-filter=ACMR", since, "HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && isTestFile(line) {
			files = append(files, line)
		}
	}
	return files, nil
}

// isTestFile recognises the test-naming conventions of the stacks ciscaffold
// already knows how to scaffold a suite for (Go, Python, Node) -- the same
// set, for the same reason: naming a convention this package cannot actually
// run a check for would report on nothing.
func isTestFile(path string) bool {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"):
		return true
	case strings.HasSuffix(base, "_test.py"):
		return true
	case strings.Contains(filepath.ToSlash(path), "/__tests__/"):
		return true
	}
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx"} {
		if strings.HasSuffix(base, ".test"+ext) || strings.HasSuffix(base, ".spec"+ext) {
			return true
		}
	}
	return false
}

// resetToBase discards whatever the previous file's overlay left behind, so
// each test is checked alone against the pre-change tree.
func resetToBase(dir, baseSHA string) error {
	if _, err := runGit(dir, "checkout", "--quiet", "-f", baseSHA, "--", "."); err != nil {
		return err
	}
	_, err := runGit(dir, "clean", "-fdx", "--quiet", "--", ".")
	return err
}

// overlayFile writes path's content, as committed at repoDir's HEAD, into
// dst at the same relative location -- the pre-change tree plus exactly one
// test file QA wrote, and nothing else about the implementation.
func overlayFile(repoDir, dst, path string) error {
	cmd := exec.Command("git", "-C", repoDir, "show", "HEAD:"+path)
	content, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("reading %s from HEAD: %w", path, err)
	}
	full := filepath.Join(dst, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, 0o644)
}

// runSuite runs this repository's one entry point for its tests (see
// ciscaffold) and reports whether it passed. A non-zero exit is a completed,
// failing run -- exactly what a red proof needs, so it is not an error; only
// a suite that could not be run at all is.
func runSuite(dir string) (ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), redGreenTimeout)
	defer cancel()
	// Through suite.ScriptCommand rather than exec'ing the script directly:
	// Windows needs the interpreter named, and this is the same argv the
	// suite package builds for the same file (OR-341).
	argv := suite.ScriptCommand(filepath.Join(dir, "scripts", "test.sh"))
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if runErr := cmd.Run(); runErr != nil {
		if _, isExit := runErr.(*exec.ExitError); isExit {
			return false, nil
		}
		return false, runErr
	}
	return true, nil
}

// headSHA reads a worktree's current commit.
func headSHA(dir string) (string, error) {
	return runGit(dir, "rev-parse", "HEAD")
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
