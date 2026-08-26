// Package njagents locates, validates and provisions the nj-agents toolkit.
//
// Orion delegates review, secret scanning, test/build verification, PR
// authoring and PM decomposition to nj-agents. That is a hard dependency,
// not a nicety: those stages have no fallback, and faking them with a
// thinner substitute would be worse than not running them.
//
// Two things about the real installation shape drove this design.
//
// First, skills are installed as SYMLINKS into a runner's config directory,
// pointing back at a clone. So the presence of ~/.claude/skills/<name> says
// nothing about whether the toolkit behind it is intact. The link must be
// resolved to find the clone root.
//
// Second, the shared contract every review skill reads (CONVENTIONS.md and
// its siblings) lives at that clone ROOT, two levels above the skill. A
// check that only looks in the skills directory passes happily while the
// file the skills depend on is missing.
package njagents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoURL is where the toolkit is cloned from when it is absent.
const RepoURL = "https://github.com/navjyotnishant/nj-agents.git"

// RequiredSkills are the ones Orion actually invokes. Deliberately not the
// full catalogue: a missing skill Orion never calls is not Orion's problem.
var RequiredSkills = []string{
	"pre-push-review",
	"review-secrets",
	"review-tests-build",
	"pr-describe",
	"pm-plan",
	"scaffold-project",
}

// RequiredDocs are the shared contracts the skills read at runtime. Their
// absence is what a naive skills-directory check misses.
var RequiredDocs = []string{
	"CONVENTIONS.md",
}

// Install describes a located toolkit.
type Install struct {
	Root     string // the clone root, containing CONVENTIONS.md and install.sh
	Via      string // how it was found, for the report
	Commit   string // short SHA, so a report pins what was actually present
	Dirty    bool   // local modifications present
	Managed  bool   // true when Orion cloned it into its own vendor directory
	Missing  []string
	Warnings []string
}

func (i *Install) OK() bool { return i != nil && i.Root != "" && len(i.Missing) == 0 }

// VendorDir is where Orion keeps its own clone when the user has none.
// Deliberately under ORION_HOME rather than anywhere in the user's tree:
// a tool that scatters clones through someone's home directory is rude,
// and one it owns is one it may safely update.
func VendorDir(orionHome string) string {
	return filepath.Join(orionHome, "vendor", "nj-agents")
}

// Discover finds the toolkit, in order of how much the user meant it.
//
// An explicit setting wins over a detected one, and a user's own install
// wins over Orion's managed clone. Orion must never quietly prefer its own
// copy over the one the user maintains: two clones that drift apart, with
// Orion silently using the stale one, is a genuinely nasty failure.
func Discover(configured, orionHome string) *Install {
	type candidate struct{ path, via string }
	var candidates []candidate

	if configured != "" {
		candidates = append(candidates, candidate{expand(configured), "configured"})
	}
	if env := strings.TrimSpace(os.Getenv("ORION_NJ_AGENTS_DIR")); env != "" {
		candidates = append(candidates, candidate{expand(env), "ORION_NJ_AGENTS_DIR"})
	}
	if root := fromRunnerSymlink(); root != "" {
		candidates = append(candidates, candidate{root, "resolved from ~/.claude/skills symlink"})
	}
	candidates = append(candidates, candidate{VendorDir(orionHome), "Orion-managed clone"})

	var firstIncomplete *Install
	for _, c := range candidates {
		if c.path == "" {
			continue
		}
		inst := Validate(c.path)
		if inst == nil {
			continue
		}
		inst.Via = c.via
		inst.Managed = c.via == "Orion-managed clone"
		if len(inst.Missing) == 0 {
			return inst
		}
		// Keep the first partially-valid candidate: reporting "found but
		// incomplete at X" is far more useful than "not found".
		if firstIncomplete == nil {
			firstIncomplete = inst
		}
	}
	return firstIncomplete
}

// fromRunnerSymlink resolves an installed skill back to its clone root.
//
// Every skill is a symlink to <root>/skills/<name>, so the root is two
// levels up from the RESOLVED path. Resolving matters: a relative walk from
// the link itself lands in the runner's config directory, where none of the
// shared contracts exist.
func fromRunnerSymlink() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, runner := range []string{".claude", ".agents", ".codex", ".gemini", ".cursor"} {
		for _, skill := range RequiredSkills {
			link := filepath.Join(home, runner, "skills", skill)
			resolved, err := filepath.EvalSymlinks(link)
			if err != nil {
				continue
			}
			// <root>/skills/<name> -> up two
			root := filepath.Dir(filepath.Dir(resolved))
			if isToolkitRoot(root) {
				return root
			}
		}
	}
	return ""
}

