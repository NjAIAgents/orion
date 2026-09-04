// Package doctor is the precheck.
//
// Orion supervises tools it does not own: the Claude CLI, git, gh, a
// sandbox provided by the OS. Every one of those can be missing,
// unauthenticated or misconfigured, and each failure looks different at
// the point of use. Diagnosing them once, up front, in plain language is
// worth more than a stack trace forty minutes into a run.
//
// Checks are graded: FAIL blocks, WARN degrades, OK passes. The exit code
// is non-zero only for FAIL, so `orion doctor` is usable in CI as a gate.
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/workspace"
)

type grade int

const (
	ok grade = iota
	warn
	fail
)

func (g grade) label() string {
	switch g {
	case ok:
		return "OK  "
	case warn:
		return "WARN"
	default:
		return "FAIL"
	}
}

type check struct {
	name   string
	grade  grade
	detail string
	fix    string
}

// Run executes every check and returns a process exit code.
//
// autoFix permits the checks that CAN repair themselves to do so. It is
// opt-in because repair here means network access and writing to disk, which
// is not something a health check should do as a side effect of being run.
func Run(w io.Writer, path string, autoFix bool) int {
	checks := []check{
		checkClaude(),
		checkClaudeAuth(),
		checkGit(),
		checkGH(),
		checkGHScopes(),
		checkNJAgents(config.Load(rootOr(path)).Toolkit, autoFix),
		checkSandbox(),
		checkHome(),
		checkDisk(),
		checkProject(path),
		checkBranchModel(rootOr(path)),
		checkHooks(rootOr(path)),
		checkAttribution(rootOr(path), autoFix),
		checkJira(trackerRequired(rootOr(path))),
		checkSlack(config.Load(rootOr(path)).Slack.Enabled),
		checkSlackAudience(workspace.FindBySource(rootOr(path))),
	}

	fmt.Fprintf(w, "orion doctor  (%s/%s)\n\n", runtime.GOOS, runtime.GOARCH)
	failed := 0
	warned := 0
	for _, c := range checks {
		fmt.Fprintf(w, "  [%s] %-22s %s\n", c.grade.label(), c.name, c.detail)
		if c.fix != "" && c.grade != ok {
			for _, line := range strings.Split(c.fix, "\n") {
				fmt.Fprintf(w, "         %s\n", line)
			}
		}
		switch c.grade {
		case fail:
			failed++
		case warn:
			warned++
		}
	}

	fmt.Fprintln(w)

	// Cache the verdict so a run does not re-probe a remote service on every
	// invocation, while keeping the TTL short enough that an expired token
	// is caught within half a day rather than at the worst moment.
	results := map[string]string{}
	for _, c := range checks {
		results[c.name] = c.grade.label() + " " + c.detail
	}
	SaveCache(configHash(path), results)

	switch {
	case failed > 0:
		fmt.Fprintf(w, "%d blocking problem(s). Orion cannot run until these are fixed.\n", failed)
		return 1
	case warned > 0:
		fmt.Fprintf(w, "Ready, with %d degraded capability(ies).\n", warned)
		return 0
	default:
		fmt.Fprintln(w, "Ready.")
		return 0
	}
}

// trackerRequired reports whether this project actually asks for a tracker.
// Only then can a Jira problem be a blocking one.
func trackerRequired(root string) bool {
	return config.Load(root).Tracker.Enabled
}

// rootOr resolves the project root, falling back to the given path so a
// config lookup outside a project does not fail the whole run.
func rootOr(path string) string {
	if root, err := config.FindRoot(path); err == nil {
		return root
	}
	return path
}

