package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// writeSettings generates the per-workspace Claude Code settings.
//
// Three layers, and each closes a hole the others cannot:
//
//	permissions  tool-level. Stops the agent asking for a secret by name.
//	sandbox      OS-level. Stops egress that a tool-level deny cannot see,
//	             because a shell command can reach the network without ever
//	             touching WebFetch.
//	hooks        semantic. Stops the things that are legal at both other
//	             layers but wrong here: pushing to main, looping forever.
//
// permissions.allow exists to keep the deny list usable. A deny list with
// no matching allow list turns every safe inner-loop command into an
// approval prompt, and a user who is prompted forty times an hour learns
// to approve without reading.
func writeSettings(ws *Workspace) error {
	bin := orionBinary()

	hookEntry := func(matcher, name string) map[string]any {
		return map[string]any{
			"matcher": matcher,
			"hooks": []map[string]any{{
				"type":    "command",
				"command": fmt.Sprintf("%s hook %s", bin, name),
			}},
		}
	}

	settings := map[string]any{
		"permissions": map[string]any{
			"deny": []string{
				// Credentials must never enter the agent's context. Once read,
				// a secret is in the transcript and cannot be recalled.
				"Read(.env*)",
				"Read(./secrets/**)",
				"Read(**/id_rsa*)",
				"Read(**/.aws/credentials)",
				"Read(**/.npmrc)",
				// Arbitrary egress through a tool.
				"WebFetch",
				"Bash(curl *)",
				"Bash(wget *)",
				// The agent must not edit its own controls.
				"Edit(orion.json)",
				"Edit(.github/workflows/**)",
			},
			"allow": []string{
				"Bash(git *)",
				"Bash(gh pr *)",
				"Bash(gh repo view*)",
				"Bash(make *)",
				"Bash(npm run *)", "Bash(npm test*)", "Bash(npm ci*)", "Bash(npm install*)",
				"Bash(go build*)", "Bash(go test*)", "Bash(go vet*)",
				"Bash(pytest*)", "Bash(python -m pytest*)",
				"Bash(cargo build*)", "Bash(cargo test*)",
				"Read(**)",
				"Edit(**)",
				"Write(**)",
			},
		},

		"sandbox": map[string]any{
			"enabled": true,
			// If the sandbox cannot initialize, refuse to run. The alternative
			// is running unsandboxed while believing you are sandboxed, which
			// is worse than not running.
			"failIfUnavailable":        true,
			"allowUnsandboxedCommands": false,
			"network": map[string]any{
				"allowedDomains": defaultAllowedDomains,
			},
			"credentials": map[string]any{
				"files": []map[string]string{
					{"path": "~/.ssh", "mode": "deny"},
					{"path": "~/.aws/credentials", "mode": "deny"},
					{"path": "~/.config/gcloud", "mode": "deny"},
				},
				"envVars": []map[string]string{
					{"name": "AWS_SECRET_ACCESS_KEY", "mode": "deny"},
					{"name": "ANTHROPIC_API_KEY", "mode": "deny"},
				},
			},
		},

		"hooks": map[string]any{
			"SessionStart": []map[string]any{
				{"hooks": []map[string]any{{
					"type": "command", "command": fmt.Sprintf("%s hook session-start", bin),
				}}},
			},
			"PreToolUse": []map[string]any{
				hookEntry("Bash", "gate"),
				hookEntry("Edit|Write|MultiEdit|NotebookEdit", "shield"),
				hookEntry("*", "breaker"),
			},
			"PostToolUse": []map[string]any{
				hookEntry("*", "breaker"),
			},
		},
	}

	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(ws.SettingsPath(), b, 0o644); err != nil {
		return err
	}
	return nil
}

// defaultAllowedDomains is the minimum a build needs. Anything else is a
// deliberate addition, and every addition widens what a compromised
// dependency could reach.
var defaultAllowedDomains = []string{
	"api.anthropic.com",
	"github.com",
	"api.github.com",
	"objects.githubusercontent.com",
	"codeload.github.com",
	"registry.npmjs.org",
	"pypi.org",
	"files.pythonhosted.org",
	"proxy.golang.org",
	"sum.golang.org",
	"crates.io",
	"static.crates.io",
}

// orionBinary resolves the path to write into hook commands. Prefers the
// running executable so a workspace stays pinned to the binary that
// provisioned it, which keeps hook behaviour stable if the user later
// installs a different version on PATH.
func orionBinary() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
		return exe
	}
	if p, err := exec.LookPath("orion"); err == nil {
		return p
	}
	return "orion"
}

// defaultProjectConfig is the orion.json dropped into a new workspace.
// It mirrors templates/orion.json; the duplication is deliberate so a
// provisioned workspace does not depend on the plugin being installed.
const defaultProjectConfig = `{
  "version": 1,
  "limits": {
    "max_tool_calls": 400,
    "max_repeat_identical": 4,
    "max_consecutive_failures": 3,
    "max_same_command_failures": 3,
    "max_session_minutes": 90,
    "max_edits_without_verify": 25,
    "max_files_touched": 60
  },
  "gates": {
    "require_plan_before_edit": true,
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
  "autonomy": {
    "dev": "gated_write",
    "staging": "gated_write",
    "production": "propose_only"
  },
  "auto_merge": {
    "enabled": false,
    "environments": ["dev"],
    "require_checks": ["build", "test", "lint", "agent-evals"],
    "require_eval_pass_rate": 0.95,
    "min_eval_cases": 20,
    "max_changed_files": 20
  },
  "vcs": {
    "provider": "github",
    "default_branch": "main",
    "work_branch": "develop",
    "protected_branches": ["main", "develop"],
    "branch_prefix": "orion/"
  },
  "tracker": {
    "provider": "jira",
    "project_key": "",
    "create_project_per_idea": true,
    "confirm_tree_before_create": true
  },
  "delegation": {
    "enabled": true,
    "extra_tool_calls_for_review": 200,
    "deep_security_review_when": "high-risk"
  }
}
`
