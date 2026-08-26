// Package config loads orion.json from the project root and supplies
// defaults for anything absent. Every value here is a hard limit enforced
// by a hook, so an absent or malformed config must never silently widen
// a control: parse failures fall back to defaults and are reported.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Limits struct {
	MaxToolCalls           int `json:"max_tool_calls"`
	MaxRepeatIdentical     int `json:"max_repeat_identical"`
	MaxConsecutiveFailures int `json:"max_consecutive_failures"`
	MaxSameCommandFailures int `json:"max_same_command_failures"`
	MaxSessionMinutes      int `json:"max_session_minutes"`
	MaxEditsWithoutVerify  int `json:"max_edits_without_verify"`
	MaxFilesTouched        int `json:"max_files_touched"`
}

// Delegation configures handoff to nj-agents skills.
//
// The budget field exists because of a real interaction: a delegated
// orchestrator such as /security-deep-review costs 15 to 30 agent calls
// for a mid-size diff, and the breaker counts every one. Without a
// separate envelope, a deep review invoked late in a session trips the
// breaker mid-review and the breaker is wrong to do so.
type Delegation struct {
	Enabled bool `json:"enabled"`
	// NJAgentsDir points at the nj-agents clone. Empty means discover it:
	// by env var, then by resolving an installed skill's symlink back to
	// its clone root, then by Orion's own managed clone.
	NJAgentsDir string `json:"nj_agents_dir,omitempty"`
	// NJAgentsRef pins the clone to a tag or branch. Empty clones the
	// default branch, which pins nothing and is not reproducible across
	// machines or across time.
	NJAgentsRef string `json:"nj_agents_ref,omitempty"`
	// ExtraToolCallsForReview is added to the tool budget while a delegated
	// review orchestrator is running. Generous by design: the failure mode
	// of too small a number is a review that cannot finish.
	ExtraToolCallsForReview int `json:"extra_tool_calls_for_review"`
	// DeepSecurityReviewWhen decides when to spend a deep review rather
	// than the standard pass. Cost is real, so this is risk-tiered rather
	// than always-on: "always", "high-risk", or "never".
	DeepSecurityReviewWhen string `json:"deep_security_review_when"`
	// HighRiskPaths mark a change as high risk regardless of size.
	HighRiskPaths []string `json:"high_risk_paths"`
}

// Slack configures the per-project channel.
//
// This needs a Slack app bot token, not an incoming webhook: a webhook is
// bound to one channel at creation and cannot create any. The two are
// independent, and both can be on.
type Slack struct {
	Enabled bool `json:"enabled"`
	// CreateChannelPerProject makes one channel per workspace. Channels
	// accumulate exactly as Jira projects do, and a bot cannot delete them;
	// `orion slack archive` is the cleanup.
	CreateChannelPerProject bool `json:"create_channel_per_project"`
	// ChannelPrefix namespaces Orion's channels so they sort together and
	// are obviously machine-made.
	ChannelPrefix string `json:"channel_prefix"`
	// Private channels by default: a workspace name can reveal an unreleased
	// project, and a public channel cannot be made private afterwards.
	Private bool `json:"private"`
	// InviteUsers are added to a channel Orion creates. Slack user IDs (U...)
	// or email addresses; emails need the users:read.email scope.
	//
	// Required for a PRIVATE channel to be of any use. The bot is the only
	// member of a channel it just made, and Slack shows a private channel to
	// nobody outside it -- not in the sidebar, not in search. Without this
	// Orion creates a "communication medium" that no human can see, and there
	// is no notification to tell them it happened.
	InviteUsers []string `json:"invite_users"`
}

// Budget caps what Orion spends over a rolling seven days.
//
// These are YOUR limits, not the provider's. Nothing reports how much of an
// Anthropic subscription's weekly allowance remains, so Orion accounts only
// for what it spends itself, from the cost and token figures each run
// returns. Zero means unlimited, deliberately: a budget nobody set should
// not be invented. That is the opposite of the circuit-breaker convention,
// where zero restores a default, because there "no limit" is never safe and
// here it is the honest default.
type Budget struct {
	WeeklyUSD    float64 `json:"weekly_usd"`
	WeeklyTokens int     `json:"weekly_tokens"`
	// PauseAtPercent are the checkpoints where a run stops for confirmation.
	PauseAtPercent []int `json:"pause_at_percent"`
}

