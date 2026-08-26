// Package adopt wires Orion into an existing repository.
//
// It replaces a four-step manual recipe (copy a config, make directories,
// hand-edit .claude/settings.json, run doctor) with one idempotent command.
// The hand-edited step was the dangerous one: settings.json usually already
// contains a team's own hooks, permissions and MCP servers, and a copy-paste
// that replaces the file destroys them silently.
//
// Everything here is therefore additive and re-runnable. Adopting twice
// changes nothing the second time, and nothing Orion did not put there is
// ever removed.
package adopt

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StableBinaryPath returns the path to write into hook commands, plus any
// warnings the caller should print.
//
// The obvious implementation, EvalSymlinks(os.Executable()), is actively
// wrong for a package-managed install. Homebrew puts the real binary at
// Cellar/orion/<version>/bin/orion and exposes it as a stable symlink;
// resolving the symlink pins the hooks to ONE version's directory, which
// `brew cleanup` deletes on the next upgrade. The hooks then point at a file
// that no longer exists.
//
// That failure is silent in the worst way: a hook that cannot execute does
// not announce itself as a missing guardrail, so the breaker, the shield and
// the push gate all read as enabled in orion.json while none of them run.
//
// So prefer a PATH entry that resolves to the SAME file. Package managers
// keep that name stable across upgrades. Verified by inode rather than by
// string, because a different `orion` earlier in PATH must not be adopted.
func StableBinaryPath() (string, []string) {
	var warns []string

	exe, err := os.Executable()
	if err != nil {
		// Fall back to the bare name and say so. It works whenever PATH is
		// set, and PATH differing between a shell and a GUI launch is the
		// usual cause of "hooks silently stopped running".
		return "orion", append(warns,
			"could not determine Orion's own path, so hooks use the bare name `orion`;\n"+
				"         they will fail in any session whose PATH lacks it")
	}
	real := exe
	if r, symErr := filepath.EvalSymlinks(exe); symErr == nil {
		real = r
	}

	if p, lookErr := exec.LookPath("orion"); lookErr == nil {
		pr := p
		if r, symErr := filepath.EvalSymlinks(p); symErr == nil {
			pr = r
		}
		if sameFile(pr, real) {
			return p, warns
		}
		warns = append(warns, fmt.Sprintf(
			"`orion` on PATH (%s) is a different binary from the one running (%s);\n"+
				"         hooks pin the running one so this repo is not silently rewired", p, real))
	}

	if versionedPath(real) {
		warns = append(warns, fmt.Sprintf(
			"hooks pin a version-specific path:\n           %s\n"+
				"         an upgrade that removes this directory will disable every gate\n"+
				"         WITHOUT reporting it. Re-run `orion init --force` after upgrading,\n"+
				"         or put a stable `orion` on PATH first.", real))
	}
	return real, warns
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// versionedPath spots an install layout whose directory name changes on every
// upgrade: Homebrew's Cellar, and the common /opt/<tool>/<version>/ shape.
func versionedPath(p string) bool {
	if strings.Contains(p, "/Cellar/") {
		return true
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		s := strings.TrimPrefix(seg, "v")
		if s == "" || strings.Count(s, ".") < 2 {
			continue
		}
		if strings.IndexFunc(s, func(r rune) bool {
			return (r < '0' || r > '9') && r != '.'
		}) < 0 {
			return true
		}
	}
	return false
}

// Result records what changed, so the command can report honestly rather
// than claiming to have done work it skipped.
type Result struct {
	Created  []string
	Updated  []string
	Skipped  []string
	Warnings []string
	Backup   string
}

// Options controls adoption.
type Options struct {
	Dir string
	// Binary is the path written into hook commands. Pinning the absolute
	// path keeps a repo working when PATH differs between a shell and a GUI
	// launch, which is the usual cause of "hooks silently stopped running".
	Binary string
	// PlanGate sets require_plan_before_edit. Off is the honest default for
	// an existing repo: a team that makes small changes without writing a
	// plan will otherwise hit a wall on their first edit and disable Orion
	// entirely rather than adjust one setting.
	// BinaryWarnings are carried from resolving Binary so they surface in the
	// same report as everything else, rather than being printed separately
	// and read as unrelated noise.
	BinaryWarnings []string
	PlanGate       bool
	Force          bool
}

// hookSpec is one hook Orion installs, keyed so a re-run can recognise its
// own entry rather than adding a duplicate.
type hookSpec struct {
	Event   string
	Matcher string
	Name    string
}

func specs() []hookSpec {
	return []hookSpec{
		{"SessionStart", "", "session-start"},
		{"PreToolUse", "Bash", "gate"},
		{"PreToolUse", "Edit|Write|MultiEdit|NotebookEdit", "shield"},
		{"PreToolUse", "*", "breaker"},
		// The breaker is on BOTH Pre and Post deliberately: PostToolUse
		// counts what happened, PreToolUse refuses the next call. Installing
		// one without the other yields a breaker that reports and never
		// stops.
		{"PostToolUse", "*", "breaker"},
	}
}

// Run adopts the repository.
func Run(opts Options) (*Result, error) {
	res := &Result{}
	res.Warnings = append(res.Warnings, opts.BinaryWarnings...)
	if opts.Binary == "" {
		opts.Binary = "orion"
	}
	// A hook command that does not resolve is the worst kind of broken: the
	// gates read as enabled in orion.json and none of them run, because a
	// hook that cannot execute does not report itself as a missing guardrail.
	if strings.ContainsRune(opts.Binary, os.PathSeparator) {
		if _, err := os.Stat(opts.Binary); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"the hook binary is not readable at %s (%v);\n"+
					"         every gate will silently do nothing until this resolves", opts.Binary, err))
		}
	}
	if fi, err := os.Stat(opts.Dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", opts.Dir)
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, ".git")); err != nil {
		// Not fatal. Orion's artifact chain is committed, so a non-repo is
		// a degraded setup rather than an impossible one, and saying so
		// beats refusing.
		res.Warnings = append(res.Warnings,
			"no .git here: the artifact chain is meant to be committed, so init a repo when you can")
	}

	for _, d := range []string{"docs/intent", "specs", "plans", "evals"} {
		p := filepath.Join(opts.Dir, d)
		if _, err := os.Stat(p); err == nil {
			res.Skipped = append(res.Skipped, d+"/")
			continue
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return res, fmt.Errorf("creating %s: %w", d, err)
		}
		// Keep the shape visible in git from the first commit. Not fatal if it
		// fails, but say so: a silently absent .gitkeep means the directory
		// never reaches the remote, and the artifact chain is meant to be
		// committed.
		if err := os.WriteFile(filepath.Join(p, ".gitkeep"), nil, 0o644); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"could not write %s/.gitkeep (%v); git will not track the empty directory", d, err))
		}
		res.Created = append(res.Created, d+"/")
	}

	if err := writeConfig(opts, res); err != nil {
		return res, err
	}
	if err := mergeSettings(opts, res); err != nil {
		return res, err
	}
	return res, nil
}