func isToolkitRoot(dir string) bool {
	for _, d := range RequiredDocs {
		if _, err := os.Stat(filepath.Join(dir, d)); err != nil {
			return false
		}
	}
	return true
}

// Validate checks a candidate root and reports precisely what is missing.
// Returns nil only when the path is not a plausible toolkit at all.
func Validate(root string) *Install {
	if root == "" {
		return nil
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil
	}
	inst := &Install{Root: root}

	for _, d := range RequiredDocs {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			inst.Missing = append(inst.Missing, d)
		}
	}
	skillsDir := filepath.Join(root, "skills")
	if _, err := os.Stat(skillsDir); err != nil {
		inst.Missing = append(inst.Missing, "skills/")
	} else {
		for _, s := range RequiredSkills {
			if _, err := os.Stat(filepath.Join(skillsDir, s, "SKILL.md")); err != nil {
				inst.Missing = append(inst.Missing, "skills/"+s)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "install.sh")); err != nil {
		// Not fatal for reading skills, but it is how Orion wires a
		// workspace, so its absence is worth saying out loud.
		inst.Warnings = append(inst.Warnings, "install.sh not found; per-workspace install unavailable")
	}

	// Nothing recognisable at all: not a toolkit, just a directory.
	if len(inst.Missing) >= len(RequiredDocs)+len(RequiredSkills) {
		return nil
	}

	inst.Commit, inst.Dirty = gitState(root)
	return inst
}

func gitState(dir string) (string, bool) {
	sha, err := run(dir, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", false
	}
	status, _ := run(dir, "git", "status", "--porcelain")
	return strings.TrimSpace(sha), strings.TrimSpace(status) != ""
}

// Clone fetches the toolkit into Orion's vendor directory.
//
// It deliberately does NOT run the toolkit's install.sh. Cloning copies
// files; running an installer from a freshly downloaded repository executes
// third-party code and edits the user's runner configuration. Those are
// different consent levels, and collapsing them into one health-check flag
// would be wrong. InstallInto is the separate, explicit step.
func Clone(orionHome, ref string) (*Install, error) {
	dst := VendorDir(orionHome)

	if inst := Validate(dst); inst != nil && len(inst.Missing) == 0 {
		inst.Via = "Orion-managed clone (already present)"
		inst.Managed = true
		return inst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("%s exists but is not a complete nj-agents checkout.\n"+
			"  Remove it and re-run, or point Orion at a good copy with ORION_NJ_AGENTS_DIR.", dst)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git is required to fetch nj-agents")
	}

	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, RepoURL, dst)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("cloning nj-agents: %s", strings.TrimSpace(string(out)))
	}

	inst := Validate(dst)
	if inst == nil || len(inst.Missing) > 0 {
		return inst, fmt.Errorf("cloned %s but the checkout is incomplete", dst)
	}
	inst.Via = "Orion-managed clone"
	inst.Managed = true
	// Cloning a default branch pins nothing. Say so rather than implying the
	// dependency is fixed: the next clone on another machine can differ.
	inst.Warnings = append(inst.Warnings,
		"cloned at "+inst.Commit+" from the default branch; nothing is pinned. "+
			"Set delegation.nj_agents_ref to a tag to make this reproducible.")
	return inst, nil
}

// InstallInto wires the toolkit into one directory using the toolkit's OWN
// installer, rather than Orion reimplementing the symlink layout.
//
// Reusing install.sh is the point. It knows about multiple runners, Codex's
// TOML generation, hook registration and uninstall. A reimplementation would
// drift from it and break in ways neither project could debug.
//
// --project keeps this per-workspace: Orion does not modify the user's
// global ~/.claude just to run a job.
func InstallInto(inst *Install, projectDir string) (string, error) {
	if inst == nil || inst.Root == "" {
		return "", fmt.Errorf("no nj-agents installation to install from")
	}
	script := filepath.Join(inst.Root, "install.sh")
	if _, err := os.Stat(script); err != nil {
		return "", fmt.Errorf("%s not found; cannot wire nj-agents into %s", script, projectDir)
	}
	out, err := run(inst.Root, "bash", script, "--project", projectDir)
	if err != nil {
		return out, fmt.Errorf("install.sh --project %s failed: %s", projectDir, strings.TrimSpace(out))
	}
	return out, nil
}