// Tracker configures project-management provisioning.
type Tracker struct {
	// Enabled is opt-in, mirroring Slack. Without it, create_project_per_idea
	// (which defaults true) was read as "a tracker is required", so a bad
	// Jira token made Orion refuse to run at all. That field answers "if you
	// use a tracker, how", not "must you use one".
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	// ProjectKey binds to an existing project. When empty, Orion creates a
	// project per idea, which requires the CREATE_PROJECT global permission
	// and accumulates projects that a non-admin cannot delete.
	ProjectKey string `json:"project_key"`
	// CreatePerIdea is what the empty ProjectKey means, stated explicitly so
	// the intent is reviewable rather than inferred from an absent field.
	CreatePerIdea bool `json:"create_project_per_idea"`
	// ConfirmTreeBeforeCreate keeps the whole Epic/Story/Task tree behind one
	// human approval. This stays true even under auto_merge: a sandboxed
	// workspace can be deleted, a shared tracker cannot.
	ConfirmTreeBeforeCreate bool `json:"confirm_tree_before_create"`
	// AgentLabel is stamped on every issue Orion's agent creates, so
	// agent-filed work is separable from work a person filed. The tracker
	// equivalent of VCS.AgentAuthorName, and needed for the same reason: once
	// the two are mixed in a backlog, no query can pull them apart again.
	//
	// A LABEL rather than a reporter, deliberately. Jira's reporter must be a
	// real account; there is no way to file as a synthetic identity without
	// paying for a licence for it, and impersonating the human would be worse
	// than leaving it unmarked.
	AgentLabel string `json:"agent_label"`
	// QueueLabel marks an issue as work Orion should pick up.
	QueueLabel string `json:"queue_label"`
	// QueueOrder is the JQL ORDER BY clause for the queue.
	//
	// Priority first, then Rank. Rank alone would mean an urgent ticket
	// filed today waits behind everything already in the backlog; priority
	// alone gives no way to sequence the ties, which is most of them.
	// Together they read as "most important first, and within equal
	// importance the order I dragged them into".
	//
	// Configurable because priority is DISABLED by default on some
	// team-managed projects, and ordering by a field the project does not
	// expose is not a useful default to force on everyone.
	QueueOrder string `json:"queue_order"`
}

type Gates struct {
	RequirePlanBeforeEdit          bool `json:"require_plan_before_edit"`
	ProtectTestsDuringFix          bool `json:"protect_tests_during_fix"`
	ProductionRequiresAuth         bool `json:"production_requires_authorization"`
	BlockDirectPushToDefaultBranch bool `json:"block_direct_push_to_default_branch"`
}

type Paths struct {
	Intent    string   `json:"intent"`
	Specs     string   `json:"specs"`
	Plans     string   `json:"plans"`
	Evals     string   `json:"evals"`
	State     string   `json:"state"`
	Protected []string `json:"protected"`
	TestGlobs []string `json:"test_globs"`
}

type AutoMerge struct {
	Enabled         bool     `json:"enabled"`
	Environments    []string `json:"environments"`
	RequireChecks   []string `json:"require_checks"`
	RequireEvalPass float64  `json:"require_eval_pass_rate"`
	MinEvalCases    int      `json:"min_eval_cases"`
	ForbidPaths     []string `json:"forbid_paths"`
	MaxChangedFiles int      `json:"max_changed_files"`
}