func writeConfig(opts Options, res *Result) error {
	p := filepath.Join(opts.Dir, "orion.json")
	if _, err := os.Stat(p); err == nil && !opts.Force {
		res.Skipped = append(res.Skipped, "orion.json (exists; --force to overwrite)")
		return nil
	}
	body := fmt.Sprintf(defaultConfig, opts.PlanGate)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		return fmt.Errorf("writing orion.json: %w", err)
	}
	// Fail loudly rather than leaving a config that silently falls back to
	// defaults: a malformed file means the limits you think are in force are
	// not the ones you wrote.
	var probe map[string]any
	if json.Unmarshal([]byte(body), &probe) != nil {
		return fmt.Errorf("generated orion.json is not valid JSON; this is a bug")
	}
	res.Created = append(res.Created, "orion.json")
	return nil
}

// mergeSettings adds Orion's hooks to .claude/settings.json without
// disturbing anything already there.
//
// This is the whole reason `orion init` exists. The file routinely holds a
// team's own hooks, permissions and MCP servers; the manual recipe invited
// someone to paste over it and lose all of that. Existing entries are
// preserved, Orion's own entries are recognised on a re-run rather than
// duplicated, and the original is backed up before any write.
func mergeSettings(opts Options, res *Result) error {
	dir := filepath.Join(opts.Dir, ".claude")
	p := filepath.Join(dir, "settings.json")

	root := map[string]any{}
	b, readErr := os.ReadFile(p)
	switch {
	case readErr != nil && !os.IsNotExist(readErr):
		// Only "the file is not there" may fall through to creating one.
		// Any other error -- a permission denial, an I/O fault, a directory
		// where a file should be -- means a file MAY exist that we cannot
		// read, and continuing would write a fresh settings.json over a
		// team's hooks, permissions and MCP servers. Refusing to guess is
		// the entire purpose of this function.
		return fmt.Errorf("cannot read %s: %w\n"+
			"  Refusing to continue: a file may exist here that would be overwritten.", p, readErr)
	case readErr == nil:
		if json.Unmarshal(b, &root) != nil {
			// Refuse rather than overwrite. A file we cannot parse may still
			// be precious, and replacing it would be the exact loss this
			// function exists to prevent.
			return fmt.Errorf("%s exists but is not valid JSON.\n"+
				"  Refusing to touch it: it may hold hooks or permissions that would be lost.\n"+
				"  Fix the JSON, then re-run.", p)
		}
		stamp := time.Now().Format("20060102-150405")
		backup := p + ".orion-" + stamp + ".bak"
		if err := os.WriteFile(backup, b, 0o644); err != nil {
			return fmt.Errorf("could not back up %s: %w", p, err)
		}
		res.Backup = backup
	default:
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	// Repoint any Orion hook that names a DIFFERENT orion binary before
	// considering what to add.
	//
	// Without this, a re-run after an upgrade does not repair anything: the
	// command string no longer matches, so the stale entry is left alone and
	// a second one is appended beside it. You end up with every hook running
	// twice, one of them against a path that no longer exists, and the
	// re-run that was supposed to fix the repo has made it worse.
	repaired := retargetOrionHooks(hooks, opts.Binary)

	added := 0
	for _, s := range specs() {
		command := opts.Binary + " hook " + s.Name
		list, _ := hooks[s.Event].([]any)

		if hasCommand(list, command, s.Matcher) {
			continue
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": command}},
		}
		if s.Matcher != "" {
			entry["matcher"] = s.Matcher
		}
		hooks[s.Event] = append(list, entry)
		added++
	}
	root["hooks"] = hooks

	if added == 0 && repaired == 0 {
		res.Skipped = append(res.Skipped, ".claude/settings.json (hooks already wired)")
		// Nothing changed, so the backup is noise. Remove it rather than
		// littering a .bak per invocation.
		if res.Backup != "" {
			_ = os.Remove(res.Backup)
			res.Backup = ""
		}
		return nil
	}

	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
		return err
	}
	switch {
	case added > 0 && repaired > 0:
		res.Updated = append(res.Updated, fmt.Sprintf(
			".claude/settings.json (+%d hook(s), %d repointed to the current binary)", added, repaired))
	case repaired > 0:
		res.Updated = append(res.Updated, fmt.Sprintf(
			".claude/settings.json (%d hook(s) repointed to the current binary)", repaired))
	default:
		res.Updated = append(res.Updated, fmt.Sprintf(".claude/settings.json (+%d hook(s))", added))
	}
	return nil
}

