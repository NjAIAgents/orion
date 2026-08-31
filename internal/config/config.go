// Package config loads orion.json from the project root and supplies
// defaults for anything absent. Every value here is a hard limit enforced
// by a hook, so an absent or malformed config must never silently widen
// a control: parse failures fall back to defaults and are reported.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// MaxConcurrentChildren caps how many subagents supervisor.Fan runs at
	// once. Low by default: unbounded fan-out against a rate-limited API
	// converts a queue into a stampede (OR-162 is what misreading this limit
	// costs).
	MaxConcurrentChildren int `json:"max_concurrent_children"`

	// MaxConcurrentTickets caps how many tickets a watcher works at the same
	// time. Unlike the limits above it does not bound one agent's behaviour;
	// it bounds how many agents exist.
	//
	// Four by default. There is no maximum: a larger value is confirmed at
	// the point it is set, not refused.
	//
	// It was two first, deliberately: every hazard concurrency introduces --
	// concurrent git against one shared clone, a budget checkpoint sailed past
	// by runs already in flight, N sessions hitting one rate limit, N tickets
	// picked that all edit the same files -- is invisible at one and obvious
	// at two, and diagnosing it at two is cheap. The rule was "prove it at
	// two, then raise it", and two has now been proven across a full release
	// (v0.8.0 and the v0.8.1 queue), so this is that raise rather than a
	// change of mind about the hazards.
	//
	// Four as a default rather than a maximum: there is no maximum. A value
	// above ConcurrencyWarnAbove is confirmed rather than refused, because the
	// hazards scale with a machine, a repository and a rate limit that Orion
	// cannot measure from here.
	//
	// Note this multiplies with MaxConcurrentChildren above rather than
	// capping it: four tickets each fanning out to subagents is a different
	// load than four processes. If a stampede shows up after this change,
	// that product is the first thing to look at.
	MaxConcurrentTickets int `json:"max_concurrent_tickets"`
}

// ConcurrencyWarnAbove is where `orion config limits` stops accepting a value
// silently and asks the operator to confirm it.
//
// A confirmation, not a ceiling. The old hard cap of five refused a number the
// operator had deliberately chosen, on a machine Orion cannot measure: the
// hazards below scale with the machine, the repository and the rate limit, and
// none of those is knowable from here. Refusing was Orion asserting it knew
// better about a thing it cannot see.
//
// Ten because that is where the costs stop being linear. Conflicts grow with
// the SQUARE of the number in flight -- six pairs at four, forty-five at ten --
// and on this project one pair in six collided, so ten is roughly where a
// batch would eject most of itself. It is a threshold for a conversation, not
// a verdict.
const ConcurrencyWarnAbove = 10

