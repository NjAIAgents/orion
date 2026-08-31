// Package agentcfg decides what a supervised run is allowed to be.
//
// A `claude -p` child started from an operator's shell inherits that
// operator's ENTIRE Claude Code configuration: their plugins, their MCP
// servers, their subagents, their slash commands and whatever system-prompt
// text a third-party plugin injects. Orion chose none of it, and it decides
// what the agent can do, how it writes, and what a run costs. Measured on a
// real run: 179 tools, 148 of them MCP tools against the operator's live,
// authenticated SaaS accounts -- createJiraIssue and editJiraIssue among
// them. Orion's breaker, sandbox and approval path govern the filesystem,
// git and the network. They do not govern any of those.
//
// Three problems, and the order matters because the fix is the same one:
//
//	blast radius     a write handle to the tracker that decides what gets
//	                 worked, held by an agent working one ticket
//	reproducibility  the same ticket on two machines is two different runs,
//	                 so an eval baseline measures the operator's plugin
//	                 folder as much as the change under test
//	cost             tool definitions are re-sent every turn, and the
//	                 implementer runs 120 to 600 turns
//
// THE FIX IS NOT ISOLATION. Orion DEPENDS on inherited configuration:
// /pre-push-review, /pm-plan, /pr-describe and the rest of nj-agents arrive
// by exactly this mechanism, and `orion doctor` grades a missing nj-agents
// as FAIL. Blanking the config would break the stages this project delegates.
// So the child gets its OWN directory, Orion-managed, populated with the
// toolkit Orion actually depends on and nothing else -- a decision rather
// than an inheritance.
//
// Two levers, because one does not reach far enough:
//
//	CLAUDE_CONFIG_DIR    what the directory holds: skills, agents, commands
//	                     and plugins. Curated here.
//	--strict-mcp-config  MCP servers, which are also configured per account
//	                     and would survive a directory swap. With no
//	                     --mcp-config alongside it, the run gets none.
//
// See docs/decisions/0014-supervised-runs-get-a-curated-config-directory.md,
// including why the tracker's own MCP is not among them.
package agentcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/njagents"
)

// DirName is the curated directory's name under ORION_HOME. One directory
// shared by every run, not one per workspace: what a run is given is a
// property of this machine's Orion installation, and a per-workspace copy
// would let two tickets on one machine run with different toolsets, which is
// the reproducibility problem again with a smaller radius.
const DirName = "agent-config"

// credentialsFile is the one thing carried over from the operator's own
// config directory. On Linux this file IS the CLI's authentication; moving
// CLAUDE_CONFIG_DIR without it would leave every run unable to log in. It is
// symlinked rather than copied, so a refreshed token stays valid and no
// secret is duplicated onto disk.
const credentialsFile = ".credentials.json"

// curationAuthenticates reports whether a run under a curated
// CLAUDE_CONFIG_DIR can still authenticate on this platform.
//
// It cannot on macOS, and that is not a bug we can carry over the way Linux
// carries .credentials.json. Two separate mechanisms defeat it (OR-239):
//
//	the CLI requires .claude.json INSIDE CLAUDE_CONFIG_DIR, while the
//	operator's own lives at ~/.claude.json, OUTSIDE ~/.claude/ -- so even
//	pointing the variable at the operator's own directory fails
//
//	credentials live in the Keychain and are not reached for a non-default
//	config directory, so supplying .claude.json is necessary and still not
//	sufficient: a full copy of ~/.claude plus that file still reports the
//	login as expired
//
// The original comment here assumed the Keychain made this a no-op on macOS.
// It shipped in v0.8.3 and made EVERY supervised run on macOS fail
// immediately, while the operator's own CLI stayed authenticated.
//
// So: no curated directory on darwin. This is a real loss -- the run gets the
// operator's whole plugin surface, which is the blast radius this package
// exists to remove -- and it is reported as a warning on every run rather
// than taken quietly, because an operator who thinks their runs are curated
// when they are not is worse off than one who knows they are not.
func curationAuthenticates() bool { return runtime.GOOS != "darwin" }

// CurationAuthenticates is curationAuthenticates for other packages.
//
// Exported for two callers that must agree with this one rather than restate
// the rule: `orion doctor`, which has to grade what a RUN will get instead of
// what the operator's own shell gets, and the tests in cmd/orion and
// internal/supervisor that assert a curated run and cannot hold where
// curation is unavailable. One predicate, so the answer cannot drift.
func CurationAuthenticates() bool { return curationAuthenticates() }

// Run is the configuration one child run was given, and the record of it.
type Run struct {
	// Dir is CLAUDE_CONFIG_DIR for the child. Empty means the operator's own
	// configuration was inherited, which only happens on an explicit opt-in.
	Dir string
	// Inherited and OptIn record the operator's choice, so a run made with
	// the whole plugin surface in scope says so rather than being discovered
	// later from a transcript.
	Inherited bool
	OptIn     string // what matched, e.g. "stage:review" or "actor:qa"

	Skills   []string
	Agents   []string
	Warnings []string
}