// InstallCommand is what a user would type to do it themselves. Printed
// rather than executed when consent has not been given.
func InstallCommand(inst *Install, projectDir string) string {
	root := "<nj-agents>"
	if inst != nil && inst.Root != "" {
		root = inst.Root
	}
	if projectDir == "" {
		return fmt.Sprintf("cd %s && ./install.sh", root)
	}
	return fmt.Sprintf("cd %s && ./install.sh --project %s", root, projectDir)
}

// CloneCommand is the manual equivalent of Clone.
func CloneCommand(orionHome string) string {
	return fmt.Sprintf("git clone %s %s", RepoURL, VendorDir(orionHome))
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func expand(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// UpdateResult describes a refresh attempt.
type UpdateResult struct {
	From, To string
	Changed  bool
	Skipped  string // non-empty when Orion declined, with the reason
}

// Update pulls the latest nj-agents.
//
// nj-agents is developed independently, so its improvements only reach Orion
// if something fetches them. But refreshing carries a real hazard that the
// rules below exist to contain.
//
//   - A clone Orion did not create belongs to the user. It may hold work in
//     progress, a branch they are mid-way through, or deliberate local edits.
//     Orion refuses to touch it unless explicitly told to, because silently
//     pulling someone's working repository is how a tool destroys an
//     afternoon.
//   - A dirty tree is never updated. Fast-forward or nothing.
//   - A pinned ref means the user chose reproducibility. Updating past it
//     would quietly undo that choice, so it is refused and named.
func Update(inst *Install, pinnedRef string, force bool) (*UpdateResult, error) {
	if inst == nil || inst.Root == "" {
		return nil, fmt.Errorf("no nj-agents installation found to update")
	}
	res := &UpdateResult{From: inst.Commit}

	if !inst.Managed && !force {
		res.Skipped = "this clone is yours, not Orion's (" + inst.Root + ").\n" +
			"  Orion will not pull a repository it did not create: it may hold work in\n" +
			"  progress. Update it yourself with `git -C " + inst.Root + " pull`,\n" +
			"  or pass --force to let Orion fast-forward it."
		return res, nil
	}
	if inst.Dirty && !force {
		res.Skipped = "the working tree at " + inst.Root + " has local modifications.\n" +
			"  Refusing to update over them. Commit or stash first."
		return res, nil
	}
	if pinnedRef != "" && !force {
		res.Skipped = "delegation.nj_agents_ref pins this to " + pinnedRef + ".\n" +
			"  Updating would undo the pin you set. Change or clear the pin, or use --force."
		return res, nil
	}

	// Fetch then fast-forward only. A merge or rebase here could produce
	// conflicts inside a dependency, which is not a state a user should ever
	// have to resolve on Orion's behalf.
	if out, err := run(inst.Root, "git", "fetch", "--tags", "origin"); err != nil {
		return res, fmt.Errorf("fetching nj-agents: %s", strings.TrimSpace(out))
	}
	out, err := run(inst.Root, "git", "merge", "--ff-only", "@{u}")
	if err != nil {
		return res, fmt.Errorf("could not fast-forward %s: %s\n"+
			"  The local branch has diverged from origin. Resolve it there; Orion will\n"+
			"  not rebase or merge inside a dependency.", inst.Root, strings.TrimSpace(out))
	}

	res.To, _ = gitState(inst.Root)
	res.Changed = res.To != res.From
	return res, nil
}

// Refreshed reports how stale the checkout is, so a caller can nudge without
// forcing. Returns false when staleness cannot be determined offline, which
// is not an error: being unable to tell is not the same as being stale.
func Refreshed(inst *Install) (behind int, known bool) {
	if inst == nil || inst.Root == "" {
		return 0, false
	}
	// Purely local: counts against the last fetch, so it never blocks on the
	// network during a routine doctor run.
	out, err := run(inst.Root, "git", "rev-list", "--count", "HEAD..@{u}")
	if err != nil {
		return 0, false
	}
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}