// retargetOrionHooks rewrites the binary in every Orion hook command that
// names a different one, and reports how many it changed.
//
// Recognising our own entry by SHAPE rather than by exact string is what
// makes a re-run a repair. The shape is "<path ending in orion> hook <name>",
// which no plausible third-party hook shares, so nothing else is touched.
func retargetOrionHooks(hooks map[string]any, binary string) int {
	n := 0
	for _, raw := range hooks {
		entries, _ := raw.([]any)
		for _, re := range entries {
			entry, ok := re.(map[string]any)
			if !ok {
				continue
			}
			inner, _ := entry["hooks"].([]any)
			for _, h := range inner {
				hm, ok := h.(map[string]any)
				if !ok {
					continue
				}
				cmd, _ := hm["command"].(string)
				f := strings.Fields(cmd)
				if len(f) < 3 || f[len(f)-2] != "hook" {
					continue
				}
				base := strings.TrimSuffix(filepath.Base(f[0]), ".exe")
				if base != "orion" || f[0] == binary {
					continue
				}
				f[0] = binary
				hm["command"] = strings.Join(f, " ")
				n++
			}
		}
	}
	return n
}

// hasCommand reports whether Orion's hook is already installed for an event,
// matching on the command and matcher so a re-run is a no-op.
func hasCommand(list []any, command, matcher string) bool {
	for _, raw := range list {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m, _ := entry["matcher"].(string); m != matcher {
			continue
		}
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if c, _ := hm["command"].(string); c == command {
				return true
			}
		}
	}
	return false
}