// ConcurrentTickets is the cap actually enforced: the configured value, with
// zero meaning the shipped default.
//
// No upper clamp. There used to be one, and it produced a file that disagreed
// with behaviour: a config saying 40 while the watcher ran 5, with nothing in
// either place explaining the gap. A configured number is now honoured, and
// the place to argue about it is where it is SET -- `orion config limits`
// asks for confirmation above ConcurrencyWarnAbove -- rather than silently at
// every read.
//
// Zero still means the default rather than unlimited. An absent value must
// never widen a control, which is this package's rule for every field.
func (l Limits) ConcurrentTickets() int {
	n := l.MaxConcurrentTickets
	if n <= 0 {
		n = Defaults().Limits.MaxConcurrentTickets
	}
	return n
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
	// InheritOperatorConfig names the stages or actors whose runs get the
	// OPERATOR's own Claude Code configuration -- their plugins, their MCP
	// servers, their subagents -- instead of the curated directory Orion
	// builds (see internal/agentcfg). An entry matches either a stage name
	// or an actor id.
	//
	// EMPTY BY DEFAULT, and that is the whole point: what a run can do is
	// Orion's decision, and an operator who genuinely wants a plugin in the
	// loop says so here, once, where the choice is visible and recorded in
	// the event log at run start.
	//
	// Here rather than in the global agents.json, which decisions/0005 makes
	// the default home for actor-level settings, because this is not only an
	// actor-level setting: it names STAGES too, and which capabilities a
	// stage needs is a property of the repository being worked -- a
	// design-heavy frontend repo wanting a Figma MCP in its build stage says
	// nothing about the next repository on the same machine.
	InheritOperatorConfig []string `json:"inherit_operator_config,omitempty"`
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
	// MergeApprovers may approve a merge from Slack. A Slack user ID (U...),
	// a username, a display name or an email address.
	//
	// The approval request MENTIONS whoever it can resolve, because that is
	// the only form Slack notifies on -- a name in the message text is styled
	// like any other word and reaches nobody. Resolving a name needs
	// users:read and an email needs users:read.email, the same scope
	// InviteUsers notes above; without them the request still sends and still
	// names the person, and the run says the mention was lost. An ID needs no
	// scope at all, so it is the form that always works.
	//
	// EMPTY MEANS NOBODY, never everybody. Being in a channel is not
	// authority: a project room contains people with no idea what they are
	// approving, and a gate any member can satisfy is decoration. Defaulting
	// to open would also mean the first repository someone forgot to
	// configure merges on a stranger's thumbs up.
	MergeApprovers []string `json:"merge_approvers"`
	// RequireApproval gates merging on a Slack approval. With it off, Orion
	// reports that checks pass and waits for a human to merge on GitHub --
	// which is the safe default and needs no extra OAuth scopes.
	RequireApproval bool `json:"require_approval"`
	// Mention are Slack user IDs (U...) to @-mention when a message needs
	// somebody to act. Empty falls back to InviteUsers.
	//
	// ONLY on messages that require action: blocked, failed, and approval
	// requests. Mentioning on every routine event is how a channel gets
	// muted, and a muted channel delivers nothing at all -- so a mention
	// attached to good news costs the delivery of the bad.
	Mention []string `json:"mention"`
}

// CI is the build gate and what Orion does when it goes red.
type CI struct {
	// AutoFix sends a failing build back to an agent on the same branch
	// rather than stopping for a person.
	//
	// Off by default. It spends money without being asked, and on a
	// repository whose tests are flaky it will spend it on nothing.
	AutoFix bool `json:"auto_fix"`
	// MaxFixAttempts bounds that loop. Zero means the built-in 3.
	//
	// A ceiling is not the only brake and not the most important one --
	// an identical repeated failure stops the loop immediately, because an
	// agent that gets back a byte-identical error has learned nothing and
	// spending the remaining attempts proves only that it can fail the same
	// way three times.
	MaxFixAttempts int `json:"max_fix_attempts"`
	// RequireUpToDate refuses to merge a branch whose base has moved since
	// its checks ran.
	//
	// ON by default, which is unusual for a gate here and deliberate: Orion
	// is the thing performing the merge, so merging on a verdict that no
	// longer describes the code is a correctness failure rather than a
	// preference. Two tickets worked in parallel can each pass alone and
	// break the trunk together, with every signal green.
	//
	// GitHub's own "require branches to be up to date" does this and cannot
	// be relied on: it is unavailable for private repositories on the free
	// plan, and with protection off `gh` reports a stale branch as CLEAN.
	// One local `git merge-base --is-ancestor` has neither limitation.
	RequireUpToDate bool `json:"require_up_to_date"`
}

