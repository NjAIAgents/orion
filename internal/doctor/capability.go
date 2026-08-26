package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/creds"
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
	_ = os.MkdirAll(workspace.Home(), workspace.HomeDirMode)
	// The cache records who you authenticated as; it lives beside the
	// credentials file and should not be more readable than it is.
	_ = os.WriteFile(cachePath(), b, workspace.PrivateFileMode)
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
func checkJira(enabled bool) check {
	j, err := tracker.NewJiraFromEnv()
	if err != nil {
		g := warn
		if enabled {
			g = fail
		}
		return check{"jira", g, "not configured", err.Error()}
	}
	// Grade against whether Jira is actually required. Orion runs perfectly
	// without a tracker, so a bad token on an OPTIONAL integration must not
	// report "Orion cannot run": that turns a degraded capability into a
	// hard stop and sends someone fixing Jira before they can do anything
	// else. Only a tracker the config asks for can block.
	g := warn
	if enabled {
		g = fail
	}
	cap, err := j.Probe()
	if err != nil {
		return check{"jira", g, "unreachable", cap.Detail}
	}
	if !cap.Authenticated {
		// Name the SOURCE before the remedy. Without it this message lists the
		// two variables and sends the user to `orion config`, which edits the
		// config FILE: the one place that may already be correct. If the shell
		// is exporting a stale value, the file wins nothing, the failure never
		// moves, and the loop has no exit. Observed costing an hour.
		src, hint := jiraCredSource()
		return check{"jira", g, "authentication failed (credentials from: " + src + ")",
			cap.Detail + hint +
				"\n  A 401 looks the same whether the token was revoked, the email does not\n" +
				"  match the account that created it, or the token was truncated on copy."}
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

// checkHooks verifies that the hook commands in .claude/settings.json point
// at something that can actually run.
//
// This is the gap that let a broken install look healthy. doctor checked that
// orion.json parsed and reported the limits as "in force", while the hooks
// meant to enforce them pointed at a deleted directory. A hook that cannot
// execute does not announce itself as a missing guardrail, so the breaker,
// the shield and the push gate all read as enabled and none of them ran.
func checkHooks(dir string) check {
	p := filepath.Join(dir, ".claude", "settings.json")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return check{"hooks", warn, "no .claude/settings.json",
				"Nothing is enforcing the limits in orion.json. Run: orion init"}
		}
		return check{"hooks", warn, "cannot read " + p, err.Error()}
	}
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &root) != nil {
		return check{"hooks", warn, p + " is not valid JSON",
			"Claude Code cannot read it either, so no hook is running."}
	}

	var total int
	var broken []string
	for _, entries := range root.Hooks {
		for _, e := range entries {
			for _, h := range e.Hooks {
				cmd := strings.Fields(h.Command)
				if len(cmd) == 0 || !strings.HasSuffix(cmd[0], "orion") {
					continue // someone else's hook; not ours to judge
				}
				total++
				if strings.ContainsRune(cmd[0], os.PathSeparator) {
					if _, statErr := os.Stat(cmd[0]); statErr != nil {
						broken = append(broken, cmd[0])
					}
				} else if _, lookErr := exec.LookPath(cmd[0]); lookErr != nil {
					broken = append(broken, cmd[0]+" (not on PATH)")
				}
			}
		}
	}

	switch {
	case total == 0:
		return check{"hooks", warn, "no Orion hooks wired",
			"orion.json's limits are advisory until the hooks exist. Run: orion init"}
	case len(broken) > 0:
		return check{"hooks", fail,
			fmt.Sprintf("%d of %d hook command(s) do not resolve", len(broken), total),
			"Missing: " + strings.Join(uniq(broken), "\n           ") +
				"\n  Every gate is silently doing nothing. This usually follows an\n" +
				"  upgrade that moved the binary, or removing the copy the hooks\n" +
				"  pointed at. Repair with: orion init --force\n" +
				"  That rewires the hooks and leaves orion.json alone."}
	}
	return check{"hooks", ok, fmt.Sprintf("%d hook(s) wired and resolvable", total), ""}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// jiraCredSource reports where the Jira credentials actually came from, and