// configHash fingerprints the config so a cached capability verdict is
// invalidated when the thing it was about changes.
func configHash(path string) string {
	root, err := config.FindRoot(path)
	if err != nil {
		root = path
	}
	b, _ := os.ReadFile(filepath.Join(root, "orion.json"))
	seed := string(b) + os.Getenv("ORION_JIRA_URL") + os.Getenv("ORION_JIRA_EMAIL")
	var h uint64 = 14695981039346656037
	for i := 0; i < len(seed); i++ {
		h ^= uint64(seed[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}

func checkClaude() check {
	p, err := exec.LookPath("claude")
	if err != nil {
		return check{"claude CLI", fail, "not found on PATH",
			"Orion supervises the Claude CLI; it does not embed a model client.\nInstall it, then re-run orion doctor."}
	}
	out, err := exec.Command(p, "--version").Output()
	if err != nil {
		return check{"claude CLI", warn, "found at " + p + " but --version failed",
			"The binary may be a broken install or a shim."}
	}
	return check{"claude CLI", ok, strings.TrimSpace(string(out)), ""}
}

// checkClaudeAuth asks the question checkClaude cannot: is the CLI SIGNED IN?
//
// A present-but-logged-out CLI passes every check above -- it is on PATH and
// --version answers -- and then fails every run within five seconds, reported
// as an exit code that names neither the cause nor the fix (OR-212). The same
// standard OR-11 set for Jira: name which credential failed, not "auth failed".
//
// `claude auth status --json` is free and spends nothing, unlike a probe
// prompt, which is what makes this affordable on every doctor run.
func checkClaudeAuth() check {
	p, err := exec.LookPath("claude")
	if err != nil {
		// checkClaude already FAILs on this; a second blocking line about the
		// same missing binary would just be noise.
		return check{"claude auth", warn, "claude not on PATH", ""}
	}
	out, err := exec.Command(p, "auth", "status", "--json").Output()
	return claudeAuthVerdict(out, err)
}

// claudeAuthVerdict grades the CLI's own answer. Split out because the grading
// is the part worth testing and exec is the part that is not.
func claudeAuthVerdict(out []byte, err error) check {
	// A CLI too old to answer, or one that errored for its own reasons, is a
	// check that could not be made -- not a logged-out CLI. Degrade rather than
	// block: reporting FAIL here would stop a working machine over a question
	// nobody could answer.
	var s struct {
		LoggedIn   *bool  `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
		Email      string `json:"email"`
	}
	if err != nil || json.Unmarshal(out, &s) != nil || s.LoggedIn == nil {
		return check{"claude auth", warn, "could not determine whether the CLI is signed in",
			"`claude auth status --json` did not answer. Orion cannot tell a signed-in\n" +
				"CLI from an expired one here; a run will say so within five seconds."}
	}
	if !*s.LoggedIn {
		return check{"claude auth", fail, "the CLI is not signed in",
			"Every run will fail immediately: the CLI is installed and has no usable\n" +
				"login, which the binary check above cannot see.\n" +
				"Run: claude, sign in, then re-run orion doctor."}
	}
	detail := "signed in"
	if s.Email != "" {
		detail += " as " + s.Email
	}
	if s.AuthMethod != "" {
		detail += " (" + s.AuthMethod + ")"
	}
	return check{"claude auth", ok, detail, ""}
}

func checkGit() check {
	p, err := exec.LookPath("git")
	if err != nil {
		return check{"git", fail, "not found on PATH",
			"The artifact chain is committed to git. Nothing works without it."}
	}
	out, _ := exec.Command(p, "--version").Output()
	// An unset identity produces commits git will make but no reviewer can
	// attribute, which defeats the audit trail the whole design rests on.
	name, _ := exec.Command(p, "config", "user.name").Output()
	email, _ := exec.Command(p, "config", "user.email").Output()
	if strings.TrimSpace(string(name)) == "" || strings.TrimSpace(string(email)) == "" {
		return check{"git", warn, strings.TrimSpace(string(out)) + " (identity not set)",
			"Commits would be unattributable, which breaks the audit trail.\ngit config --global user.name \"...\" && git config --global user.email \"...\""}
	}
	return check{"git", ok, strings.TrimSpace(string(out)), ""}
}

func checkGH() check {
	p, err := exec.LookPath("gh")
	if err != nil {
		return check{"gh CLI", warn, "not found on PATH",
			"Orion opens pull requests through gh. Without it, stages 1 to 4 work\nand the PR must be opened by hand."}
	}
	if err := exec.Command(p, "auth", "status").Run(); err != nil {
		return check{"gh CLI", warn, "installed but not authenticated",
			"Run: gh auth login"}
	}
	return check{"gh CLI", ok, "authenticated", ""}
}

// checkSandbox reports whether OS-level isolation is available. This is a
// capability probe, not a guarantee: the authority on whether a sandbox
// actually initialized is Claude Code at startup, with failIfUnavailable
// set. What this catches is the obvious case of a platform that cannot
// sandbox at all, before a workspace is provisioned in false confidence.
func checkSandbox() check {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat("/usr/bin/sandbox-exec"); err == nil {
			return check{"os sandbox", ok, "seatbelt available", ""}
		}
		return check{"os sandbox", warn, "sandbox-exec not found",
			"Workspaces will refuse to start with failIfUnavailable set.\nUse --container, or relax the sandbox in the generated settings."}
	case "linux":
		for _, p := range []string{"/usr/bin/bwrap", "/usr/bin/unshare"} {
			if _, err := os.Stat(p); err == nil {
				return check{"os sandbox", ok, filepath.Base(p) + " available", ""}
			}
		}
		return check{"os sandbox", warn, "no bwrap or unshare found",
			"Install bubblewrap for OS-level isolation, or use --container."}
	default:
		return check{"os sandbox", warn, runtime.GOOS + " has no supported OS sandbox",
			"Use --container mode on this platform."}
	}
}

func checkHome() check {
	h, _ := workspace.EnsureHome()
	// Same mode as the rest of the tree. One inconsistent directory is how
	// someone later "tidies" the parent and reopens everything beneath it.
	if err := os.MkdirAll(filepath.Join(h, "projects"), workspace.HomeDirMode); err != nil {
		return check{"orion home", fail, h + " is not writable",
			"Set ORION_HOME to a writable location."}
	}
	return check{"orion home", ok, h, ""}
}

// checkBranchModel asks the question the branch model was never asked:
// is there an integration branch at all, or does agent work merge straight
// into the release branch?
//
// FAIL rather than WARN. Every merge Orion performs under a collapsed model
// is a release nobody authorised, and it is reported as an ordinary merge,
// so nothing downstream will raise it either.
func checkBranchModel(root string) check {
	cfg := config.Load(root)
	if err := cfg.Validate(); err != nil {
		return check{"branch model", fail,
			fmt.Sprintf("work_branch and default_branch are both %q", cfg.VCS.WorkBranch),
			err.Error()}
	}
	if waiver := cfg.ReleaseBranchWaiver(); waiver != "" {
		return check{"branch model", warn,
			fmt.Sprintf("single-branch model: %q", cfg.VCS.WorkBranch), waiver}
	}
	return check{"branch model", ok,
		fmt.Sprintf("work %s -> release %s", cfg.VCS.WorkBranch, cfg.VCS.DefaultBranch), ""}
}

func checkProject(path string) check {
	root, err := config.FindRoot(path)
	if err != nil {
		return check{"project config", warn, "no orion.json or .git found from " + path,
			"Fine outside a project. Inside one, run orion init or copy templates/orion.json."}
	}
	cfgPath := filepath.Join(root, "orion.json")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return check{"project config", warn, "no orion.json at " + root + " (defaults in force)",
			"Copy templates/orion.json to make the limits explicit and reviewable."}
	}
	var probe map[string]any
	if err := json.Unmarshal(b, &probe); err != nil {
		// This one is FAIL rather than WARN on purpose. A malformed config
		// silently falls back to defaults, so the user believes limits are
		// in force that are not the ones they wrote.
		return check{"project config", fail, "orion.json is not valid JSON",
			err.Error() + "\nDefaults would silently apply instead of your limits. Fix the file."}
	}
	cfg := config.Load(root)
	return check{"project config", ok,
		fmt.Sprintf("%s (max_tool_calls=%d, plan gate=%v)",
			cfgPath, cfg.Limits.MaxToolCalls, cfg.Gates.RequirePlanBeforeEdit), ""}
}