// Collect configures the pass that reconciles a pull request after CI.
type Collect struct {
	// AutoRebase replays a branch that is BEHIND its base and merges
	// CLEANLY onto that base, force-pushes it with a lease, and lets the
	// checks re-run against what would actually be merged.
	//
	// ON by default, and the only automatic rewrite of a branch in Orion.
	// It is safe to default on because it does not choose anything: git has
	// already said the merge is clean, so the rebase has one possible
	// result. Contrast automatic conflict resolution, which decides.
	//
	// The alternative is not "a human reviews it" -- require_up_to_date
	// makes every merge invalidate every other open pull request, so the
	// alternative is a person typing the same three commands once per merge
	// per open branch, which grows with the square of the queue.
	//
	// Turn it OFF for a repository you do not own: however safe the rewrite,
	// a force-push to somebody else's branch is theirs to authorise.
	AutoRebase bool `json:"auto_rebase"`

	// BatchIntegration lands the ready branches as ONE set (OR-236): they are
	// merged into an ephemeral ref, that ref is tested once, and the whole
	// batch merges. Branches are never rewritten, so with this on there is no
	// rebase, no force-push and no landing queue.
	//
	// OFF by default, and deliberately not defaulted on the way AutoRebase is.
	// AutoRebase is safe to default because it decides nothing: git has
	// already said the merge is clean. This decides what lands and in what
	// order, and a mistake mis-merges or strands every branch in flight at
	// once rather than one branch at a time. It is turned on per repository,
	// watched for the first few batches, and only then left alone.
	//
	// With it off, nothing below is reached and the per-branch path is
	// unchanged, so enabling it is reversible by setting it back.
	//
	// There is deliberately no separate batch size. A batch can only hold
	// branches that finished, and no more can finish than
	// limits.max_concurrent_tickets allowed to run -- so a second number
	// could only ever disagree with the first about the same thing.
	BatchIntegration bool `json:"batch_integration,omitempty"`
}

// Budget caps what Orion spends over a rolling seven days.
//
// NOT the plan limit. That one is now read from the runs themselves: the CLI
// reports its own rate_limit_info on every run, so Orion knows when the
// account is refused and exactly when the window resets, and a watcher
// sleeps until then. Nothing here needs to approximate it.
//
// What remains is a limit YOU choose, for reasons the plan knows nothing
// about -- a cap on a project, a spend you want to notice before the plan
// would. Zero means unlimited, and zero is the right default: a budget
// nobody set should not be invented, and an invented ceiling stops work for
// a reason that was never true.
//
// The percentage in `claude /usage` is not available here. That view fetches
// it from an API this process cannot reach, and it is in no file on disk, so
// Orion cannot warn at 80%. It gets a yes/no and a reset time instead --
// which is the more actionable half, since "when can I try again" is the
// only question a refusal actually raises.
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
	// WorkBranch is the integration branch and the default PR base. It must
	// differ from DefaultBranch: see Validate.
	WorkBranch string `json:"work_branch"`
	// AllowReleaseBranchMerges waives the rule that WorkBranch and
	// DefaultBranch are different branches.
	//
	// The rule exists because Orion's responsibility ends when work merges
	// into the integration branch; promoting that to the release branch is a
	// human decision about what constitutes a release. Collapse the two and
	// Orion merges agent output straight into the release branch -- not as a
	// bug, but as configured, which is worse.
	//
	// A repository with genuinely one branch and no release process is a
	// legitimate case, so this stays possible. It is a named key rather than
	// a reachable side effect of editing one string, because giving up the
	// human promotion step should take a sentence that says so.
	AllowReleaseBranchMerges bool `json:"allow_release_branch_merges"`
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
	// AgentAuthorEmail is the address recorded with that name, and it is the
	// field that actually decides what GitHub displays: GitHub matches
	// commits to accounts by EMAIL and ignores the name entirely. Setting
	// only agent_author_name therefore changes `git log` and changes nothing
	// on the web, which is exactly what happened on the first real run --
	// the commits were authored orion_agent and GitHub still said the
	// account owner had made them.
	//
	// The default is a noreply address, because the email is the field that
	// actually decides attribution: GitHub matches commits to accounts by
	// ADDRESS and ignores the name entirely. Sharing the owner's address
	// means the web UI keeps saying the owner made the change however the
	// name fields read, which is what happened on the first real run.
	//
	// orionbot@users.noreply.github.com carries no account id, so it
	// resolves to nobody and the commit displays as a plain "orionbot".
	// Two consequences, both intended:
	//   - these commits leave the owner's contribution graph, which is
	//     correct, since the owner did not write them;
	//   - a branch rule demanding a verified or allowlisted committer email
	//     will reject them, and the fix is a real bot account.
	//
	// For a genuine avatar and profile, create a GitHub account for the bot
	// and use its own ID+name@users.noreply.github.com here.
	//
	// Setting this to the owner's real address restores the old behaviour:
	// the alias then lives only in `git log`, `git blame` and a bisect.
	AgentAuthorEmail string `json:"agent_author_email"`
	// MergeStrategy is how an approved pull request lands: "rebase",
	// "squash" or "merge". Empty means rebase.
	//
	// This decides whether the agent's authorship survives onto the trunk,
	// which is not obvious and was got wrong here first time round.
	//
	//	squash  collapses the branch into ONE new commit, authored by the
	//	        pull request's author -- which is whoever's token opened it,
	//	        i.e. you. Every orionbot commit vanishes from the trunk's
	//	        history. Tidiest log, worst attribution.
	//	merge   keeps every commit, author intact, plus a merge commit.
	//	        Best attribution, noisiest history.
	//	rebase  replays each commit onto the base, preserving its author.
	//	        Linear history AND orionbot attribution, which is why it is
	//	        the default.
	//
	// The cost of rebase is that the branch's incremental commits and its
	// decision records all land on the trunk rather than being collapsed.
	// That is a real downside; it is chosen because "who wrote this" is
	// harder to reconstruct later than "which commits belong together".
	MergeStrategy string `json:"merge_strategy"`
	// RequireUpToDate controls GitHub's own "require branches to be up to
	// date before merging" (required_status_checks.strict), applied by
	// `orion protect`.
	//
	// ON by default so an operator who never touches this setting keeps
	// today's behaviour. It is the SERVER-SIDE twin of CI.RequireUpToDate:
	// that gate runs one local `git merge-base --is-ancestor` and can only
	// warn a human, who may merge anyway; this refuses the merge outright.
	// Turning this off does not remove that gate and does not weaken it --
	// with strict off it becomes the only signal that a branch is stale,
	// which makes it matter MORE, not less.
	//
	// The cost this trades away is real: with strict on, a queue of N ready
	// pull requests turns every merge into a rebase of the other N-1, and
	// each rebase re-runs the full CI matrix -- a cost that scales with
	// queue depth while the benefit (catching a genuine semantic conflict)
	// does not. That tradeoff is the operator's to make, not `orion
	// protect`'s to assume.
	RequireUpToDate bool `json:"require_up_to_date"`
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

