// Package toolkit locates, validates and provisions the toolkit a project
// delegates to.
//
// Orion delegates review, secret scanning, test/build verification, PR
// authoring and PM decomposition to whichever toolkit the project configures.
// Having one is a hard dependency, not a nicety: those stages have no
// fallback, and faking them with a thinner substitute would be worse than not
// running them. Which one it is, is the project's choice.
//
// nj-agents is the shipped default, not the only one. WHAT a toolkit must
// ship comes from the stages the project configures (orion.json
// toolkit.stages), so a project delegating to its own skill repository is
// validated against the skills it actually invokes rather than against
// nj-agents' catalogue.
// Everything specific to nj-agents -- CONVENTIONS.md, install.sh -- is
// required only of nj-agents.
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
package toolkit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// RepoURL is where the toolkit is cloned from when it is absent.
const RepoURL = "https://github.com/navjyotnishant/nj-agents.git"

// Toolkit is the project's declared toolkit, in the terms this package
// resolves against.
//
// A copy of config's block rather than the block itself, because config
// imports THIS package for RepoURL and the dependency cannot run both ways.
// config.Toolkit.Spec() makes the copy, so the two stay in one place.
type Toolkit struct {
	Repo   string            // clone URL; empty means the nj-agents default
	Dir    string            // an existing clone to prefer over discovery
	Stages map[string]string // stage name -> the command that stage delegates to
}

// IsDefault reports whether this is the built-in nj-agents toolkit, which is
// the only one Orion may assume anything about. Everything a foreign toolkit
// is NOT required to ship -- CONVENTIONS.md, install.sh -- hangs off this.
func (t Toolkit) IsDefault() bool {
	r := strings.TrimSpace(t.Repo)
	return r == "" || r == RepoURL
}

// Requirement is one skill a toolkit must ship, and the stage that named it.
//
// The stage is carried so a failure points at the config line that caused it.
// "Missing skills/their-review" sends someone hunting through a toolkit; "the
// review stage names it" sends them to the one line they can change.
type Requirement struct {
	Skill string
	Stage string // "" for Orion's built-in default set, which no stage named
}

// defaultSkills are the ones Orion invokes when a project configures no
// stages of its own. Deliberately not the full catalogue: a missing skill
// Orion never calls is not Orion's problem.
var defaultSkills = []string{
	"pre-push-review",
	"review-secrets",
	"review-tests-build",
	"pr-describe",
	"pm-plan",
	"scaffold-project",
}

// RequiredSkills is what THIS project's toolkit must actually ship.
//
// Derived from the stages a project configures rather than fixed, because a
// fixed list validates a foreign toolkit against nj-agents' catalogue: a
// perfectly healthy toolkit fails doctor for six skills the project never
// invokes. What a project names is what it needs.
//
// An empty stages map is "unset", not "run nothing", so it yields the
// built-in set and doctor behaves exactly as it did before toolkits were
// configurable.
func RequiredSkills(tk Toolkit) []Requirement {
	if len(tk.Stages) == 0 {
		out := make([]Requirement, 0, len(defaultSkills))
		for _, s := range defaultSkills {
			out = append(out, Requirement{Skill: s})
		}
		return out
	}
	// Sorted so a skill two stages both name reports the same stage on every
	// machine; map order would make the message flap between runs.
	stages := make([]string, 0, len(tk.Stages))
	for k := range tk.Stages {
		stages = append(stages, k)
	}
	sort.Strings(stages)

	seen := map[string]bool{}
	var out []Requirement
	for _, stage := range stages {
		name := skillName(tk.Stages[stage])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Requirement{Skill: name, Stage: stage})
	}
	return out
}

// skillName turns a stage's command into the skills/<name>/SKILL.md lookup
// HasSkill already performs: the leading slash goes, and anything after the
// command itself is an argument, not part of the directory name.
func skillName(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimPrefix(f[0], "/")
}

// RequiredDocs are the shared contracts the skills read at runtime. Their
// absence is what a naive skills-directory check misses.
//
// Only for the default toolkit. CONVENTIONS.md is nj-agents' own contract,
// not a thing every skill repository has, and requiring it of a foreign
// toolkit fails a healthy one over a file it was never going to ship.
func RequiredDocs(tk Toolkit) []string {
	if !tk.IsDefault() {
		return nil
	}
	return []string{"CONVENTIONS.md"}
}