// VCS describes the branch model.
//
// Two long-lived branches: main is the release branch and is protected,
// develop is the integration branch and the default base for pull requests.
// Named "develop" rather than "dev" so it does not collide with the dev
// ENVIRONMENT in the autonomy block; they are different things.
// Feature branches are cut from develop and merge back into it.
//
// Both are push-protected. Protecting only main would make the PR into
// develop optional, and an optional review gate is not a gate.
type VCS struct {
	Provider string `json:"provider"`
	// DefaultBranch is the release branch. Most protected, merged into only
	// from WorkBranch.
	DefaultBranch string `json:"default_branch"`
	// WorkBranch is the integration branch and the default PR base.
	WorkBranch string `json:"work_branch"`
	// ProtectedBranches may not be pushed to directly. Defaults to both
	// long-lived branches.
	ProtectedBranches []string `json:"protected_branches"`
	BranchPrefix      string   `json:"branch_prefix"`
	PRDraft           bool     `json:"pr_draft"`
	// AgentAuthorName is the git AUTHOR recorded for commits the agent makes.
	// The committer stays the human, so responsibility is unchanged and only
	// authorship is distinguished.
	//
	// Without this, an agent commit is indistinguishable from a hand-written
	// one: same name, same email, no marker. "Who wrote this" then has no
	// answer in the history, and a bad agent commit looks like your own work
	// during a bisect or a blame. That defeats the point of a committed,
	// traceable artifact chain.
	//
	// Empty disables the alias and commits are authored as you.
	AgentAuthorName string `json:"agent_author_name"`
	// AgentAuthorEmail defaults to the repo's configured user.email when
	// empty. Keeping YOUR address is deliberate: the email is what GitHub
	// matches commits against, so a made-up one shows every agent commit as
	// an unrecognised author with no avatar and no link, and quietly drops
	// them out of the contribution history of a repo you are accountable for.
	AgentAuthorEmail string `json:"agent_author_email"`
}

// Attribution instruments commits with whodunit (`dun`), which records what
// evidence exists about how each change was written.
//
// Distinct from VCS.AgentAuthorName, which only marks that a commit came from
// an Orion run. The alias is set before the work happens and cannot say which
// agent, which model, or how much of the diff the agent produced; dun answers
// that from the session transcripts afterwards.
type Attribution struct {
	Enabled bool `json:"enabled"`
	// AutoInstall lets `orion init` fetch dun through the platform's package
	// manager. Package-managed installs put the binary on PATH under the name
	// `dun`, which matters: the git hook resolves it by name at commit time,
	// and a dun that is not on PATH silently stamps every commit
	// `undetermined` -- read downstream as "no AI was used".
	AutoInstall bool `json:"auto_install"`
}

type Config struct {
	Version     int               `json:"version"`
	Limits      Limits            `json:"limits"`
	Gates       Gates             `json:"gates"`
	Paths       Paths             `json:"paths"`
	Autonomy    map[string]string `json:"autonomy"`
	AutoMerge   AutoMerge         `json:"auto_merge"`
	Budget      Budget            `json:"budget"`
	Slack       Slack             `json:"slack"`
	VCS         VCS               `json:"vcs"`
	Tracker     Tracker           `json:"tracker"`
	Delegation  Delegation        `json:"delegation"`
	Attribution Attribution       `json:"attribution"`

	// Root is the resolved project root. Not read from JSON.
	Root string `json:"-"`
	// Degraded is true when orion.json was missing or unparseable and
	// defaults are in force. Hooks surface this so a broken config is
	// visible rather than silently permissive.
	Degraded bool `json:"-"`
	// DegradedReason explains why, for the block message.
	DegradedReason string `json:"-"`

	// slackPrefixSet records whether channel_prefix was actually present in
	// the file. Without it an explicit "" is indistinguishable from absent,
	// so the prefix could be changed but never removed: the config said one
	// thing and the channel was named another.
	slackPrefixSet bool
}