// QA is the independent verification stage: an agent that derives test
// cases from the ticket's acceptance criteria, writes the tests the
// implementer did not, runs them, and reports what failed.
//
// Enabled is a POINTER because absent and false mean different things here.
// Absent is "run it" -- verification a project never asked to switch off
// should not be silently missing -- while an explicit false is a project
// saying it does not want the spend, which a docs repository is right to
// say.
type QA struct {
	Enabled *bool `json:"enabled"`
	// MaxRounds bounds the findings-fix-reverify exchange. Zero means the
	// built-in default. Past it Orion escalates to a person rather than
	// paying two agents to argue: QA never blocks on its own authority, so
	// an unbounded loop is the only way this stage could stop a run, and
	// it would do it by spending.
	MaxRounds int `json:"max_rounds"`
	// E2EBaseURL is the explicit non-production target an end-to-end run may
	// point at. EMPTY MEANS NO E2E, never "guess one": nj-agents
	// CONVENTIONS-testing §T3 blocks an e2e execution without an explicit
	// non-prod URL, and a suite that quietly found production is the failure
	// that rule exists to prevent. Without it the stage still authors and
	// runs unit and integration tests, and says that is what it did.
	E2EBaseURL string `json:"e2e_base_url,omitempty"`
}

// On reports whether the stage runs. See the Enabled comment: absent is on.
func (q QA) On() bool { return q.Enabled == nil || *q.Enabled }