// TestingSkills are the ones the QA stage points its agent at when they are
// there. NOT in RequiredSkills, and that is the whole distinction: the review
// stages have no fallback, whereas QA does -- a repository's own test runner
// -- so a missing testing skill degrades the stage rather than failing it.
var TestingSkills = []string{"test-suite-author", "e2e-suite"}

// HasSkill reports whether a located toolkit actually ships a skill.
//
// Asked of the RESOLVED clone rather than of the runner's skills directory,
// for the reason this package exists: a symlink in ~/.claude says a skill was
// installed once, not that the toolkit behind it is still there.
func HasSkill(inst *Install, name string) bool {
	if inst == nil || inst.Root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(inst.Root, "skills", name, "SKILL.md"))
	return err == nil
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
	return VendorDirFor(orionHome, RepoURL)
}

// VendorDirFor is the same directory for a toolkit other than nj-agents,
// named after the repository rather than fixed.
//
// A fixed leaf was safe while there was exactly one toolkit. Once a project
// can declare its own (orion.json toolkit.repo), a fixed name means the
// second clone lands on top of the first: same path, different repository,
// no error -- Orion would then run one project's skills for another's stages
// and report success. The repo name is what distinguishes them, so the path
// carries it.
//
// The default repo still resolves to vendor/nj-agents, so no existing clone
// moves.
func VendorDirFor(orionHome, repoURL string) string {
	return filepath.Join(orionHome, "vendor", repoLeaf(repoURL))
}

