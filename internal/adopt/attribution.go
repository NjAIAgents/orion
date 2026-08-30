package adopt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Attribution instruments a repository with whodunit (`dun`), which stamps
// each commit with an AI-Attribution trailer describing what evidence exists
// about how the change was written.
//
// Why Orion cares. The agent author alias in vcs.agent_author_name is a
// coarse marker: it says a commit came from an Orion run. It cannot say which
// agent, which model, or how much of the diff the agent actually produced,
// because it is set by the supervisor before the work happens. dun answers
// that from the session transcripts afterwards, which is a different and
// stronger claim. The two are complementary rather than redundant.
//
// The sharpest failure this code exists to prevent is documented upstream:
// the git hook resolves the binary by NAME at commit time, so a `dun` that is
// not on PATH falls back to wherever it was when `dun init` ran. When that
// path later disappears, every commit is stamped `undetermined` silently --
// and downstream `undetermined` reads as "no AI was used" rather than "the
// tool went missing". A wrong answer that looks like a finding.
//
// KNOWN UPSTREAM GAP, measured 2026-08-29 against dun v0.4.0 (OR-193).
// Instrumenting the sandbox clone is necessary but not yet sufficient: dun
// still cannot find a transcript for work done in an Orion sandbox, so those
// commits land `unmatched` rather than `intersected`.
//
// whodunit locates transcripts at ~/.claude/projects/<slug of cwd>, and its
// SlugForCwd maps "/" and "\" to "-" but leaves "." alone. Claude Code maps
// "." to "-" as well. Every Orion sandbox lives under ~/.orion, so the two
// disagree on the first dotted component and never meet:
//
//	Claude Code writes   ...projects/-Users-me--orion-projects-X-worktrees-Y
//	whodunit looks in    ...projects/-Users-me-.orion-projects-X-worktrees-Y
//
// The user's own checkout has no dot in its path, which is why attribution
// works there and only there. The fix belongs in whodunit's adapter, not
// here: Orion could shim it by reproducing that private slug under a
// WHODUNIT_CLAUDE_CODE_PATH override, but the shim would silently break the
// day upstream corrects the encoding, which trades one invisible wrong answer
// for another.

// DunStatus is what we know about attribution tooling for one repository.
type DunStatus struct {
	Path         string // resolved binary, empty when not installed
	Version      string
	OnPath       bool // found via PATH rather than a fixed location
	Instrumented bool // this repo already has dun's hooks
}

// DunLook reports the state of attribution tooling for a repository.
func DunLook(dir string) DunStatus {
	var s DunStatus
	if p, err := exec.LookPath("dun"); err == nil {
		s.Path, s.OnPath = p, true
		if out, err := exec.Command(p, "version").Output(); err == nil {
			s.Version = strings.TrimSpace(string(out))
		}
	}
	// Read the hook rather than asking dun, so this stays honest even when
	// the binary has gone missing -- which is exactly the case that matters.
	for _, h := range []string{"prepare-commit-msg", "commit-msg"} {
		b, err := os.ReadFile(filepath.Join(HooksDir(dir), h))
		if err == nil && strings.Contains(string(b), "dun") {
			s.Instrumented = true
			break
		}
	}
	return s
}

// HooksDir resolves the directory git will actually run this repository's
// hooks from.
//
// Not <dir>/.git/hooks. Inside a worktree `.git` is a FILE pointing at
// <clone>/.git/worktrees/<name>, and git resolves hooks from the COMMON
// dir -- so reading <dir>/.git/hooks is a read of a path that is not a
// directory, and DunLook reported "not instrumented" from inside every job
// worktree regardless of the truth. A worktree is where every agent commit
// is made, so that was the one place the answer had to be right.
//
// core.hooksPath wins when set, because it wins for git too: a repo using
// husky or lefthook keeps its hooks somewhere else entirely, and reporting
// on the common dir there would be confidently wrong in the other direction.
func HooksDir(dir string) string {
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	if p := gitOut(dir, "config", "--get", "core.hooksPath"); p != "" {
		return abs(p)
	}
	// --git-common-dir is the shared .git of the clone whether asked from the
	// clone or from one of its worktrees; --git-dir is the per-worktree one,
	// which has no hooks directory at all.
	if c := gitOut(dir, "rev-parse", "--git-common-dir"); c != "" {
		return filepath.Join(abs(c), "hooks")
	}
	return filepath.Join(dir, ".git", "hooks")
}