// Rounds is MaxRounds with the default applied.
func (q QA) Rounds() int {
	if q.MaxRounds > 0 {
		return q.MaxRounds
	}
	return 2
}

// Agent overrides how one actor is displayed, and which model and effort it
// runs on. Global, not per-project: see LoadAgents. Keyed by the STABLE
// actor identifier ("implementer"), never by the display name, which is the
// only thing this file exists to let someone change.
//
//	{
//	  "implementer": { "name": "Alex", "designation": "backend engineer", "effort": "high" }
//	}
//
// Any field left out keeps the shipped default, so renaming one agent does
// not mean restating its model. `orion config agents` writes this file
// interactively -- a numbered menu for model and effort, free text only for
// the name -- and `orion config agents --reset [id...]` clears it by hand.
// Name is a pointer so that an explicit "" is distinguishable from absent.
// They mean opposite things: absent keeps the shipped name, while "" clears
// it and the actor renders as its job title alone -- which is how a team
// that does not want personas turns them off.
type Agent struct {
	Name        *string `json:"name"`
	Designation string  `json:"designation,omitempty"`
	Model       string  `json:"model,omitempty"`
	// Effort is the `claude --effort` value this actor runs at: one of
	// low, medium, high, xhigh, max. Empty leaves the CLI's own default in
	// force. Not validated here -- `orion config agents` only ever writes
	// one of the five values because it is a select menu, never free text,
	// so a typo cannot reach this field via that path. A hand-edited
	// agents.json with an unrecognized value is instead rejected by the
	// `claude` binary at run time, with `claude`'s own error naming the
	// valid set.
	Effort string `json:"effort,omitempty"`
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
	CI          CI                `json:"ci"`
	QA          QA                `json:"qa"`
	Collect     Collect           `json:"collect"`
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
	// vcsRequireUpToDateSet records whether vcs.require_up_to_date was
	// actually present in the file, so `orion protect` can say whether the
	// value it is enforcing came from orion.json or from the shipped
	// default -- see VCSRequireUpToDateSource.
	vcsRequireUpToDateSet bool
}

// VCSRequireUpToDateSource describes where VCS.RequireUpToDate's value came
// from, for `orion protect` to state at the moment it enforces the setting
// rather than leaving it to be discovered later from a merge refusal.
func (c Config) VCSRequireUpToDateSource() string {
	if c.vcsRequireUpToDateSet {
		return "orion.json (vcs.require_up_to_date)"
	}
	return "default; not set in orion.json"
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
			MaxConcurrentChildren:  2,
			MaxConcurrentTickets:   4,
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
			AgentAuthorName:   "orionbot",
			AgentAuthorEmail:  "orionbot@users.noreply.github.com",
			RequireUpToDate:   true,
		},
		Budget: Budget{PauseAtPercent: []int{50, 75, 90, 95}},
		// On by default: Orion performs the merge, so merging on a verdict
		// that no longer describes the code is a correctness failure.
		CI: CI{RequireUpToDate: true},
		// On by default too, and for the same reason turned inside out: the
		// gate above is what makes every merge invalidate every other open
		// branch, so shipping it without the mechanical half leaves a person
		// typing three commands per merge per branch.
		Collect:     Collect{AutoRebase: true},
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

// AgentsPath is where the global agent roster lives. One file, shared by
// every project on this machine, because who the implementer is and what
// it runs on is an operator preference, not something that should differ
// by checkout -- the reason this moved out of orion.json (OR-132). home is
// workspace.Home() (ORION_HOME, defaulting to ~/.orion); not imported here
// to avoid a cycle, so callers pass it in.
func AgentsPath(home string) string { return filepath.Join(home, "agents.json") }

// LoadAgents reads the global roster. A missing file is not an error --
// every actor just keeps its shipped default, the same as a project that
// has never run `orion config agents`.
func LoadAgents(home string) (map[string]Agent, error) {
	b, err := os.ReadFile(AgentsPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Agent{}, nil
	}
	if err != nil {
		return nil, err
	}
	agents := map[string]Agent{}
	if err := json.Unmarshal(b, &agents); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", AgentsPath(home), err)
	}
	return agents, nil
}