// Defaults returns the shipped baseline. These are deliberately
// conservative: a project with no orion.json still gets real limits.
func Defaults() Config {
	return Config{
		Version: 1,
		Limits: Limits{
			MaxToolCalls:           400,
			MaxRepeatIdentical:     4,
			MaxConsecutiveFailures: 3,
			MaxSameCommandFailures: 3,
			MaxSessionMinutes:      90,
			MaxEditsWithoutVerify:  25,
			MaxFilesTouched:        60,
		},
		Gates: Gates{
			RequirePlanBeforeEdit:          false, // opt-in: too disruptive to force on an unconfigured repo
			ProtectTestsDuringFix:          true,
			ProductionRequiresAuth:         true,
			BlockDirectPushToDefaultBranch: true,
		},
		Paths: Paths{
			Intent: "docs/intent",
			Specs:  "specs",
			Plans:  "plans",
			Evals:  "evals",
			State:  ".orion/state",
			TestGlobs: []string{
				"**/test_*.py", "**/*_test.go", "**/*.test.ts", "**/*.test.tsx",
				"**/*.spec.ts", "**/tests/**", "**/__tests__/**",
			},
		},
		Autonomy: map[string]string{
			"dev": "gated_write", "staging": "gated_write", "production": "propose_only",
		},
		AutoMerge: AutoMerge{
			Enabled: false, RequireEvalPass: 0.95, MinEvalCases: 20, MaxChangedFiles: 20,
		},
		VCS: VCS{
			Provider:          "github",
			DefaultBranch:     "main",
			WorkBranch:        "develop",
			ProtectedBranches: []string{"main", "develop"},
			BranchPrefix:      "orion/",
			AgentAuthorName:   "orion_agent",
		},
		Budget:      Budget{PauseAtPercent: []int{50, 75, 90, 95}},
		Attribution: Attribution{Enabled: true, AutoInstall: true},
		Slack: Slack{
			Enabled:                 false,
			CreateChannelPerProject: true,
			ChannelPrefix:           "orion-",
			Private:                 true,
		},
		Tracker: Tracker{
			Enabled:                 false,
			Provider:                "jira",
			AgentLabel:              "orion_agent",
			QueueLabel:              "ORION",
			QueueOrder:              "priority DESC, Rank ASC",
			CreatePerIdea:           true,
			ConfirmTreeBeforeCreate: true,
		},
		Delegation: Delegation{
			Enabled:                 true,
			ExtraToolCallsForReview: 200,
			DeepSecurityReviewWhen:  "high-risk",
			HighRiskPaths: []string{
				"**/auth/**", "**/security/**", "**/crypto/**",
				"**/payment*/**", "**/migrations/**",
				"**/*deserial*", "**/Dockerfile", "**/*.tf",
			},
		},
	}
}

// FindRoot walks up from start looking for orion.json, then for .git.
// Returns the first directory containing either.
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "orion.json")); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no orion.json or .git found walking up from " + start)
		}
		dir = parent
	}
}

// Load reads orion.json from root, overlaying it on Defaults.
// A missing or malformed file yields defaults with Degraded set; it never
// yields an error that would let a caller skip enforcement.
func Load(root string) Config {
	cfg := Defaults()
	cfg.Root = root

	b, err := os.ReadFile(filepath.Join(root, "orion.json"))
	if err != nil {
		cfg.Degraded = true
		cfg.DegradedReason = "orion.json not found; shipped defaults in force"
		return cfg
	}
	// Strip the _comment_* keys before unmarshalling so the template
	// stays self-documenting without tripping strict consumers.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		cfg.Degraded = true
		cfg.DegradedReason = "orion.json is not valid JSON (" + err.Error() + "); defaults in force"
		return cfg
	}
	for k := range raw {
		if strings.HasPrefix(k, "_comment") || k == "$schema" {
			delete(raw, k)
		}
	}
	// Note whether channel_prefix was actually written, before defaults are
	// applied. Without this an explicit "" is indistinguishable from absent
	// and the prefix can be changed but never removed.
	prefixSet := false
	if sb, ok := raw["slack"]; ok {
		var probe map[string]json.RawMessage
		if json.Unmarshal(sb, &probe) == nil {
			_, prefixSet = probe["channel_prefix"]
		}
	}

	clean, _ := json.Marshal(raw)
	if err := json.Unmarshal(clean, &cfg); err != nil {
		fresh := Defaults()
		fresh.Root = root
		fresh.Degraded = true
		fresh.DegradedReason = "orion.json failed to decode (" + err.Error() + "); defaults in force"
		return fresh
	}
	cfg.Root = root
	cfg.slackPrefixSet = prefixSet
	normalize(&cfg)
	return cfg
}