// warns when an exported variable is shadowing a stored one.
//
// Environment beats file by design, so a correct config.env can sit behind a
// stale export indefinitely. That state is invisible to every obvious check:
// an export made in one terminal is in no rc file and no config, so nothing on
// disk reveals it, and the same command passes in a new shell.
func jiraCredSource() (string, string) {
	home := workspace.Home()
	file, _ := creds.Load(home)

	keys := []string{creds.JiraURL, creds.JiraEmail, creds.JiraToken}
	var fromEnv, shadowed []string
	for _, k := range keys {
		if creds.Source(home, k) != "environment" {
			continue
		}
		fromEnv = append(fromEnv, k)
		if file[k] != "" {
			shadowed = append(shadowed, k)
		}
	}

	if len(fromEnv) == 0 {
		return "config file " + creds.Path(home),
			"\n  Re-run: orion config --only ORION_JIRA_EMAIL,ORION_JIRA_TOKEN"
	}

	src := "environment"
	if len(fromEnv) < len(keys) {
		src = "environment + config file"
	}
	h := "\n  Exported in this shell: " + strings.Join(fromEnv, " ") +
		"\n  These OVERRIDE " + creds.Path(home) + "."
	if len(shadowed) > 0 {
		verb := " also has a stored value, which is being ignored."
		if len(shadowed) > 1 {
			verb = " also have stored values, which are being ignored."
		}
		h += "\n  " + strings.Join(shadowed, " ") + verb
	}
	h += "\n  If the exported value is stale, clear it and retry:" +
		"\n    unset " + strings.Join(fromEnv, " ") +
		"\n  Otherwise correct the export, or re-run: orion config"
	return src, h
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

// checkSlackAudience asks the question every other Slack check skipped: can
// a person actually READ what Orion sends?
//
// Every existing check passes on a private channel whose only member is the
// bot. The token is valid, the channel resolves, the post is accepted, the
// approval scopes are granted -- and no human can see the message, find it
// by search, or learn the channel exists. fcia ran that way for two full
// pipelines. The symptom is indistinguishable from Slack being broken,
// which is exactly how it was repeatedly misdiagnosed.
//
// Checked here rather than only at init because init runs once and this can
// become true later: somebody leaves the channel, or a workspace record is
// repointed at a room nobody joined.
func checkSlackAudience(ws *workspace.Workspace) check {
	if ws == nil || ws.Task.Slack == nil || ws.Task.Slack.ID == "" {
		return check{"slack audience", ok, "no channel bound yet", ""}
	}
	name := ws.Task.Slack.Name
	c, err := slack.FromEnv()
	if err != nil {
		return check{"slack audience", ok, "slack not configured (optional)", ""}
	}
	members, err := c.Members(ws.Task.Slack.ID)
	if err != nil {
		// Usually a missing read scope. Not a failure of the channel.
		return check{"slack audience", warn,
			"could not read the members of #" + name, err.Error()}
	}
	self := ""
	if id, aErr := c.AuthTest(); aErr == nil {
		self = id.UserID
	}
	people := 0
	for _, m := range members {
		if m != self && strings.TrimSpace(m) != "" {
			people++
		}
	}
	if people == 0 {
		return check{"slack audience", fail,
			"#" + name + " has no members except the bot",
			"Everything Orion sends there is delivered and unreadable: a private\n" +
				"channel is invisible to anyone not in it, with no notification that\n" +
				"it exists.\n" +
				"  Add yourself in Slack, or set slack.invite_users in orion.json\n" +
				"  and re-run: orion init --force"}
	}
	return check{"slack audience",
		ok, fmt.Sprintf("#%s has %d human member(s)", name, people), ""}
}

// checkDisk catches the failure that looks like everything else failing.
// A workspace, its logs and a node_modules will not fit in 200MB, and the
// resulting errors are wildly misleading.
func checkDisk() check {
	dir, err := workspace.EnsureHome()
	if err != nil {
		return check{"disk", fail, "cannot write to " + dir, err.Error()}
	}
	probe := filepath.Join(dir, ".write-probe")
	if err := os.WriteFile(probe, []byte("x"), workspace.PrivateFileMode); err != nil {
		return check{"disk", fail, dir + " is not writable", err.Error()}
	}
	_ = os.Remove(probe)

	// Report the tree's exposure, not just that it is writable. This holds
	// credentials, a spend ledger and full agent transcripts; a 0755 home
	// from an earlier version is a quiet, durable leak to every other
	// account on the machine.
	if creds.PermsSupported() {
		if fi, statErr := os.Stat(dir); statErr == nil && fi.Mode().Perm()&0o077 != 0 {
			return check{"disk", warn,
				fmt.Sprintf("%s is mode %o, readable by other users", dir, fi.Mode().Perm()),
				"It holds credentials, usage and full run transcripts.\nFix with: chmod -R go-rwx " + dir}
		}
	}
	return check{"disk", ok, dir + " writable, owner-only", ""}
}