// repoLeaf is the repository name out of a clone URL: the last path element,
// minus any .git suffix. Handles the https and scp-like (git@host:owner/name)
// forms alike, since both are just text up to the last separator.
//
// A URL it cannot read yields "toolkit" rather than an empty leaf, which
// would put the clone at <ORION_HOME>/vendor itself and make the vendor
// directory a git repository.
func repoLeaf(repoURL string) string {
	s := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(repoURL), "/"), ".git")
	// Backslash counts as a separator too: a repo may be a LOCAL path, and
	// on Windows that is C:\...\name. Splitting only on "/:" cut such a
	// path at its drive colon, so the "leaf" was the entire remainder of
	// the path -- which VendorDirFor then joined under vendor/ as a dozen
	// nested directories, and the clone failed (OR-342).
	if i := strings.LastIndexAny(s, `/:\`); i >= 0 {
		s = s[i+1:]
	}
	if s = strings.TrimSpace(s); s == "" || s == "." || s == ".." {
		return "toolkit"
	}
	return s
}

// Discover finds the toolkit, in order of how much the user meant it.
//
// An explicit setting wins over a detected one, and a user's own install
// wins over Orion's managed clone. Orion must never quietly prefer its own
// copy over the one the user maintains: two clones that drift apart, with
// Orion silently using the stale one, is a genuinely nasty failure.
func Discover(orionHome string, tk Toolkit) *Install {
	type candidate struct{ path, via string }
	var candidates []candidate

	if tk.Dir != "" {
		candidates = append(candidates, candidate{expand(tk.Dir), "configured"})
	}
	if env := strings.TrimSpace(os.Getenv("ORION_NJ_AGENTS_DIR")); env != "" {
		candidates = append(candidates, candidate{expand(env), "ORION_NJ_AGENTS_DIR"})
	}
	if root := fromRunnerSymlink(tk); root != "" {
		candidates = append(candidates, candidate{root, "resolved from ~/.claude/skills symlink"})
	}
	candidates = append(candidates, candidate{VendorDirFor(orionHome, tk.Repo), "Orion-managed clone"})

	var firstIncomplete *Install
	for _, c := range candidates {
		if c.path == "" {
			continue
		}
		inst := Validate(c.path, tk)
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
// The skills it probes are the CONFIGURED ones: a project delegating to its
// own toolkit has no nj-agents skill installed to resolve from, so probing
// the built-in names would never find the clone that is actually there.
func fromRunnerSymlink(tk Toolkit) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, runner := range []string{".claude", ".agents", ".codex", ".gemini", ".cursor"} {
		for _, req := range RequiredSkills(tk) {
			link := filepath.Join(home, runner, "skills", req.Skill)
			resolved, err := filepath.EvalSymlinks(link)
			if err != nil {
				continue
			}
			// <root>/skills/<name> -> up two
			root := filepath.Dir(filepath.Dir(resolved))
			if isToolkitRoot(root, tk) {
				return root
			}
		}
	}
	return ""
}

// isToolkitRoot is the same "is this a toolkit at all" question Validate
// asks, and must stay the same answer: a skills directory, plus whatever
// docs are required of this toolkit. For a foreign toolkit no doc is
// required, so the skills directory is the whole test -- demanding
// CONVENTIONS.md there would reject the clone the symlink points at.
func isToolkitRoot(dir string, tk Toolkit) bool {
	for _, d := range RequiredDocs(tk) {
		if _, err := os.Stat(filepath.Join(dir, d)); err != nil {
			return false
		}
	}
	return hasSkillsDir(dir)
}

func hasSkillsDir(root string) bool {
	st, err := os.Stat(filepath.Join(root, "skills"))
	return err == nil && st.IsDir()
}

// Validate checks a candidate root and reports precisely what is missing.
// Returns nil only when the path is not a plausible toolkit at all.
func Validate(root string, tk Toolkit) *Install {
	if root == "" {
		return nil
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil
	}

	// Nothing recognisable at all: not a toolkit, just a directory.
	//
	// Tested EXPLICITLY, on the one thing every skill repository has. This
	// used to be arithmetic -- everything required is missing, so nothing is
	// here -- which held only while the required lists were fixed at seven.
	// They now shrink with the config, and a one-skill zero-doc project would
	// make an empty directory pass that comparison and be reported healthy.
	// A false negative here costs a confusing "not installed"; a false
	// positive tells someone a directory with no skills in it is fine.
	if !hasSkillsDir(root) {
		return nil
	}

	inst := &Install{Root: root}
	for _, d := range RequiredDocs(tk) {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			inst.Missing = append(inst.Missing, d)
		}
	}
	skillsDir := filepath.Join(root, "skills")
	for _, req := range RequiredSkills(tk) {
		if _, err := os.Stat(filepath.Join(skillsDir, req.Skill, "SKILL.md")); err != nil {
			inst.Missing = append(inst.Missing, req.describe())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "install.sh")); err != nil && tk.IsDefault() {
		// Not fatal for reading skills, but it is how Orion wires a
		// workspace, so its absence is worth saying out loud.
		//
		// Only for the default toolkit, which does ship one. install.sh is
		// nj-agents' convention, not a rule every skill repository follows,
		// and warning about a file a foreign toolkit was never going to have
		// trains people to ignore the warnings that mean something.
		inst.Warnings = append(inst.Warnings, "install.sh not found; per-workspace install unavailable")
	}

	inst.Commit, inst.Dirty = gitState(root)
	return inst
}

// describe names the missing skill AND the config line that asked for it,
// so a failure points at something the reader can change.
func (r Requirement) describe() string {
	if r.Stage == "" {
		return "skills/" + r.Skill
	}
	return "skills/" + r.Skill + " (required by the " + r.Stage + " stage)"
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
// The URL comes from the project's toolkit block, and a non-default one is
// CONFIRMED before anything is fetched. Cloning the vendor default is a
// decision Orion already made on the user's behalf; cloning whatever URL a
// checked-in config names is code from an arbitrary third party landing in
// ORION_HOME, which is a different consent level and belongs to the operator.
// ask returning false leaves the machine exactly as it was.
func Clone(orionHome string, tk Toolkit, ref string, ask Confirm) (*Install, error) {
	repo := strings.TrimSpace(tk.Repo)
	if repo == "" {
		repo = RepoURL
	}
	dst := VendorDirFor(orionHome, repo)

	if inst := Validate(dst, tk); inst != nil && len(inst.Missing) == 0 {
		inst.Via = "Orion-managed clone (already present)"
		inst.Managed = true
		return inst, nil
	}
	if !tk.IsDefault() {
		if ask == nil || !ask(repo) {
			return nil, fmt.Errorf("declined: %s is not the toolkit Orion ships, and nothing was fetched.\n"+
				"  Clone it yourself if you meant to:\n    %s", repo, CloneCommand(orionHome, tk))
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("%s exists but is not a complete checkout of %s.\n"+
			"  Remove it and re-run, or point Orion at a good copy with ORION_NJ_AGENTS_DIR.", dst, repo)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git is required to fetch the toolkit")
	}

	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, repo, dst)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("cloning %s: %s", repo, strings.TrimSpace(string(out)))
	}

	inst := Validate(dst, tk)
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
// USUALLY UNNECESSARY. A global nj-agents install is visible to every
// `claude` run regardless of directory, so a workspace needs no wiring at
// all. This exists for the one case where it does: the user had no global
// install, Orion fetched its own clone, and a workspace needs to see those
// skills without Orion modifying the user's ~/.claude to get there.
//
// Reusing install.sh is the point. It knows about multiple runners, Codex's
// TOML generation, hook registration and uninstall. A reimplementation would
// drift from it and break in ways neither project could debug.
func InstallInto(inst *Install, projectDir string) (string, error) {
	if inst == nil || inst.Root == "" {
		return "", fmt.Errorf("no nj-agents installation to install from")
	}
	script := filepath.Join(inst.Root, "install.sh")
	if _, err := os.Stat(script); err != nil {
		// Say WHY rather than surfacing a stat error. install.sh is optional
		// now, so its absence is a fact about the toolkit, not a fault: this
		// one wires itself some other way, and there is nothing to run.
		return "", fmt.Errorf("the toolkit at %s ships no install.sh, so Orion has no installer to run for %s.\n"+
			"  It is still usable: skills are read from the clone directly. Wire it into\n"+
			"  the directory however that toolkit documents, or run its skills globally.",
			inst.Root, projectDir)
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

// CloneCommand is the manual equivalent of Clone. Printed when consent has
// not been given, so it must name the SAME repository Clone would have used.
func CloneCommand(orionHome string, tk Toolkit) string {
	repo := strings.TrimSpace(tk.Repo)
	if repo == "" {
		repo = RepoURL
	}
	return fmt.Sprintf("git clone %s %s", repo, VendorDirFor(orionHome, repo))
}

// Confirm asks the operator whether a toolkit URL may be fetched. Injected
// rather than prompted inline so the decision is testable and so a caller
// with nowhere to ask can pass nil, which reads as no.
type Confirm func(repoURL string) bool

// ConfirmOnStdin is the interactive answer. A non-interactive shell answers
// no: fetching third-party code because nobody was there to object is the
// wrong default, and `orion doctor --fix` runs in CI.
func ConfirmOnStdin(repoURL string) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Printf("Fetch the toolkit from %s into Orion's vendor directory? [y/N] ", repoURL)
	var ans string
	_, _ = fmt.Scanln(&ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
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

// Update pulls the latest nj-agents, and only ever into a clone Orion owns.
//
// nj-agents ships independently of Orion, so its improvements reach Orion
// only if something fetches them. Which clone that is decides everything:
//
//   - A GLOBAL install belongs to the user. It is their working repository,
//     very possibly the one they develop nj-agents in. Orion reads it and
//     never writes to it. There is no override, because there is no
//     legitimate caller for one: anyone who wants it updated can run git
//     pull themselves, and a flag that lets a tool pull someone's working
//     tree is a footgun whose only real use is to cause the accident it
//     warns about.
//   - Orion's OWN clone, fetched by `orion doctor --fix` when the user had
//     none, is Orion's to maintain. Updating it needs no ceremony.
//
// Within Orion's own clone two rules still hold: fast-forward or nothing,
// and a pinned ref is a reproducibility choice that an update must not
// quietly undo.
func Update(inst *Install, pinnedRef string) (*UpdateResult, error) {
	if inst == nil || inst.Root == "" {
		return nil, fmt.Errorf("no nj-agents installation found to update")
	}
	res := &UpdateResult{From: inst.Commit}

	if !inst.Managed {
		res.Skipped = "this is your global nj-agents (" + inst.Root + "), not Orion's.\n" +
			"  Orion reads it and does not write to it. Update it yourself:\n" +
			"    git -C " + inst.Root + " pull"
		return res, nil
	}
	if inst.Dirty {
		res.Skipped = "Orion's clone at " + inst.Root + " has local modifications.\n" +
			"  Refusing to update over them. Commit, stash, or delete the directory\n" +
			"  and re-run `orion doctor --fix` for a clean copy."
		return res, nil
	}
	if pinnedRef != "" {
		res.Skipped = "delegation.nj_agents_ref pins this to " + pinnedRef + ".\n" +
			"  Updating would undo the pin you set. Clear or change the pin first."
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
