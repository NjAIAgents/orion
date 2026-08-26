package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Capability checks answer "can Orion actually do the thing" rather than
// "is the binary present".
//
// These run at install time AND before any run that needs them, because an
// install-time check proves nothing later: a token that passed in March has
// expired by June, and the check that "passed" is now a lie. Re-running the
// expensive ones on every invocation would be slow, so results are cached
// with a short TTL and invalidated when config changes.

const cacheTTL = 12 * time.Hour

type cacheFile struct {
	CheckedAt  time.Time         `json:"checked_at"`
	ConfigHash string            `json:"config_hash"`
	Results    map[string]string `json:"results"` // name -> "OK"|"WARN: ..."|"FAIL: ..."
}

func cachePath() string { return filepath.Join(workspace.Home(), "doctor.json") }

// Fresh reports whether a cached verdict can be trusted. A miss is not an
// error; it just means the check runs again.
func Fresh(configHash string) (map[string]string, bool) {
	b, err := os.ReadFile(cachePath())
	if err != nil {
		return nil, false
	}
	var c cacheFile
	if json.Unmarshal(b, &c) != nil {
		return nil, false
	}
	if time.Since(c.CheckedAt) > cacheTTL {
		return nil, false
	}
	// A config change can invalidate a capability verdict, for example
	// pointing at a different Jira instance. Trusting a stale verdict there
	// would be worse than rechecking.
	if c.ConfigHash != configHash {
		return nil, false
	}
	return c.Results, true
}

func SaveCache(configHash string, results map[string]string) {
	c := cacheFile{CheckedAt: time.Now().UTC(), ConfigHash: configHash, Results: results}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(workspace.Home(), 0o755)
	_ = os.WriteFile(cachePath(), b, 0o644)
}

// checkGHScopes verifies gh can actually create a repository, not merely
// that it is logged in. `gh auth status` passes with a token that lacks the
// repo scope, and the failure then surfaces at provisioning time, after an
// idea has been captured and planned.
func checkGHScopes() check {
	p, err := exec.LookPath("gh")
	if err != nil {
		return check{"gh repo scope", warn, "gh not installed",
			"Orion creates the remote through gh. Stages 1 to 4 work without it."}
	}
	out, err := exec.Command(p, "auth", "status").CombinedOutput()
	if err != nil {
		return check{"gh repo scope", warn, "not authenticated", "Run: gh auth login"}
	}
	s := string(out)
	if strings.Contains(s, "'repo'") || strings.Contains(s, "repo,") || strings.Contains(s, " repo") {
		return check{"gh repo scope", ok, "repo scope present", ""}
	}
	return check{"gh repo scope", warn, "could not confirm the repo scope",
		"If `orion provision` fails to create a repository, run:\n" +
			"gh auth refresh -h github.com -s repo,workflow"}
}

// checkNJAgents confirms the delegated skill library is installed. Orion
// delegates review, security, testing and PR authoring to it, so its absence
// is not cosmetic: those stages have no fallback and should not be faked
// with a thinner substitute.
func checkNJAgents() check {
	home, err := os.UserHomeDir()
	if err != nil {
		return check{"nj-agents", warn, "cannot resolve home directory", ""}
	}
	skills := filepath.Join(home, ".claude", "skills")
	required := []string{"pre-push-review", "review-secrets", "review-tests-build", "pr-describe"}

	var missing []string
	for _, s := range required {
		if _, err := os.Stat(filepath.Join(skills, s)); err != nil {
			missing = append(missing, s)
		}
	}
	switch {
	case len(missing) == len(required):
		return check{"nj-agents", fail, "not installed at ~/.claude/skills",
			"Orion delegates review, security, testing and PR authoring to nj-agents.\n" +
				"Install it: git clone https://github.com/navjyotnishant/nj-agents && ./install.sh"}
	case len(missing) > 0:
		return check{"nj-agents", warn, "partially installed, missing: " + strings.Join(missing, ", "),
			"Re-run ./install.sh in the nj-agents repo."}
	default:
		return check{"nj-agents", ok, "installed (" + fmt.Sprint(len(required)) + " required skills present)", ""}
	}
}

// checkJira probes reachability, authentication and the project-creation
// permission. The permission check is the point: Orion is configured to
// create a project per idea, and discovering mid-run that the account is not
// an admin wastes the whole run.
func checkJira(required bool) check {
	j, err := tracker.NewJiraFromEnv()
	if err != nil {
		g := warn
		if required {
			g = fail
		}
		return check{"jira", g, "not configured", err.Error()}
	}
	cap, err := j.Probe()
	if err != nil {
		return check{"jira", fail, "unreachable", cap.Detail}
	}
	if !cap.Authenticated {
		return check{"jira", fail, "authentication failed", cap.Detail}
	}
	if cap.Undetermined {
		// Distinct from a denial. Reporting "cannot create projects" here
		// would send the user to fix a permission they may already have.
		return check{"jira", warn, "permission undetermined", cap.Detail}
	}
	if !cap.CanCreateProject {
		// WARN rather than FAIL: Orion degrades to binding an existing
		// project key, so this is a reduced capability, not a dead stop.
		return check{"jira", warn, "cannot create projects", cap.Detail}
	}
	return check{"jira", ok, cap.Detail, ""}
}

// checkDisk catches the failure that looks like everything else failing.
// A workspace, its logs and a node_modules will not fit in 200MB, and the
// resulting errors are wildly misleading.
func checkDisk() check {
	dir := workspace.Home()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return check{"disk", fail, "cannot write to " + dir, err.Error()}
	}
	probe := filepath.Join(dir, ".write-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		return check{"disk", fail, dir + " is not writable", err.Error()}
	}
	_ = os.Remove(probe)
	return check{"disk", ok, dir + " writable", ""}
}
