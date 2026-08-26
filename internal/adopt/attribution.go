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
		b, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", h))
		if err == nil && strings.Contains(string(b), "dun") {
			s.Instrumented = true
			break
		}
	}
	return s
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