// SaveAgents writes the global roster, pretty-printed and sorted by key so
// a re-run of the wizard produces a stable diff. Unlike orion.json this
// file carries no human-authored comments to preserve, so a full
// marshal/write is the right tool here -- no adopt.SetBlock text surgery
// needed, because there is nothing else in the file to disturb.
func SaveAgents(home string, agents map[string]Agent) error {
	// 0o700/0o600 rather than importing workspace.HomeDirMode/PrivateFileMode
	// for two constants: workspace does not import config today, and this
	// package staying leaf-level is worth more than not repeating two
	// numbers workspace already uses for the same home directory.
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(agents, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(AgentsPath(home), b, 0o600)
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
	// Same probe for vcs.require_up_to_date, so "explicitly false" can be
	// told apart from "absent" even though both unmarshal to the same bool.
	requireUpToDateSet := false
	if vb, ok := raw["vcs"]; ok {
		var probe map[string]json.RawMessage
		if json.Unmarshal(vb, &probe) == nil {
			_, requireUpToDateSet = probe["require_up_to_date"]
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
	cfg.vcsRequireUpToDateSet = requireUpToDateSet
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
	if c.Limits.MaxConcurrentChildren <= 0 {
		c.Limits.MaxConcurrentChildren = d.Limits.MaxConcurrentChildren
	}
	// Defaulted when absent, and NOT clamped when present. A configured
	// number is honoured: the argument about whether it is wise happens where
	// it is set, not silently on every read.
	if c.Limits.MaxConcurrentTickets <= 0 {
		c.Limits.MaxConcurrentTickets = d.Limits.MaxConcurrentTickets
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

// Validate refuses a configuration Orion must not act on.
//
// Only one thing is refused today: a branch model with no integration
// branch. Orion merges agent output into vcs.work_branch, and the whole
// safety of that rests on work_branch not being the branch a release is cut
// from. Set them equal and every merge Orion performs is a release nobody
// authorised -- reported accurately, which is what makes it hard to notice.
//
// The rule was documented in the provision package and encoded in a default
// value, and enforced nowhere; a default is not a constraint. This is the
// constraint.
func (c Config) Validate() error {
	if c.VCS.WorkBranch == "" || c.VCS.WorkBranch != c.VCS.DefaultBranch {
		return nil
	}
	if c.VCS.AllowReleaseBranchMerges {
		return nil
	}
	return fmt.Errorf(
		"vcs.work_branch and vcs.default_branch are both %q, so Orion would merge "+
			"agent work straight into the release branch.\n"+
			"  Agent work lands on the integration branch; a human promotes it to the "+
			"release branch. Collapsing the two removes the human from the release decision.\n"+
			"  Fix: set vcs.work_branch to an integration branch (\"develop\" is the "+
			"default). `orion init` creates it and switches to it when it does not exist yet.\n"+
			"  Or, if this repository genuinely has one branch and no release process, "+
			"set vcs.allow_release_branch_merges: true to say so on purpose.",
		c.VCS.WorkBranch)
}

// ReleaseBranchWaiver describes what an active
// vcs.allow_release_branch_merges is giving up, or "" when it is not in
// force. An override nobody is reminded of is an override nobody remembers
// making.
func (c Config) ReleaseBranchWaiver() string {
	if !c.VCS.AllowReleaseBranchMerges || c.VCS.WorkBranch != c.VCS.DefaultBranch {
		return ""
	}
	return fmt.Sprintf("vcs.allow_release_branch_merges is on: %q is both the "+
		"integration and the release branch, so Orion merges agent work directly "+
		"into the release branch and there is no human promotion step left.",
		c.VCS.WorkBranch)
}

// StateDir returns the absolute path to the state directory.
func (c Config) StateDir() string {
	if filepath.IsAbs(c.Paths.State) {
		return c.Paths.State
	}
	return filepath.Join(c.Root, c.Paths.State)
}