// For builds the configuration for a run of this stage by this actor.
//
// Failing to build is fatal to the caller by design. Degrading to the
// operator's configuration would silently restore exactly the blast radius
// this exists to remove, and a security default that turns itself off when
// a mkdir fails is not a default.
func For(orionHome string, cfg config.Config, stage, actor string) (*Run, error) {
	if via := optIn(cfg.Delegation.InheritOperatorConfig, stage, actor); via != "" {
		return &Run{Inherited: true, OptIn: via}, nil
	}

	// A curated directory that cannot log in is not a safer run, it is no run
	// at all. Inherit deliberately and say so, rather than handing the child a
	// directory that fails at the first call (OR-239).
	if !curationAuthenticates() {
		return &Run{
			Inherited: true,
			OptIn:     "platform:" + runtime.GOOS,
			Warnings: []string{
				"this run inherited YOUR Claude Code configuration -- plugins, MCP servers and " +
					"subagents included -- because a curated config directory cannot authenticate " +
					"on " + runtime.GOOS + ". The run is not capability-curated. See OR-239.",
			},
		}, nil
	}

	dir := filepath.Join(orionHome, DirName)
	r := &Run{Dir: dir}
	for _, d := range []string{dir, filepath.Join(dir, "skills"), filepath.Join(dir, "agents")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("provisioning the agent config directory %s: %w", d, err)
		}
	}

	inst := njagents.Discover(cfg.Delegation.NJAgentsDir, orionHome)
	if inst == nil || inst.Root == "" {
		// Not fatal here: `orion doctor` is what grades a missing toolkit,
		// and it grades it FAIL. Saying it twice at different severities
		// would leave two answers to one question.
		r.Warnings = append(r.Warnings,
			"nj-agents was not found, so this run has no delegated skills; check: orion doctor")
	} else {
		r.Skills = linkAll(filepath.Join(inst.Root, "skills"), filepath.Join(dir, "skills"), r)
		r.Agents = linkAll(filepath.Join(inst.Root, "agents"), filepath.Join(dir, "agents"), r)
	}
	linkCredentials(dir, r)
	return r, nil
}

// Env returns base with CLAUDE_CONFIG_DIR set to the curated directory.
//
// Any inherited CLAUDE_CONFIG_DIR is REMOVED first rather than shadowed:
// two entries for one name in a child's environment resolve to whichever
// the C library happens to find first, which is not a coin Orion should be
// flipping over where the agent's capabilities come from.
func (r *Run) Env(base []string) []string {
	if r == nil || r.Dir == "" {
		return base
	}
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); k == "CLAUDE_CONFIG_DIR" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "CLAUDE_CONFIG_DIR="+r.Dir)
}

// Args are the CLI flags that go with the directory.
//
// --strict-mcp-config with no --mcp-config beside it means no MCP servers at
// all. It is a separate lever from the directory because MCP servers are not
// only configured there -- an account-level connector arrives regardless of
// which directory the CLI reads -- so curating the directory alone would
// leave the write handles to the tracker exactly where they were.
func (r *Run) Args() []string {
	if r == nil || r.Inherited {
		return nil
	}
	return []string{"--strict-mcp-config"}
}

// Describe is the one line a person reads in the event log. It states what
// the run was GIVEN, which until now could only be discovered by reading a
// raw transcript -- which is how this went unnoticed.
func (r *Run) Describe() string {
	if r == nil {
		return "agent config: unknown"
	}
	if r.Inherited {
		return "agent config: the operator's own, opted in by " + r.OptIn +
			" (plugins and MCP servers included)"
	}
	return fmt.Sprintf("agent config: Orion-managed, %d nj-agents skills, %d agents, no MCP servers",
		len(r.Skills), len(r.Agents))
}

// optIn reports which configured entry, if any, puts this run on the
// operator's own configuration. Matched against the stage OR the actor, case
// insensitively, because an operator thinks in whichever of the two the
// plugin is for -- "the review stage needs it" and "the QA agent needs it"
// are both natural and neither is wrong.
func optIn(entries []string, stage, actor string) string {
	for _, e := range entries {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if stage != "" && e == strings.ToLower(stage) {
			return "stage:" + e
		}
		if actor != "" && e == strings.ToLower(actor) {
			return "actor:" + e
		}
	}
	return ""
}

// linkAll relinks every entry of src into dst and returns their names.
//
// Symlinks rather than copies: the toolkit is a checkout the operator may
// update at any moment, and a copy would pin every run to whenever Orion
// last synced it while reporting the checkout's commit.
func linkAll(src, dst string, r *Run) []string {
	entries, err := os.ReadDir(src)
	if err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("could not read %s: %v", src, err))
		return nil
	}
	prune(dst)

	var names []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if err := os.Symlink(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("could not link %s: %v", name, err))
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".md"))
	}
	sort.Strings(names)
	return names
}

// prune drops the links a previous build left, so a skill deleted from the
// toolkit stops being offered to the next run.
//
// SYMLINKS ONLY. Anything else in there was put there by a person, and a
// tool that deletes a file it did not create is a tool nobody can leave a
// note in.
func prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// linkCredentials keeps the child authenticated after the directory move.
func linkCredentials(dir string, r *Run) {
	src := filepath.Join(operatorDir(), credentialsFile)
	if _, err := os.Lstat(src); err != nil {
		// Not silent. This used to return quietly on the assumption that an
		// absent file meant the platform kept credentials elsewhere and all
		// was well; on macOS that assumption was wrong and every run failed
		// to authenticate with nothing said here (OR-239). A child that
		// cannot log in is a setup fault, so say so where it happened.
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"no %s in %s, so this run has nothing to authenticate with; check: orion doctor",
			credentialsFile, operatorDir()))
		return
	}
	dst := filepath.Join(dir, credentialsFile)
	if _, err := os.Lstat(dst); err == nil {
		return
	}
	if err := os.Symlink(src, dst); err != nil {
		r.Warnings = append(r.Warnings,
			fmt.Sprintf("could not link %s: %v; the run may not be authenticated", credentialsFile, err))
	}
}

// operatorDir is the configuration directory this run would otherwise have
// inherited -- the one whose contents are the problem.
func operatorDir() string {
	if d := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}