func gitOut(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DunInstallCommand returns the install command for this platform, and
// whether one is available at all.
func DunInstallCommand() ([]string, string, bool) {
	switch runtime.GOOS {
	case "windows":
		if p, err := exec.LookPath("scoop"); err == nil {
			return []string{p, "install", "dun"}, "scoop", true
		}
	default:
		if p, err := exec.LookPath("brew"); err == nil {
			return []string{p, "install", "navjyotnishant/tap/dun"}, "brew", true
		}
	}
	// `go install` is the last resort deliberately. It puts the binary in
	// GOBIN, which is frequently not on PATH, and a dun that is not on PATH
	// as `dun` is the documented cause of silently undetermined commits.
	if p, err := exec.LookPath("go"); err == nil {
		return []string{p, "install", "github.com/navjyotnishant/whodunit/cmd/dun@latest"}, "go", true
	}
	return nil, "", false
}

// EnsureDun installs dun when missing and instruments the repository.
//
// confirm is asked before anything is installed or any hook is written;
// passing nil means proceed. It returns lines to print and any warning.
func EnsureDun(dir string, autoInstall bool, confirm func(prompt string) bool) (lines []string, warnings []string) {
	st := DunLook(dir)

	if st.Path == "" {
		if !autoInstall {
			return nil, []string{"dun is not installed, so commits carry no attribution trailer.\n" +
				"         Install it:  brew install navjyotnishant/tap/dun"}
		}
		cmd, via, ok := DunInstallCommand()
		if !ok {
			return nil, []string{"dun is not installed and no package manager was found.\n" +
				"         See https://navjyotnishant.github.io/whodunit/getting-started/install"}
		}
		if confirm != nil && !confirm(fmt.Sprintf("Install dun via %s (%s)?", via, strings.Join(cmd[1:], " "))) {
			return nil, []string{"skipped installing dun; commits will carry no attribution trailer"}
		}
		out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		if err != nil {
			return nil, []string{fmt.Sprintf("could not install dun via %s: %v\n         %s", via, err, lastLine(string(out)))}
		}
		st = DunLook(dir)
		if st.Path == "" {
			return nil, []string{"dun installed but is not on PATH as `dun`.\n" +
				"         The git hook resolves it by name at commit time, so until it is on\n" +
				"         PATH every commit is stamped `undetermined` -- which downstream reads\n" +
				"         as \"no AI was used\" rather than \"the tool was missing\"."}
		}
		lines = append(lines, "installed dun "+st.Version+" ("+via+")")
	}

	if !st.OnPath {
		warnings = append(warnings,
			"dun is not on PATH as `dun`; the hook resolves it by name at commit time,\n"+
				"         so commits will silently record `undetermined` if that path moves")
	}

	if st.Instrumented {
		lines = append(lines, "bound    dun (repository already instrumented)")
		return lines, warnings
	}

	if confirm != nil && !confirm("Instrument this repository with dun (adds 3 git hooks)?") {
		return lines, append(warnings, "skipped dun init; commits will carry no attribution trailer")
	}

	// dun chains to hooks that already exist rather than replacing them, so
	// this is safe alongside husky, pre-commit or lefthook.
	out, err := exec.Command(st.Path, "init", "--repo", dir).CombinedOutput()
	if err != nil {
		return lines, append(warnings, fmt.Sprintf("dun init failed: %v\n         %s", err, lastLine(string(out))))
	}
	lines = append(lines, "installed dun hooks (prepare-commit-msg, commit-msg, pre-push)")
	// Show dun's own report rather than swallowing it. It lists which agents
	// it can see for this repository, and an agent it cannot find is the
	// difference between a commit stamped with real evidence and one stamped
	// `undetermined` -- which downstream reads as "no AI was used". Hiding
	// that turns a fixable configuration gap into a silent wrong answer.
	lines = append(lines, indent(strings.TrimRight(string(out), "\n"))...)
	return lines, warnings
}

// EnsureSandboxDun instruments the SANDBOX CLONE, which is a different
// repository from the user's checkout and was never being instrumented.
//
// This is the measured cause of OR-193. `orion init` runs EnsureDun against
// the path the user typed; the sandbox clone under ~/.orion/projects/<id>/repo
// is a separate `git clone` with its own .git, and nothing ever ran `dun init`
// there. Its hooks directory held only git's .sample files, so every commit an
// agent made in a job worktree carried NO AI-Attribution trailer at all --
// which downstream is not even "unmatched", it is silence.
//
// Instrumenting the clone covers every worktree: a worktree has no hooks of
// its own and resolves them from the clone's common dir, so one init here is
// the fix for N concurrent jobs (OR-184) rather than one per job.
//
// Called per job rather than only at adoption, for the same reason
// writeSettings is: a sandbox provisioned before this release otherwise keeps
// the state that release gave it, and the run that would benefit is the one
// that cannot know it should. `dun init` is idempotent and this short-circuits
// on the already-instrumented case, so the steady-state cost is one hook read.
//
// Best-effort by contract: returns an error to be REPORTED, never one that
// should abandon a job. Losing attribution is bad; losing the work is worse.
func EnsureSandboxDun(clone string) error {
	st := DunLook(clone)
	if st.Instrumented {
		return nil
	}
	if st.Path == "" {
		return fmt.Errorf("dun is not on PATH, so commits in this sandbox carry no attribution trailer")
	}
	if out, err := exec.Command(st.Path, "init", "--repo", clone).CombinedOutput(); err != nil {
		return fmt.Errorf("dun init on the sandbox clone %s: %w\n%s", clone, err, lastLine(string(out)))
	}
	// Verify rather than trust the exit code. `dun init` succeeding while the
	// hooks are not where git will look for them is precisely the silent
	// wrong answer this whole path exists to prevent.
	if !DunLook(clone).Instrumented {
		return fmt.Errorf("dun init reported success but %s has no dun hooks", HooksDir(clone))
	}
	return nil
}

// DunVerify returns dun's own verdict on a repository.
//
// dun verify is the authoritative view and it names what to fix; DunLook only
// answers "are the hooks there". Surfacing dun's answer rather than
// paraphrasing it is the point -- a paraphrase is another thing that can drift
// from the truth without anyone noticing.
//
// A non-zero exit is a finding, not a failure to report: dun exits non-zero
// when it found problems, and those are exactly the lines worth printing.
func DunVerify(dir string) (out string, ok bool, err error) {
	st := DunLook(dir)
	if st.Path == "" {
		return "", false, fmt.Errorf("dun is not installed")
	}
	b, runErr := exec.Command(st.Path, "verify", "--repo", dir).CombinedOutput()
	return strings.TrimRight(string(b), "\n"), runErr == nil, nil
}

// DunReplay retries determinations that failed against a journal that may
// have learned more since.
//
// Git history is not rewritten and the original trailer stands; the replay
// log records the corrected outcome. That is the honest way to recover a
// backlog: the commit still says what was knowable when it was made, and the
// record says what is knowable now.
func DunReplay(dir string) (string, error) {
	st := DunLook(dir)
	if st.Path == "" {
		return "", fmt.Errorf("dun is not installed")
	}
	cmd := exec.Command(st.Path, "replay", "--apply")
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	out := strings.TrimRight(string(b), "\n")
	if err != nil {
		return out, fmt.Errorf("dun replay --apply: %w", err)
	}
	return out, nil
}

// indent prefixes captured sub-command output so it is visibly not Orion's
// own, and drops blank lines that would otherwise break up the report.
func indent(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, "  dun | "+l)
	}
	return out
}

func lastLine(s string) string {
	f := strings.Split(strings.TrimSpace(s), "\n")
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}
