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
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
	PlanGate bool
	Force    bool
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
	if opts.Binary == "" {
		opts.Binary = "orion"
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
		// Keep the shape visible in git from the first commit.
		_ = os.WriteFile(filepath.Join(p, ".gitkeep"), nil, 0o644)
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
	if b, err := os.ReadFile(p); err == nil {
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
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

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

	if added == 0 {
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
	res.Updated = append(res.Updated, fmt.Sprintf(".claude/settings.json (+%d hook(s))", added))
	return nil
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
