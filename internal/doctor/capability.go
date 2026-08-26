package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/njagents"
	"github.com/orion-sdlc/orion/internal/slack"
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

// checkNJAgents confirms the delegated toolkit is present AND intact.
//
// This is a hard dependency. Orion delegates review, secret scanning,
// test/build verification, PR authoring and PM decomposition to nj-agents;
// those stages have no fallback and must not be faked with a thinner
// substitute. So a missing toolkit is FAIL, not WARN.
//
// The check resolves the symlink rather than trusting the skills directory.
// Skills install as links back to a clone, and the shared contract they all
// read (CONVENTIONS.md) lives at that clone's root, two levels up. Looking
// only in ~/.claude/skills passes while the file the skills depend on is
// absent.
func checkNJAgents(configured string, autoFix bool) check {
	home := workspace.Home()
	inst := njagents.Discover(configured, home)

	if inst == nil && autoFix {
		cloned, err := njagents.Clone(home, "")
		if err != nil {
			return check{"nj-agents", fail, "not installed, and the clone failed",
				err.Error() + "\nManually: " + njagents.CloneCommand(home)}
		}
		inst = cloned
	}

	if inst == nil {
		return check{"nj-agents", fail, "not installed",
			"Orion delegates review, security, testing, PR authoring and PM\n" +
				"decomposition to nj-agents. Those stages have no fallback.\n" +
				"Fetch it automatically:  orion doctor --fix\n" +
				"Or do it yourself:       " + njagents.CloneCommand(home)}
	}

	if len(inst.Missing) > 0 {
		return check{"nj-agents", fail,
			"incomplete at " + inst.Root + " (" + inst.Via + ")",
			"Missing: " + strings.Join(inst.Missing, ", ") + "\n" +
				"A partial checkout is worse than none: the review skills read the\n" +
				"shared contracts at the repo root and fail confusingly without them.\n" +
				"Repair with: git -C " + inst.Root + " pull"}
	}

	detail := inst.Root + " (" + inst.Via
	if inst.Commit != "" {
		detail += " @ " + inst.Commit
		if inst.Dirty {
			detail += ", modified"
		}
	}
	detail += ")"

	if len(inst.Warnings) > 0 {
		return check{"nj-agents", warn, detail, strings.Join(inst.Warnings, "\n")}
	}
	return check{"nj-agents", ok, detail, ""}
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

// checkSlack verifies the bot token can actually do what Orion needs.
//
// Naming the workspace is the point. A token for the wrong workspace
// authenticates perfectly and posts into somewhere nobody reads, which looks
// exactly like working until someone asks why they never saw a message.
func checkSlack(enabled bool) check {
	c, err := slack.FromEnv()
	if err != nil {
		g := warn
		if !enabled {
			// Not configured and not asked for: this is not a problem.
			return check{"slack", ok, "not configured (optional)", ""}
		}
		return check{"slack", g, "enabled in config but not configured", err.Error()}
	}
	id, err := c.AuthTest()
	if err != nil {
		return check{"slack", warn, "token rejected", err.Error()}
	}
	return check{"slack", ok,
		fmt.Sprintf("%s as %s (workspace %s)", id.Team, id.User, id.TeamID),
		""}
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