// Summary renders what happened, leading with anything that needs a human.
func (r *Result) Summary() string {
	var b strings.Builder
	sort.Strings(r.Created)
	sort.Strings(r.Skipped)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "WARNING  %s\n", w)
	}
	for _, c := range r.Created {
		fmt.Fprintf(&b, "created  %s\n", c)
	}
	for _, u := range r.Updated {
		fmt.Fprintf(&b, "updated  %s\n", u)
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(&b, "skipped  %s\n", s)
	}
	if r.Backup != "" {
		fmt.Fprintf(&b, "backup   %s\n", r.Backup)
	}
	return b.String()
}

// defaultConfig is the starting orion.json for an adopted repo. It differs
// from a fresh workspace's in one respect: the plan gate is off unless asked
// for, because an existing repo has habits Orion should not break on day one.
const defaultConfig = `{
  "version": 1,

  "_comment_limits": "A limit of 0 restores the default rather than meaning unlimited: 'no limit' is never a safe reading of an absent value in a circuit breaker.",
  "limits": {
    "max_tool_calls": 400,
    "max_repeat_identical": 4,
    "max_consecutive_failures": 3,
    "max_same_command_failures": 3,
    "max_session_minutes": 90,
    "max_edits_without_verify": 25,
    "max_files_touched": 60
  },

  "_comment_gates": "require_plan_before_edit is OFF for an adopted repo. In a codebase where small changes are made without writing a plan first, leaving it on means hitting a wall on the first edit, and people disable Orion entirely rather than change one setting. Turn it on when the team is ready.",
  "gates": {
    "require_plan_before_edit": %t,
    "protect_tests_during_fix": true,
    "production_requires_authorization": true,
    "block_direct_push_to_default_branch": true
  },

  "paths": {
    "intent": "docs/intent",
    "specs": "specs",
    "plans": "plans",
    "evals": "evals",
    "state": ".orion/state",
    "protected": [".github/workflows/**", "orion.json", "managed-settings.json"]
  },

  "vcs": {
    "provider": "github",
    "default_branch": "main",
    "work_branch": "develop",
    "protected_branches": ["main", "develop"],
    "branch_prefix": "orion/"
  },

  "_comment_budget": "YOUR weekly budget, not your Anthropic plan's allowance. Zero means unlimited.",
  "budget": {
    "weekly_usd": 0,
    "weekly_tokens": 0,
    "pause_at_percent": [50, 75, 90, 95]
  },

  "tracker": {
    "enabled": false,
    "provider": "jira",
    "project_key": "",
    "create_project_per_idea": true,
    "confirm_tree_before_create": true
  },

  "slack": {
    "enabled": false,
    "create_channel_per_project": true,
    "channel_prefix": "orion-",
    "private": true
  },

  "delegation": {
    "enabled": true,
    "extra_tool_calls_for_review": 200,
    "deep_security_review_when": "high-risk"
  }
}
`