// normalize repairs zero values that would disable a control. A limit of
// 0 in JSON is indistinguishable from absent, and "unlimited" is never a
// safe reading for a circuit breaker, so 0 restores the default.
func normalize(c *Config) {
	d := Defaults()
	if c.Limits.MaxToolCalls <= 0 {
		c.Limits.MaxToolCalls = d.Limits.MaxToolCalls
	}
	if c.Limits.MaxRepeatIdentical <= 0 {
		c.Limits.MaxRepeatIdentical = d.Limits.MaxRepeatIdentical
	}
	if c.Limits.MaxConsecutiveFailures <= 0 {
		c.Limits.MaxConsecutiveFailures = d.Limits.MaxConsecutiveFailures
	}
	if c.Limits.MaxSameCommandFailures <= 0 {
		c.Limits.MaxSameCommandFailures = d.Limits.MaxSameCommandFailures
	}
	if c.Limits.MaxSessionMinutes <= 0 {
		c.Limits.MaxSessionMinutes = d.Limits.MaxSessionMinutes
	}
	if c.Limits.MaxEditsWithoutVerify <= 0 {
		c.Limits.MaxEditsWithoutVerify = d.Limits.MaxEditsWithoutVerify
	}
	if c.Limits.MaxFilesTouched <= 0 {
		c.Limits.MaxFilesTouched = d.Limits.MaxFilesTouched
	}
	if c.Paths.State == "" {
		c.Paths.State = d.Paths.State
	}
	if c.Paths.Plans == "" {
		c.Paths.Plans = d.Paths.Plans
	}
	if len(c.Paths.TestGlobs) == 0 {
		c.Paths.TestGlobs = d.Paths.TestGlobs
	}
	if c.VCS.DefaultBranch == "" {
		c.VCS.DefaultBranch = d.VCS.DefaultBranch
	}
	if c.VCS.BranchPrefix == "" {
		c.VCS.BranchPrefix = d.VCS.BranchPrefix
	}
	// An explicitly empty channel_prefix means "no prefix", not "use the
	// default". Coercing it back made the setting impossible to turn off:
	// you could change the prefix but never remove it, and the file said one
	// thing while the channel was named another. The generated orion.json
	// writes "orion-" explicitly, so new repos still get it.
	if !c.slackPrefixSet {
		c.Slack.ChannelPrefix = d.Slack.ChannelPrefix
	}
	if len(c.Budget.PauseAtPercent) == 0 {
		c.Budget.PauseAtPercent = d.Budget.PauseAtPercent
	}
	if c.VCS.WorkBranch == "" {
		c.VCS.WorkBranch = d.VCS.WorkBranch
	}
	if len(c.VCS.ProtectedBranches) == 0 {
		// Falling back to protecting nothing would silently remove the
		// control, so an empty list restores both long-lived branches.
		c.VCS.ProtectedBranches = []string{c.VCS.DefaultBranch, c.VCS.WorkBranch}
	}
	if c.Delegation.ExtraToolCallsForReview <= 0 {
		c.Delegation.ExtraToolCallsForReview = d.Delegation.ExtraToolCallsForReview
	}
	if c.Delegation.DeepSecurityReviewWhen == "" {
		c.Delegation.DeepSecurityReviewWhen = d.Delegation.DeepSecurityReviewWhen
	}
	if len(c.Delegation.HighRiskPaths) == 0 {
		c.Delegation.HighRiskPaths = d.Delegation.HighRiskPaths
	}
}

// StateDir returns the absolute path to the state directory.
func (c Config) StateDir() string {
	if filepath.IsAbs(c.Paths.State) {
		return c.Paths.State
	}
	return filepath.Join(c.Root, c.Paths.State)
}
