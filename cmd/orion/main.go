// Command orion is the AI-native SDLC orchestrator.
//
// It runs in two modes that share one config:
//
//	hook mode  invoked by Claude Code on every matching tool call, enforcing
//	           limits from inside the session
//	CLI mode   provisions isolated workspaces and supervises `claude -p`
//	           runs from outside, enforcing what a hook cannot: wall clock,
//	           turn caps, and hard kill
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/creds"
	"github.com/orion-sdlc/orion/internal/discovery"
	"github.com/orion-sdlc/orion/internal/doctor"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/hook"
	"github.com/orion-sdlc/orion/internal/lessons"
	"github.com/orion-sdlc/orion/internal/njagents"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/provision"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/report"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/state"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/work"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Version is set at build time: -ldflags "-X main.Version=$(git describe)".
var Version = "dev"

const usage = `orion - AI-native SDLC orchestrator

CONFIGURATION
  orion config                interactive setup for Jira, Slack and webhooks
  orion config show           what is set, where it came from, secrets masked
  orion config path           print the config file location
  orion config agents         interactive: name, model and effort per agent, by menu
  orion config limits         show the circuit breakers and where each value came from
  orion config limits KEY N   set one, e.g. limits max_concurrent_tickets 3
  orion config collect        show how work lands, and what each switch costs
  orion config collect K B    set one, e.g. collect batch_integration true
                              (global -- one roster, shared by every project)
  orion config agents --reset [id...]   reset one, some, or every agent to shipped defaults

ADOPT AN EXISTING REPO
  orion init [--plan-gate]    config, artifact dirs and hooks, idempotent

STARTING SOMETHING NEW
  orion new "<idea>"          interview you about the idea, then create the
                              tracker project that orion plan designs from
                              (interactive; creates no workspace)

WORKSPACES
  orion answer <id>           resolve the open questions blocking a workspace
  orion ls                    list workspaces
  orion open <id>             print a workspace path (use with cd)
  orion rm <id>               remove a workspace

RUNNING
  orion provision <id>        create the remote repo, branches, and tracker
  orion plan <KEY>            design a provisioned tracker project: workspace
                              first, then the roster and cost shape
                              (--dry-run prints it all and spends nothing)
  orion run <id> [--stage S]  supervise a sandboxed claude run in a workspace
  orion status                show this repo: branch, hooks, Jira, Slack, spend
  orion status <id>           show stage, breaker state and last run
  orion work <KEY> [KEY...]   work tickets, in the order given
                              (--verbose adds the agent's tool-call lines;
                              they are in the event log either way)
  orion queue                 what the watcher would pick up, in order (read-only)
  orion queue add <KEY>...    queue tickets: keys and inclusive ranges, e.g.
                              OR-100 OR-140..OR-145 (--project KEY; --reset to
                              requeue a failed ticket and return it to To Do)
  orion queue remove <KEY>... take tickets out of the queue; status and
                              fixVersion are left alone
  orion routes                which marker sends a ticket to which actor, and
                              which actors are reached another way (read-only)
  orion watch [PROJECT...]    run the queue by itself: work, collect, repeat
                              (--once, --interval S, --max-jobs N, --dry-run,
                              --verbose for the full tool-call stream)
  orion collect [KEY...]      finish tickets awaiting CI: close, refresh, prune
                              (--dry-run for verdicts only, --no-prune, --no-fix)
  orion protect               require the checks CI actually runs, and that
                              branches be up to date before merging
                              (--branch B, --dry-run; run once CI has run once)
  orion repos                 project key -> repository, as adoption recorded it
  orion repos unbind <KEY>    forget one mapping
  orion sandbox               where agents actually worked: clones and worktrees
  orion sandbox <KEY>         one ticket's worktree: branch, commits, dirt
  orion sandbox <KEY> --code  open it in VS Code
  orion sandbox <KEY> --shell start a shell in it
  orion sandbox <KEY> --path  print the path only (use with cd)
  orion sandbox prune         remove worktrees whose branch is merged and clean
  orion slack test [KEY]      send a real message and report exactly what breaks

GUARDRAILS
  orion doctor [--fix]        preflight: tools, auth, sandbox, config
                              --fix fetches nj-agents if it is missing
  orion reset --session <id>  clear a tripped breaker after human review
  orion fix start|end         mark a bug fix, protecting the failing test
  orion settle <KEY>          unstick a ticket's worktree: report what is
                              blocking its branch and commit it, so collect can
                              rebase again (--dry-run to look first)

FOR AN AGENT INSIDE A RUN
  orion explore "<question>"  answer one question about this repository in a
                              subagent's context, citing the paths (--repo DIR)

DEPENDENCIES
  orion njagents status       where nj-agents is, which commit, how stale
  orion njagents update       fast-forward Orion's own clone, if it has one
  orion njagents install      wire Orion's clone into a dir (only if no global)

MONITORING
  orion changelog --version vX.Y.Z  collate .changelog.d/ fragments into CHANGELOG.md
  orion changelog [--version vX.Y.Z]  no fragments: generate from commits (nj-agents)
  orion report [KEY] [--since 7d]  digest: failures, workspaces, budget, usage
  orion report --notify       also send it to ORION_NOTIFY_WEBHOOK (Slack)
  orion logs <KEY> [-f]       what Orion is doing, live (FCIA or FCIA-6)
  orion logs <KEY> --actor implementer   only that role's lines
  orion logs <KEY> --transcript   the raw agent output instead
  orion aiops <KEY>           read a FINISHED run's event log and report what is
                              worth filing, with draft tickets. Proposes only --
                              it never creates anything (--no-agent for rules only)

BUDGET (rolling 7 days, your limit, not your plan's)
  orion budget status         spend, tokens and the next checkpoint
  orion budget ack [pct]      confirm a checkpoint and continue

MEMORY (shared across every project)
  orion lessons add "<text>"  record a correction so it is not repeated
  orion lessons list          show what Orion has learned, and its scope
  orion lessons pending       lessons Orion proposed, awaiting your yes or no
  orion lessons approve <sig> record a proposed lesson
  orion lessons reject <sig>  discard one, and never propose it again
  orion lessons retire "<t>"  stop injecting a lesson

HOOKS (invoked by Claude Code, not by hand)
  orion hook breaker          loop, failure and budget circuit breaker
  orion hook gate             shell command gate: prod deploy, push safety
  orion hook shield           file write gate: protected paths, tests, plan
  orion hook session-start    reset per-session counters

  orion version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(64)
	}

	// Read once, for every subcommand, before anything prints. --verbose is
	// a property of the terminal rather than of one command: `orion work`
	// and `orion watch` emit the same stream through the same renderer, and
	// a flag that only one of them honoured would mean the same run reads
	// differently depending on which one started it (OR-217).
	ui.SetVerbose(hasFlag(os.Args[1:], "--verbose"))

	// Slack resolves its token through this hook rather than importing
	// workspace directly, which would create an import cycle via notify.
	slack.SetResolver(func() string {
		return creds.Get(workspace.Home(), creds.SlackToken)
	})
	notify.SetWebhookResolver(func() string {
		return creds.Get(workspace.Home(), creds.Webhook)
	})

	switch os.Args[1] {
	case "hook":
		runHook(os.Args[2:])
	case "doctor":
		os.Exit(doctor.Run(os.Stdout, argFlag(os.Args[2:], "--path", "."), hasFlag(os.Args[2:], "--fix")))
	case "config":
		runConfig(os.Args[2:])
	case "init":
		runInit(os.Args[2:])
	case "answer":
		mustArg(os.Args, 2, "orion answer <id>")
		runAnswer(os.Args[2])
	case "new":
		mustArg(os.Args, 2, "orion new \"<idea>\"")
		runNew(os.Args[2], os.Args[3:])
	case "ls":
		exitOn(workspace.List(os.Stdout))
	case "open":
		mustArg(os.Args, 2, "orion open <id>")
		exitOn(workspace.PrintPath(os.Stdout, os.Args[2]))
	case "rm":
		mustArg(os.Args, 2, "orion rm <id>")
		exitOn(workspace.Remove(os.Args[2], hasFlag(os.Args[3:], "--force")))
	case "provision":
		mustArg(os.Args, 2, "orion provision <id>")
		runProvision(os.Args[2], os.Args[3:])
	case "plan":
		mustArg(os.Args, 2, "orion plan <KEY>")
		runPlan(os.Args[2:])
	case "run":
		mustArg(os.Args, 2, "orion run <id>")
		runSupervised(os.Args[2], os.Args[3:])
	case "status":
		// With an id, the workspace view. Without one, the project view:
		// asking "what is Orion's state here" should not require knowing a
		// workspace id you may not have created yet.
		if len(os.Args) > 2 {
			exitOn(workspace.Status(os.Stdout, os.Args[2]))
		} else {
			runProjectStatus(os.Stdout)
		}
	case "work":
		mustArg(os.Args, 2, "orion work <KEY> [KEY...]")
		runWork(os.Args[2:])
	case "queue":
		// Bare `queue` reads; `queue add|remove` writes (OR-223). A verb rather
		// than a flag because reading the queue and changing it are different
		// operations with different consequences, and the read has to stay the
		// thing you get when you type the command with nothing after it.
		if len(os.Args) > 2 && (os.Args[2] == "add" || os.Args[2] == "remove") {
			runQueueEdit(os.Args[2], os.Args[3:])
		} else {
			runQueue(os.Args[2:])
		}
	case "routes":
		runRoutes()
	case "repos":
		runRepos(os.Args[2:])
	case "sandbox":
		runSandbox(os.Args[2:])
	case "slack":
		runSlackCmd(os.Args[2:])
	case "collect":
		runCollect(os.Args[2:])
	case "protect":
		runProtect(os.Args[2:])
	case "watch":
		runWatch(os.Args[2:])
	case "changelog":
		runChangelog(os.Args[2:])
	case "release":
		runRelease(os.Args[2:])
	case "conflict":
		runConflict(os.Args[2:])
	case "reset":
		runReset(os.Args[2:])
	case "settle":
		runSettle(os.Args[2:])
	case "fix":
		mustArg(os.Args, 2, "orion fix start|end")
		runFix(os.Args[2])
	case "explore":
		runExplore(os.Args[2:])
	case "njagents", "nj-agents":
		runNJAgents(os.Args[2:])
	case "report":
		runReport(os.Args[2:])
	case "logs", "log":
		runLogs(os.Args[2:])
	case "budget":
		runBudget(os.Args[2:])
	case "lessons":
		runLessons(os.Args[2:])
	case "aiops":
		runAIOps(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("orion %s\n", Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "orion: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(64)
	}
}

// runHook is the hot path: it runs on every matching tool call, so it
// does the minimum work needed to reach a verdict.
func runHook(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "orion: hook needs a name (breaker|gate|shield|session-start)")
		os.Exit(64)
	}
	name := args[0]

	in, ok := hook.Read(os.Stdin)
	if !ok {
		// Deliberate: unparseable input from a future harness version must
		// not brick the session. Exit 0, do not block. Documented as a known
		// gap in the README.
		fmt.Fprintln(os.Stderr, "orion: could not parse hook input; allowing (guardrails inactive for this call)")
		os.Exit(hook.ExitAllow)
	}

	start := in.CWD
	if start == "" {
		start, _ = os.Getwd()
	}
	root, err := config.FindRoot(start)
	if err != nil {
		// No project root means no state directory and no config. Better to
		// proceed and say so than to block every call in a scratch dir.
		fmt.Fprintf(os.Stderr, "orion: no project root found from %s; guardrails inactive\n", start)
		os.Exit(hook.ExitAllow)
	}
	cfg := config.Load(root)
	store := state.New(cfg.StateDir())

	switch name {
	case "breaker":
		hook.Emit(hook.Breaker(in, cfg, store))
	case "gate":
		hook.Emit(hook.Gate(in, cfg))
	case "shield":
		hook.Emit(hook.Shield(in, cfg))
	case "session-start":
		// A new session must never inherit an exhausted budget or a tripped
		// breaker from the last one.
		_ = store.Reset(in.SessionID)
		d := hook.Allow("")
		if n, _ := store.Sweep(7 * 24 * time.Hour); n > 0 {
			d = d.WithNote("swept %d stale session state files", n)
		}
		// Refresh the lessons block so every session starts already knowing
		// what previous projects learned.
		if note := refreshLessons(cfg.Root); note != "" {
			d = d.WithNote("%s", note)
		}
		hook.Emit(d)
	default:
		fmt.Fprintf(os.Stderr, "orion: unknown hook %q\n", name)
		os.Exit(64)
	}
}

// runConfig is the credential wizard.
//
// It exists because a shell profile is read by interactive shells only, so
// credentials exported in a terminal are invisible to cron and launchd. Orion
// reading its own file removes that whole class of "works for me, not for the
// scheduled run".
const configUsage = `orion config                interactive setup for Jira, Slack and webhooks
orion config show           what is set, where it came from, secrets masked
orion config path           print the config file location
orion config agents         interactive: name, model and effort per agent, by menu
                             (global -- one roster, shared by every project)
orion config agents --list  print the roster: every agent's effective model and
                             effort, and which of them agents.json overrides
orion config agents --reset [id...]   reset one, some, or every agent to shipped defaults
orion config limits         show the circuit breakers and where each value came from
orion config limits KEY N   set one, e.g. limits max_concurrent_tickets 3
                             (writes the project's orion.json -- the same file
                              the watcher reads; a running watcher keeps its own)
`

// runConfig dispatches "orion config" and its subcommands.
//
// --help and -h are checked before anything else, and independently of
// which subcommand (if any) precedes them: "orion config --help" is a
// request for the text above, never for the interactive credentials
// wizard. Without this check "--help" falls through as an unrecognized sub
// (it starts with "-", so the positional-subcommand parse below skips it)
// and lands on the wizard's own default case, which blocks on stdin
// waiting for a Jira URL nobody meant to type -- the only way out is
// Ctrl-C. A help flag must never be able to start an interactive prompt.
func runConfig(args []string) {
	if hasFlag(args, "--help") || hasFlag(args, "-h") {
		fmt.Print(configUsage)
		return
	}

	home := workspace.Home()
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	if sub == "help" {
		fmt.Print(configUsage)
		return
	}

	switch sub {
	case "", "set", "edit":
		var only []string
		if v := argFlag(args, "--only", ""); v != "" {
			for _, k := range strings.Split(v, ",") {
				only = append(only, strings.TrimSpace(k))
			}
		}
		exitOn(creds.Wizard(home, os.Stdin, os.Stdout, only))

	case "show", "list":
		exitOn(creds.Show(home, os.Stdout))

	case "path":
		fmt.Println(creds.Path(home))

	case "agents":
		runConfigAgents(args)

	case "limits":
		runConfigLimits(args)

	case "collect":
		runConfigCollect(args)

	default:
		fmt.Fprintf(os.Stderr, "orion: unknown config subcommand %q (set|show|path|agents|limits|collect)\n", sub)
		os.Exit(64)
	}
}

// runInit adopts the current repository.
func runInit(args []string) {
	dir := argFlag(args, "--dir", ".")
	abs, err := filepath.Abs(dir)
	exitOn(err)

	bin, binWarns := adopt.StableBinaryPath()

	res, err := adopt.Run(adopt.Options{
		Dir:            abs,
		Binary:         bin,
		BinaryWarnings: binWarns,
		PlanGate:       hasFlag(args, "--plan-gate"),
		Force:          hasFlag(args, "--force"),
	})
	if res != nil {
		res.Write(os.Stdout)
	}
	exitOn(err)

	cfg := config.Load(abs)

	// Before anything is bound: a repository whose only branch is the
	// release branch gets an integration branch created below, but only if
	// the config names one. If work_branch has been pointed at the release
	// branch, EnsureWorkBranch would happily "bind" it and the repository
	// would be initialised into the very model Orion forbids. Say so here,
	// where the remedy is one edit away.
	exitOn(cfg.Validate())
	if waiver := cfg.ReleaseBranchWaiver(); waiver != "" {
		ui.Warn(os.Stdout, "%s", waiver)
	}

	// The work branch. orion.json can name `develop` as the base for every
	// task branch while no such branch exists, and nothing notices until the
	// first PR has nowhere to go.
	if created, warns, bErr := adopt.EnsureWorkBranch(abs, cfg.VCS.WorkBranch); bErr != nil {
		ui.Warn(os.Stdout, "%v", bErr)
	} else {
		if created {
			ui.Ok(os.Stdout, "created", "branch %s (switched to it; the PR base for all task branches)",
				cfg.VCS.WorkBranch)
		} else {
			ui.Ok(os.Stdout, "bound", "branch %s (already exists)", cfg.VCS.WorkBranch)
		}
		for _, w := range warns {
			ui.Warn(os.Stdout, "%s", w)
		}
	}

	// Attribution tooling. Separate from the author alias: the alias marks a
	// commit as coming from an Orion run, dun records which agent, which
	// model and how much of the diff it actually produced, from the session
	// transcripts afterwards.
	if cfg.Attribution.Enabled {
		ask := func(p string) bool { return confirm(p) }
		if hasFlag(args, "--yes") {
			ask = func(string) bool { return true }
		}
		lines, warns := adopt.EnsureDun(abs, cfg.Attribution.AutoInstall, ask)
		for _, l := range lines {
			if v, d, ok := strings.Cut(l, " "); ok && !strings.HasPrefix(l, "  dun |") {
				ui.Ok(os.Stdout, strings.TrimSpace(v), "%s", strings.TrimSpace(d))
			} else {
				fmt.Println(ui.Dim(os.Stdout, l))
			}
		}
		for _, w := range warns {
			ui.Warn(os.Stdout, "%s", w)
		}
	}

	// CI. Without a workflow, GitHub reports no checks at all and Orion has
	// no honest verdict to gate a merge on -- so it treats the branch as
	// passing, because the alternative leaves every ticket waiting forever
	// for a verdict nobody will produce. Scaffolding here is what makes
	// "no checks configured" a deliberate state rather than the default one.
	ensureCI(abs)

	if !hasFlag(args, "--no-provision") {
		provisionRemote(abs, cfg, hasFlag(args, "--yes"))
	}

	// Server-side cleanup of merged head branches. After provisionRemote,
	// because a repository that does not exist yet has no settings to set.
	ensureRepoSettings(abs)

	// The sandbox. Built at adoption rather than lazily at the first run, so
	// a bad remote, missing auth or a dirty working copy surfaces now --
	// while you are watching -- instead of halfway through a paid run.
	if !hasFlag(args, "--no-sandbox") {
		ensureSandbox(abs, cfg, hasFlag(args, "--force"))
	}

	fmt.Println()
	fmt.Println("Orion is wired into this repo. Restart any running Claude Code session:")
	fmt.Println("hooks are read at session start, so an open session is still unguarded.")
	fmt.Println()
	fmt.Println("  orion doctor        confirm the config parses and the limits are in force")
	if !hasFlag(args, "--plan-gate") {
		fmt.Println()
		fmt.Println("The plan gate is off. Turn on gates.require_plan_before_edit in orion.json")
		fmt.Println("when you want implementation blocked until a plan exists.")
	}
}

// provisionRemote offers to create the tracker project and chat channel for
// an adopted repo, then records them in orion.json.
//
// It describes before it acts, and asks once. A Jira project cannot be
// deleted without admin rights, and `orion init` is a command people re-run
// casually, so silent creation would let a stray invocation in the wrong
// directory litter a shared tracker permanently.
func provisionRemote(dir string, cfg config.Config, assumeYes bool) {
	name := adopt.DeriveProjectName(dir)
	plan := adopt.RemotePlan{
		ProjectName: name,
		SlackIsPriv: cfg.Slack.Private,
	}

	var jiraLead string
	j, jErr := tracker.NewJiraFromEnv()
	switch {
	case jErr != nil:
		plan.JiraSkip = "not configured"
	default:
		// Jira rejects project creation without a lead, and the error
		// ("You must specify a valid project lead") arrives only after the
		// confirmation has been given. Resolve the authenticated account up
		// front and use it: whoever ran init is the obvious lead.
		if cap, err := j.Probe(); err == nil && cap.AccountID != "" {
			jiraLead = cap.AccountID
		}
		plan.JiraSite = j.BaseURL
		plan.JiraKey = cfg.Tracker.ProjectKey
		if plan.JiraKey == "" {
			plan.JiraKey = adopt.DeriveJiraKey(name)
		}
		if exists, _, err := j.ProjectExists(plan.JiraKey); err != nil {
			plan.JiraSkip = "could not check " + plan.JiraKey + ": " + err.Error()
		} else {
			plan.JiraExists = exists
		}
	}

	sc, sErr := slack.FromEnv()
	switch {
	case sErr != nil:
		plan.SlackSkip = "not configured"
	default:
		id, err := sc.AuthTest()
		if err != nil {
			plan.SlackSkip = "token rejected: " + err.Error()
			break
		}
		plan.SlackTeam = id.Team
		plan.SlackName = slack.NormalizeChannelName(cfg.Slack.ChannelPrefix + name)
		// Check before describing, so the plan does not offer to create a
		// channel it would in fact bind. The Jira line already distinguishes
		// the two; a plan that is honest about one and not the other trains
		// people to skim it.
		if ch, err := sc.FindChannel(plan.SlackName, cfg.Slack.Private); err == nil && ch != nil {
			plan.SlackExists = true
		}
	}

	if plan.JiraSkip != "" && plan.SlackSkip != "" {
		return // nothing configured; say nothing rather than nagging
	}

	fmt.Println()
	fmt.Print(plan.Describe())
	if !plan.Nothing() && !assumeYes {
		if !confirm("Proceed?") {
			ui.Ok(os.Stdout, "skipped", "remote provisioning (re-run orion init to do it later)")
			return
		}
	}

	cfgPath := filepath.Join(dir, "orion.json")
	if plan.JiraSkip == "" && plan.JiraKey != "" {
		if !plan.JiraExists {
			if jiraLead == "" {
				ui.Warn(os.Stdout, "no Jira account resolved to lead the project; not creating it")
				goto slackStep
			}
			// No description: `orion init` adopts a repo that already explains
			// itself, and inventing one from a directory name would put a worse
			// statement of the work where `orion new` puts a real one.
			if _, err := j.CreateProject(plan.JiraKey, name, "", jiraLead); err != nil {
				ui.Fail(os.Stdout, "Jira project %s: %v", plan.JiraKey, err)
				goto slackStep
			}
			ui.Ok(os.Stdout, "created", "Jira project %s  %s/browse/%s", plan.JiraKey, j.BaseURL, plan.JiraKey)
		} else {
			ui.Ok(os.Stdout, "bound", "Jira project %s  %s/browse/%s", plan.JiraKey, j.BaseURL, plan.JiraKey)
		}
		patchConfig(cfgPath, map[string][2]string{
			"tracker": {"enabled", "true"},
		}, plan.JiraKey)
	}

slackStep:
	if plan.SlackSkip == "" && plan.SlackName != "" {
		ch, err := sc.CreateChannel(plan.SlackName, cfg.Slack.Private)
		if err != nil {
			ui.Fail(os.Stdout, "Slack channel #%s: %v", plan.SlackName, err)
			return
		}
		verb := "bound"
		if ch.Created {
			verb = "created"
		}
		ui.Ok(os.Stdout, verb, "Slack channel #%s", ch.Name)
		ensureAudience(sc, ch, cfg, cfgPath)
		patchConfig(cfgPath, map[string][2]string{"slack": {"enabled", "true"}}, "")

		// Record the channel ON THE WORKSPACE, which is the only place
		// anything later looks for it.
		//
		// Without this, adoption created the channel, enabled Slack in the
		// config, printed a success line -- and left the workspace's own
		// record nil, so notify was handed an empty channel and skipped
		// Slack entirely, without error, for every run. Nothing failed,
		// because nothing had been asked. The whole feature looked broken
		// while every part of it was working.
		recordChannel(dir, ch)
	}
}

// ensureSandbox creates the isolated clone this repo's runs happen in.
//
// Orion never runs the agent in your working copy. An agent editing the
// directory you have open in an editor can destroy uncommitted work with no
// undo, so the sandbox clones from the REMOTE and your checkout stays
// read-only, fast-forwarded afterwards rather than written to.
func ensureSandbox(dir string, cfg config.Config, force bool) {
	w := os.Stdout

	if existing := workspace.FindBySource(dir); existing != nil {
		ui.Ok(w, "bound", "sandbox %s (%s)", existing.ID, existing.RepoDir())
		registerRepo(dir, cfg, existing)
		ensureSandboxEnv(w, existing.RepoDir())
		return
	}
	remote, err := workspace.RemoteOf(dir)
	if err != nil {
		ui.Warn(w, "no sandbox: %v", err)
		return
	}
	if problems := workspace.Preflight(dir); len(problems) > 0 && !force {
		ui.Warn(w, "no sandbox yet: the working copy is not in a state worth cloning")
		for _, p := range problems {
			fmt.Fprintf(w, "         %s\n", p)
		}
		fmt.Fprintf(w, "         Commit and push, then re-run orion init (or --force).\n")
		return
	}
	ws, err := workspace.Bind(workspace.BindOptions{
		SourcePath: dir, Remote: remote, Force: force,
		DefaultBranch: cfg.VCS.DefaultBranch, WorkBranch: cfg.VCS.WorkBranch,
	})
	if err != nil {
		ui.Fail(w, "sandbox: %v", err)
		return
	}
	ui.Ok(w, "created", "sandbox %s", ws.ID)
	fmt.Fprintf(w, "         %s\n", ui.Dim(w, ws.RepoDir()))
	registerRepo(dir, cfg, ws)
	if len(ws.Task.Branches) > 0 {
		ui.Ok(w, "created", "branches %s in the sandbox", strings.Join(ws.Task.Branches, ", "))
	}
	// The environment, once, here -- not once per ticket in a worktree that
	// cannot keep it.
	ensureSandboxEnv(w, ws.RepoDir())
}

// registerRepo records the project-key to repository mapping.
//
// Without it every command has to be run from inside a checkout, and the
// daemon -- which has no meaningful working directory -- cannot act on a
// ticket at all. The ticket key already names the project; this supplies the
// other half.
func registerRepo(dir string, cfg config.Config, ws *workspace.Workspace) {
	key := strings.TrimSpace(cfg.Tracker.ProjectKey)
	if key == "" {
		return // no tracker bound: nothing to key the mapping on
	}
	var channel string
	if ws.Task.Slack != nil {
		channel = ws.Task.Slack.ID
	}
	err := registry.Bind(workspace.Home(), registry.Entry{
		Key: key, Source: dir, Workspace: ws.ID,
		Channel: channel, Remote: ws.Task.Remote,
	})
	if err != nil {
		// A collision is a real problem, not a warning to scroll past: two
		// repositories claiming one key means work lands in whichever ran
		// last, so say it loudly and leave the original binding intact.
		ui.Fail(os.Stdout, "%v", err)
		return
	}
	ui.Ok(os.Stdout, "bound", "project %s -> this repository", key)
}

// runRepos lists what is registered, so a mapping that has gone wrong is
// visible rather than something you deduce from a failure.
func runRepos(args []string) {
	home := workspace.Home()
	w := os.Stdout

	if len(args) > 0 && args[0] == "unbind" {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: orion repos unbind <PROJECT>")
			os.Exit(64)
		}
		exitOn(registry.Unbind(home, args[1]))
		ui.Ok(w, "updated", "unbound %s", strings.ToUpper(args[1]))
		return
	}

	f, err := registry.Load(home)
	exitOn(err)
	if len(f.Repos) == 0 {
		fmt.Fprintln(w, "no repositories registered. Run orion init inside one.")
		return
	}
	fmt.Fprintf(w, "%s\n\n", ui.Heading(w, "registered repositories"))
	for _, k := range f.Keys() {
		e := f.Repos[k]
		fmt.Fprintf(w, "  %-10s %s\n", k, e.Source)
		fmt.Fprintf(w, "             %s\n", ui.Dim(w, "sandbox "+e.Workspace))
	}
	missing, err := registry.Prune(home)
	if err == nil && len(missing) > 0 {
		fmt.Fprintln(w)
		for _, e := range missing {
			ui.Warn(w, "%s: %s is gone.\n"+
				"         Not unbound automatically: an unmounted volume looks the same as a\n"+
				"         deletion, and freeing the key would let another repo claim it.\n"+
				"         Remove it deliberately with: orion repos unbind %s", e.Key, e.Source, e.Key)
		}
	}
}

// ensureAudience makes certain a human can actually read this channel.
//
// A PRIVATE channel with only the bot in it is not a communication medium.
// Slack shows it to nobody outside it -- not in the sidebar, not in search,
// with no notification that it exists -- so every message Orion sends is
// accepted, stored, and unreadable. That is not a hypothetical: fcia ran
// that way for two full pipelines, and each failure was diagnosed as "Slack
// is broken" because every check that existed passed.
//
// The old version could not have caught it, for two reasons:
//
//	it returned early unless the channel had just been CREATED -- but the
//	one that stranded fcia was FOUND, and a found channel was assumed to
//	"already have whoever belongs in it"
//
//	with no invite_users it printed a warning and continued, and a warning
//	during a long init scrolls past
//
// So this now runs for a found channel too, VERIFIES membership rather than
// assuming the invite worked, and tries to identify the person running init
// before giving up. Verification is the part that matters: an invite that
// silently failed used to look identical to one that succeeded.
func ensureAudience(sc *slack.Client, ch *slack.Channel, cfg config.Config, cfgPath string) {
	if len(cfg.Slack.InviteUsers) > 0 {
		invited, errs := sc.Invite(ch.ID, cfg.Slack.InviteUsers)
		for _, e := range errs {
			ui.Warn(os.Stdout, "inviting to #%s: %v", ch.Name, e)
		}
		if len(invited) > 0 {
			ui.Ok(os.Stdout, "invited", "%d to #%s", len(invited), ch.Name)
		}
	}

	members, err := sc.Members(ch.ID)
	if err != nil {
		// Cannot verify. Say so rather than implying it is fine -- this is
		// the check, and a check that fails quietly is worse than none.
		ui.Warn(os.Stdout, "could not confirm who is in #%s: %v\n"+
			"         Open it in Slack and check you are a member.", ch.Name, err)
		return
	}
	if n := countHumans(members, sc); n > 0 {
		ui.Ok(os.Stdout, "ok", "#%s has %d human member(s)", ch.Name, n)
		return
	}

	// Nobody home. Try to work out who is standing here.
	//
	// git's configured email is the best guess available: the person running
	// init is the person committing to this repository. Needs the
	// users:read.email scope, which is not in Orion's default manifest, so
	// this is an attempt and not a guarantee.
	if id := slackIDForGitUser(sc); id != "" {
		if invited, errs := sc.Invite(ch.ID, []string{id}); len(invited) > 0 {
			ui.Ok(os.Stdout, "invited", "you (%s) to #%s", id, ch.Name)
			// Persist it, so the next project and the next re-run do not
			// depend on a lookup that may not be permitted then.
			patchConfig(cfgPath, map[string][2]string{
				"slack": {"invite_users", `["` + id + `"]`},
			}, "")
			return
		} else if len(errs) > 0 {
			ui.Warn(os.Stdout, "could not add you to #%s: %v", ch.Name, errs[0])
		}
	}

	// Out of options. This is a hard failure of the notification setup, so
	// it is stated as one, with the exact edit that fixes it.
	ui.Fail(os.Stdout, "#%s has no members except the bot, so nobody can read anything Orion sends there.",
		ch.Name)
	fmt.Fprintf(os.Stdout,
		"         Messages will be delivered and invisible -- the failure looks exactly\n"+
			"         like Slack being broken. Fix it before the first run:\n\n"+
			"           1. open Slack, find your member ID (Profile -> ... -> Copy member ID)\n"+
			"           2. set it in %s:  \"invite_users\": [\"U0123456789\"]\n"+
			"           3. orion init --force\n\n"+
			"         Or add yourself to #%s by hand -- a bot cannot invite itself an audience.\n",
		cfgPath, ch.Name)
}

// countHumans returns how many members are not the bot itself.
//
// Defers to humansAmong rather than re-deriving the rule. Two copies of
// "who counts as a reader" in one package is one copy too many: they drift,
// and the one that drifts is the one nobody has a test for.
func countHumans(members []string, sc *slack.Client) int {
	self := ""
	if id, err := sc.AuthTest(); err == nil {
		self = id.UserID
	}
	return len(humansAmong(members, self))
}

// slackIDForGitUser looks up the operator's Slack id from their git email.
func slackIDForGitUser(sc *slack.Client) string {
	out, err := exec.Command("git", "config", "user.email").Output()
	email := strings.TrimSpace(string(out))
	if err != nil || email == "" {
		return ""
	}
	id, err := sc.LookupUserByEmail(email)
	if err != nil {
		return ""
	}
	return id
}

// patchConfig edits orion.json as text rather than round-tripping the JSON,
// so the "_comment_*" keys stay next to the settings they explain.
func patchConfig(path string, fields map[string][2]string, jiraKey string) {
	b, err := os.ReadFile(path)
	if err != nil {
		ui.Warn(os.Stdout, "could not update %s: %v", path, err)
		return
	}
	src := string(b)
	changed := false
	for block, kv := range fields {
		if out, ok := adopt.SetBlockField(src, block, kv[0], kv[1]); ok {
			src, changed = out, true
		}
	}
	if jiraKey != "" {
		if out, ok := adopt.SetBlockField(src, "tracker", "project_key", `"`+jiraKey+`"`); ok {
			src, changed = out, true
		}
	}
	if !changed {
		return
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		ui.Warn(os.Stdout, "could not write %s: %v", path, err)
		return
	}
	ui.Ok(os.Stdout, "updated", "orion.json")
}

// confirm asks once. A non-interactive shell answers no: creating something
// undeletable because nobody was there to object is the wrong default.
func confirm(prompt string) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		fmt.Println("(not a terminal; skipping. Re-run with --yes to provision unattended.)")
		return false
	}
	fmt.Printf("%s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Scanln(&ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// runWork implements one or more tickets.
func runWork(args []string) {
	keys, err := ticketKeys("work", "<KEY> [KEY...]",
		positional(args, "--max-minutes", "--max-turns"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	if len(keys) == 0 {
		fmt.Fprintln(os.Stderr, "orion work needs at least one ticket: orion work FCIA-6")
		os.Exit(64)
	}

	results := work.Run(work.Options{
		Keys: keys, Out: os.Stdout, Home: workspace.Home(),
		DryRun:     hasFlag(args, "--dry-run"),
		MaxMinutes: intFlag(args, "--max-minutes", 0),
		MaxTurns:   intFlag(args, "--max-turns", 0),
	}, work.Deps{
		Jira:      mustJira(),
		Supervise: supervisor.Run,
		Advise:    adviseRunner,
		Describe:  describeRunner,
		Push:      pushBranch,
		OpenPR:    openPR,
		Merged:    mergedBranch,
	})

	// Exit non-zero when anything needs a person, so a wrapper script or a
	// cron entry can tell "done" from "someone has to look at this".
	worst := 0
	for _, r := range results {
		switch r.Outcome {
		case work.OutcomeFailed:
			worst = 1
		case work.OutcomeBlocked:
			if worst == 0 {
				worst = 2
			}
		}
	}
	os.Exit(worst)
}

// adviseRunner runs one READ-ONLY agent turn for an advisor or the router.
//
// Read-only is enforced by --allowedTools, not by asking politely in the
// prompt. Two agents writing to one worktree is a race with no referee, and
// an architect that "just fixes it while it is here" destroys the separation
// that makes its answer worth anything -- an advisor that edits is no longer
// an independent opinion, it is a second implementer.
//
// --max-turns is low: an advisor reads three documents and decides. One that
// needs twenty turns is exploring the codebase, which is precisely what it
// was told not to do, and the cap makes that a bounded cost rather than a
// second implementation run.
func adviseRunner(dir, model, prompt string) (string, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude CLI not found on PATH")
	}
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--model", model,
		"--max-turns", "8",
		"--allowedTools", "Read,Glob,Grep",
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	// Advisors read committed artifacts, nothing else. Scrubbing the
	// environment matters more here than for the implementer: this agent has
	// no reason to touch a credential at all.
	cmd.Env = append(os.Environ(), "ORION_ROLE=advisor")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("advisor run failed: %w", err)
	}
	var res struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if jsonErr := json.Unmarshal(out, &res); jsonErr != nil {
		return strings.TrimSpace(string(out)), nil
	}
	if res.IsError {
		return "", fmt.Errorf("the advisor reported an error: %s", truncateStr(res.Result, 200))
	}
	return res.Result, nil
}

func mustJira() work.TrackerAPI {
	j, err := tracker.NewJiraFromEnv()
	exitOn(err)
	return j
}

// mustJiraSearch is the same client through the collector's wider interface,
// which also needs Search to find what is waiting.
func mustJiraSearch() collect.TrackerAPI {
	j, err := tracker.NewJiraFromEnv()
	exitOn(err)
	return j
}

// pushBranch sets upstream on first push so a later `git push` in that
// worktree does the obvious thing.
// Bounded by pushTimeout (OR-128): a push that stalls -- a credential
// helper waiting on a prompt nobody will answer, a dead connection -- would
// otherwise block orion watch's whole loop with nothing on the console.
func pushBranch(dir, branch string) error {
	cmd, cancel := gitCommand(dir, "push", "-u", "origin", branch)
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// prCommand builds the gh invocation.
//
// Split out from openPR so the working directory can be asserted in a test
// without running gh. That is the whole reason this exists: openPR took a
// dir and never used it, so gh ran in Orion's own cwd -- which is wherever
// the user was standing, and on the first real end-to-end run was ~/.claude.
// The branch pushed, then the PR failed with "not a git repository", leaving
// a ticket marked failed over work that had entirely succeeded.
//
// gh resolves the repository from the working directory, and the branch
// lives in a worktree, never in the cwd -- which ghCommand sets. Bounded by
// ghTimeout (OR-128) like every other gh call on the watch path: opening the
// pull request is the last step of a job that has already been paid for, so
// hanging here strands work that is otherwise complete.
func prCommand(dir, branch, title, body, base string) (*exec.Cmd, context.CancelFunc) {
	return ghCommand(dir, "pr", "create", "--head", branch, "--base", base,
		"--title", title, "--body", body)
}

// openPR shells out to gh. Orion does not embed a GitHub client: gh already
// holds the auth, and a second credential path is a second thing to expire.
func openPR(dir, branch, title, body, base string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("gh is not installed, so the branch is pushed but no pull request was opened.\n" +
			"  Open it yourself, or install gh and re-run")
	}
	cmd, cancel := prCommand(dir, branch, title, body, base)
	defer cancel()
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("%v\n%s", err, text)
	}
	// gh prints the URL as the last line.
	for i := len(strings.Split(text, "\n")) - 1; i >= 0; i-- {
		line := strings.TrimSpace(strings.Split(text, "\n")[i])
		if strings.HasPrefix(line, "http") {
			return line, nil
		}
	}
	return text, nil
}

// runQueue shows what the watcher WOULD work, in the order it would work
// it, and does nothing else.
//
// Read-only on purpose. The queue is driven by a Jira label, so the obvious
// failure is a JQL that matches more than you meant -- and the moment that
// query drives real runs, discovering the mistake costs money and writes to
// your repo. This lets you see the query and its result first.
func runQueue(args []string) {
	root, err := config.FindRoot(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "not inside an Orion project (no orion.json or .git found)")
		os.Exit(1)
	}
	cfg := config.Load(root)
	w := os.Stdout

	if !cfg.Tracker.Enabled {
		fmt.Fprintln(w, "tracker is disabled in orion.json; nothing to queue from")
		return
	}
	j, err := tracker.NewJiraFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	jql := queueJQL(cfg)
	fmt.Fprintf(w, "%s\n  %s\n\n", ui.Heading(w, "queue"), ui.Dim(w, jql))

	issues, err := j.Search(jql, intFlag(args, "--limit", 25))
	if err != nil {
		ui.Fail(w, "%v", err)
		os.Exit(1)
	}
	if len(issues) == 0 {
		fmt.Fprintf(w, "  nothing labelled %s in %s\n",
			cfg.Tracker.QueueLabel, cfg.Tracker.ProjectKey)
		return
	}
	// Why a labelled ticket will not be claimed, per ticket. Read here rather
	// than filtered into the query on purpose: the WATCHER's query excludes
	// an unscheduled ticket (OR-221), and if this command did the same the
	// ticket would vanish from the one view whose entire job is to say what
	// the watcher would do and why.
	holds := queueHolds(w, j, cfg, issues)

	// Group by queue state, not by Jira order. What is running and what
	// broke are the two things you look for first; making them the top of
	// the list is the difference between a report and a wall of text.
	// Within a group the tracker's own ordering is preserved, because that
	// is the execution order.
	groups := []struct {
		state string
		verb  string
	}{
		{"working", "working"},
		{"ci-wait", "ci-wait"},
		{"failed", "failed"},
		{"queued", "queued"},
	}
	counts := map[string]int{}
	n := 0
	for _, g := range groups {
		for _, i := range issues {
			if tracker.State(i.Labels, cfg.Tracker.QueueLabel) != g.state {
				continue
			}
			verb := g.verb
			// A held ticket is queued as far as the labels go and is not
			// queued at all as far as the watcher is concerned. Counting it
			// as queued would make this command's own summary disagree with
			// what the watcher does next.
			if hold := holds[i.Key]; hold != "" && g.state == "queued" {
				verb = "held"
				counts["held"]++
			} else {
				counts[g.state]++
			}
			n++
			pr := i.Priority
			if pr == "" {
				// Priority is disabled on some team-managed projects. Saying
				// so beats a blank column that makes the order look arbitrary.
				pr = "none"
			}
			fmt.Fprintf(w, "  %2d. %s %-9s %-7s %-12s %s\n",
				n, ui.Label(w, verb, ""), i.Key, pr, i.Status, i.Summary)
			fmt.Fprintf(w, "      %s\n", ui.Dim(w, i.URL))
			if hold := holds[i.Key]; hold != "" {
				fmt.Fprintf(w, "      %s\n", ui.Dim(w, hold))
			}
		}
	}

	fmt.Fprintf(w, "\n  %d working, %d awaiting CI, %d queued, %d failed.\n",
		counts["working"], counts["ci-wait"], counts["queued"], counts["failed"])
	if counts["held"] > 0 {
		ui.Warn(w, "%d labelled ticket(s) will NOT be claimed until they are scheduled.\n"+
			"  Attach each to an open release: orion release add <version> <KEY>",
			counts["held"])
	}

	// Where this work would go, before any of it runs. See routingSummary.
	if summary, hint := routingSummary(issues); summary != "" {
		fmt.Fprintf(w, "  %s\n", summary)
		if hint != "" {
			fmt.Fprintf(w, "  %s\n", ui.Dim(w, hint))
		}
	}
	if counts["failed"] > 0 {
		fmt.Fprintf(w, "  A failed ticket is not retried: remove %s and add %s to requeue it.\n",
			tracker.LabelFailed, cfg.Tracker.QueueLabel)
	}
	// A finished ticket holding the claim lock stops the whole queue, and it
	// looks identical to a job that is genuinely running. Say so here rather
	// than let the reader spot that a "working" line's status reads Done.
	if stale := tracker.StaleLocks(issues); len(stale) > 0 {
		ui.Warn(w, "%s is finished but still holds the %s lock, which stops the queue.\n"+
			"  Remove the label, or let `orion watch` clear it on its next tick.",
			strings.Join(stale, ", "), tracker.LabelWorking)
	}
	fmt.Fprintln(w, "  Nothing has been started: this command only reads.")
}

// routingSummary says which actor each queued ticket would reach, and adds a
// hint when every one of them takes the default.
//
// Printed here because here is BEFORE the money is spent. A queue that is
// entirely default is either correct -- these really are backend tickets --
// or a planning failure in which nothing wrote a marker, and the two are
// indistinguishable until the split is on screen. The hint is not a warning:
// all-default is the right answer often enough that flagging it as a problem
// would train the reader to ignore the line (OR-191).
func routingSummary(issues []tracker.Issue) (summary, hint string) {
	dist := work.Distribution(issues)
	if len(dist) == 0 {
		return "", ""
	}
	parts := make([]string, 0, len(dist))
	for _, t := range dist {
		part := fmt.Sprintf("%d %s", t.N, actors.Display(t.Actor))
		if t.Actor == work.DefaultActor {
			part += " (default)"
		}
		parts = append(parts, part)
	}
	summary = "routing: " + strings.Join(parts, ", ") + "."
	if len(dist) == 1 && dist[0].Actor == work.DefaultActor {
		hint = "Every ticket takes the default. `orion routes` prints the markers that " +
			"change that; they are set when the ticket is created, not here."
	}
	return summary, hint
}

// queueHolds says, per issue key, why the watcher will not claim that ticket.
//
// DEGRADES rather than fails. This command is read-only and its job is to
// show the queue; a version read that 403s or times out must not turn that
// into an error, so the hold column is dropped with a line saying it was
// dropped. Silence would be the one outcome worse than either -- a reader
// would take an unmarked ticket for a claimable one.
func queueHolds(w io.Writer, j *tracker.Jira, cfg config.Config, issues []tracker.Issue) map[string]string {
	key := strings.TrimSpace(cfg.Tracker.ProjectKey)
	if key == "" {
		return nil
	}
	sched, err := tracker.LoadSchedules(j, []string{key})
	if err != nil {
		ui.Warn(w, "could not read %s's releases (%v), so this list does not say which\n"+
			"  tickets the watcher would hold back for having no fixVersion.", key, err)
		return nil
	}
	holds := map[string]string{}
	for _, i := range issues {
		// Only the ones waiting to be claimed. A ticket already working, in
		// CI or failed has been claimed already, and telling its reader it
		// "will not be claimed" would be false.
		if tracker.State(i.Labels, cfg.Tracker.QueueLabel) != "queued" {
			continue
		}
		if r := sched.HoldReason(i, cfg.Tracker.QueueLabel); r != "" {
			holds[i.Key] = r
		}
	}
	return holds
}

// queueJQL builds the query from config, scoped to the bound project so a
// label someone reused in another project cannot pull work into this repo.
func queueJQL(cfg config.Config) string {
	var scope string
	if k := strings.TrimSpace(cfg.Tracker.ProjectKey); k != "" {
		scope = tracker.JQLEq("project", k)
	}
	// Match the in-flight and failed states too, not just the queued one.
	// Matching only the queue label means a ticket DISAPPEARS from this view
	// the moment Orion claims it, so the one thing you most want to see --
	// what is running right now, and what stopped -- is the one thing the
	// command could not show.
	jql := tracker.JQLAnd(scope, tracker.JQLIn("labels", tracker.Managed(cfg.Tracker.QueueLabel)...))
	if o := strings.TrimSpace(cfg.Tracker.QueueOrder); o != "" {
		jql += " ORDER BY " + o
	}
	return jql
}

// runProjectStatus reports Orion's state for the repository you are in:
// what is wired, where the tracker and channel are, and what has been spent.
//
// Kept separate from `orion doctor`. doctor answers "can Orion work here",
// a pass/fail health check; this answers "what is it connected to", which is
// what you actually want when returning to a project after a week. Merging
// them would make one command that does neither job clearly.
func runProjectStatus(w io.Writer) {
	root, err := config.FindRoot(".")
	if err != nil {
		fmt.Fprintln(w, "not inside an Orion project (no orion.json or .git found)")
		fmt.Fprintln(w, "  adopt this repo with: orion init")
		os.Exit(1)
	}
	cfg := config.Load(root)
	name := adopt.DeriveProjectName(root)

	fmt.Fprintf(w, "orion status  %s\n\n", name)
	fmt.Fprintf(w, "  repo        %s\n", root)
	if cfg.Degraded {
		fmt.Fprintf(w, "  config      DEGRADED: %s\n", cfg.DegradedReason)
	} else {
		fmt.Fprintf(w, "  config      orion.json (max_tool_calls=%d, plan gate=%v)\n",
			cfg.Limits.MaxToolCalls, cfg.Gates.RequirePlanBeforeEdit)
	}

	// Branches.
	cur := gitOut(root, "branch", "--show-current")
	fmt.Fprintf(w, "  branch      %s", cur)
	if cur != cfg.VCS.WorkBranch && cur != cfg.VCS.DefaultBranch {
		fmt.Fprintf(w, "  (work branch is %s)", cfg.VCS.WorkBranch)
	}
	fmt.Fprintln(w)
	if gitOut(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+cfg.VCS.WorkBranch) == "" {
		fmt.Fprintf(w, "              WARNING %s does not exist; task branches have no PR base\n",
			cfg.VCS.WorkBranch)
	}

	// Hooks: the limits above are advisory unless these resolve.
	if st := hookState(root); st != "" {
		fmt.Fprintf(w, "  hooks       %s\n", st)
	}

	// Attribution.
	dun := adopt.DunLook(root)
	switch {
	case dun.Path == "":
		fmt.Fprintln(w, "  dun         not installed; commits carry no attribution trailer")
	case !dun.Instrumented:
		fmt.Fprintf(w, "  dun         %s installed, this repo NOT instrumented (orion init)\n", dun.Version)
	default:
		fmt.Fprintf(w, "  dun         %s, instrumented\n", dun.Version)
		if s := dunSummary(dun.Path, root); s != "" {
			fmt.Fprintf(w, "              %s\n", s)
		}
	}

	// Tracker.
	switch {
	case !cfg.Tracker.Enabled:
		fmt.Fprintln(w, "  jira        disabled in orion.json")
	case cfg.Tracker.ProjectKey == "":
		fmt.Fprintln(w, "  jira        enabled, no project bound (a project is created per idea)")
	default:
		line := cfg.Tracker.ProjectKey
		if j, err := tracker.NewJiraFromEnv(); err == nil {
			line += "  " + j.BaseURL + "/browse/" + cfg.Tracker.ProjectKey
			if ok, _, err := j.ProjectExists(cfg.Tracker.ProjectKey); err == nil && !ok {
				line += "  WARNING not found on this instance"
			}
		} else {
			line += "  (credentials not configured, so it cannot be reached)"
		}
		fmt.Fprintf(w, "  jira        %s\n", line)
	}

	// Chat.
	if !cfg.Slack.Enabled {
		fmt.Fprintln(w, "  slack       disabled in orion.json")
	} else {
		chName := slack.NormalizeChannelName(cfg.Slack.ChannelPrefix + name)
		line := "#" + chName
		if c, err := slack.FromEnv(); err == nil {
			if id, err := c.AuthTest(); err == nil {
				// Distinguish "the workspace says no such channel" from "the
				// lookup itself failed". Reporting a missing scope as a
				// missing channel sends you to create one that already
				// exists, which is how the Jira 401 wasted an hour earlier.
				ch, err := c.FindChannel(chName, cfg.Slack.Private)
				switch {
				case err == nil && ch != nil:
					if u := slack.ChannelURL(id.TeamID, ch.ID); u != "" {
						line += "  " + u
					}
				case err != nil && strings.Contains(err.Error(), "not found"):
					line += "  WARNING not in " + id.Team + " (orion init creates it)"
				default:
					line += "  WARNING lookup failed: " + err.Error()
				}
			} else {
				line += "  WARNING token rejected: " + err.Error()
			}
		} else {
			line += "  (no bot token, so nothing will be posted)"
		}
		fmt.Fprintf(w, "  slack       %s\n", line)
	}

	// Spend. Named as the user's own budget so it is never read as the
	// provider's remaining quota, which nothing reports.
	if l, err := budget.Load(workspace.Home()); err == nil {
		lim := budget.Limits{WeeklyUSD: cfg.Budget.WeeklyUSD, WeeklyTokens: cfg.Budget.WeeklyTokens}
		s := l.Status(lim)
		if !lim.Set() {
			fmt.Fprintf(w, "  budget      no weekly limit set, so the %v%% checkpoints can never fire\n",
				cfg.Budget.PauseAtPercent)
			fmt.Fprintf(w, "              $%.2f and %d runs in the last 7 days\n", s.SpentUSD, s.Runs)
		} else {
			fmt.Fprintf(w, "  budget      %d%% used  ($%.2f, %d runs, last 7 days)\n",
				s.Percent, s.SpentUSD, s.Runs)
		}
	}
	fmt.Fprintln(w)
}

func gitOut(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// hookState reuses what doctor checks, so status cannot report a healthier
// picture than doctor would for the same repo.
func hookState(root string) string {
	b, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		return "no .claude/settings.json; nothing enforces the limits above"
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return "settings.json is not valid JSON; no hook is running"
	}
	total, broken := 0, 0
	for _, entries := range doc.Hooks {
		for _, e := range entries {
			for _, h := range e.Hooks {
				f := strings.Fields(h.Command)
				if len(f) == 0 || !strings.HasPrefix(filepath.Base(f[0]), "orion") {
					continue
				}
				total++
				if _, err := exec.LookPath(f[0]); err != nil {
					broken++
				}
			}
		}
	}
	switch {
	case total == 0:
		return "none wired; the limits above are advisory (orion init)"
	case broken > 0:
		return fmt.Sprintf("%d of %d do not resolve; every gate is silently doing nothing", broken, total)
	}
	return fmt.Sprintf("%d wired and resolvable", total)
}

// dunSummary pulls the coverage line out of `dun status`, which is the one
// number worth surfacing here. Coverage is not adoption: it counts commits
// carrying any valid trailer, including `undetermined`.
func dunSummary(bin, root string) string {
	out, err := exec.Command(bin, "status", "--repo", root).Output()
	if err != nil {
		if out, err = exec.Command(bin, "status").Output(); err != nil {
			return ""
		}
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "coverage:") {
			return strings.Join(strings.Fields(l), " ")
		}
	}
	return ""
}

// runAnswer walks the open questions blocking a workspace.
func runAnswer(id string) {
	ws, err := workspace.Open(id)
	exitOn(err)
	cfg := config.Load(ws.RepoDir())
	path := filepath.Join(ws.RepoDir(), cfg.Paths.Intent, ws.Task.Slug+".md")

	a := discovery.Assess(path)
	if !a.Found {
		fmt.Printf("no intent captured yet at %s\n", path)
		fmt.Printf("run: orion run %s --stage intent\n", ws.ID)
		os.Exit(1)
	}
	if a.Open == 0 {
		fmt.Printf("no open questions in %s\n", path)
		return
	}

	// Editing the file is the answer path, not a prompt loop. The answers
	// belong in the committed artifact where every later stage reads them;
	// capturing them in a terminal session would put them somewhere no
	// stage can see.
	fmt.Printf("%d open question(s) in %s\n\n", a.Open, path)
	for _, q := range a.Questions {
		if q.Answered {
			continue
		}
		fmt.Printf("  - %s\n", q.Text)
	}
	fmt.Println()
	fmt.Println("Answer them in the file itself, so every later stage reads the answer:")
	fmt.Printf("  $EDITOR %s\n\n", path)
	fmt.Println("Mark each one with [x], ~~strikethrough~~, or an inline \"Answer: ...\".")
	fmt.Printf("Then: orion run %s --stage spec\n", ws.ID)
	os.Exit(1)
}

// createProjectChannel makes the workspace's Slack channel and posts the
// opening message, so the channel is useful the moment it appears rather
// than being an empty room someone has to interpret.
func createProjectChannel(ws *workspace.Workspace) *workspace.SlackChannel {
	cfg := config.Load(ws.RepoDir())
	if !cfg.Slack.Enabled || !cfg.Slack.CreateChannelPerProject {
		return nil
	}
	c, err := slack.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: slack enabled but not usable: %v\n", err)
		return nil
	}
	id, err := c.AuthTest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: slack: %v\n", err)
		return nil
	}
	ch, err := c.CreateChannel(cfg.Slack.ChannelPrefix+ws.Task.Slug, cfg.Slack.Private)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: could not create the project channel: %v\n", err)
		return nil
	}
	_ = c.SetTopic(ch.ID, truncateStr(ws.Task.Idea, 240))
	_ = c.Post(ch.ID, fmt.Sprintf(
		"*%s*\n%s\n\nWorkspace `%s`. Orion will report stage results, failures, "+
			"budget checkpoints and quota waits here.\n"+
			"Drive it with `orion run %s --stage <stage>`; `orion status %s` for state.",
		ch.Name, ws.Task.Idea, ws.ID, ws.ID, ws.ID))

	verb := "created"
	if !ch.Created {
		verb = "reusing"
	}
	fmt.Printf("slack      #%s (%s)\n", ch.Name, verb)
	return &workspace.SlackChannel{
		ID: ch.ID, Name: ch.Name, TeamID: id.TeamID,
		URL: slack.ChannelURL(id.TeamID, ch.ID),
	}
}

func runSupervised(id string, rest []string) {
	ws, err := workspace.Open(id)
	exitOn(err)
	opts := supervisor.Options{
		Stage:      argFlag(rest, "--stage", "intent"),
		Prompt:     argFlag(rest, "--prompt", ""),
		MaxMinutes: intFlag(rest, "--max-minutes", 0),
		MaxTurns:   intFlag(rest, "--max-turns", 0),
		DryRun:     hasFlag(rest, "--dry-run"),
		NoWait:     hasFlag(rest, "--no-wait"),
	}
	res, err := supervisor.Run(ws, opts)
	if res != nil {
		fmt.Printf("\nstage      %s\nexit       %d\nreason     %s\nattempts   %d\nduration   %s\nlog        %s\n",
			opts.Stage, res.ExitCode, res.Reason, res.Attempts,
			res.Duration.Round(time.Second), res.LogPath)
		if !res.ResumeAt.IsZero() {
			fmt.Printf("resume     %s (orion run %s --stage %s)\n",
				res.ResumeAt.Local().Format("15:04 MST"), ws.ID, opts.Stage)
		}
	}
	exitOn(err)
}

// refreshLessons regenerates the managed lessons block in the project's
// CLAUDE.md. Runs at SessionStart so cross-project memory is present
// before the agent reads anything, and returns a note rather than an
// error: a lessons failure must never block a session.
func refreshLessons(root string) string {
	store := lessons.New(workspace.Home())
	records, err := store.Load()
	if err != nil || len(records) == 0 {
		return ""
	}
	// Promotions are evidence-driven and are logged so the scope change is
	// itself auditable rather than silent.
	for _, p := range store.AutoPromote(records) {
		_ = store.Append(p.Lesson)
	}
	records, _ = store.Load()

	applicable := lessons.Applicable(records, filepath.Base(root), detectStack(root))
	if len(applicable) == 0 {
		return ""
	}
	if err := lessons.Inject(filepath.Join(root, "CLAUDE.md"), lessons.Render(applicable)); err != nil {
		return "could not refresh lessons block: " + err.Error()
	}
	return fmt.Sprintf("loaded %d carried-over lesson(s) into CLAUDE.md", len(applicable))
}

// detectStack identifies the project's ecosystem from its manifest, which
// is what decides whether a stack-scoped lesson applies here.
func detectStack(root string) string { return lessons.DetectStack(root) }

func runReset(args []string) {
	sessionID := argFlag(args, "--session", "")
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "orion: reset needs --session <id> (the id is printed in the block message)")
		os.Exit(64)
	}
	root, err := config.FindRoot(".")
	exitOn(err)
	cfg := config.Load(root)
	exitOn(state.New(cfg.StateDir()).Reset(sessionID))
	fmt.Printf("orion: breaker cleared for session %s\n", sessionID)
}

func runFix(action string) {
	root, err := config.FindRoot(".")
	exitOn(err)
	cfg := config.Load(root)
	marker := filepath.Join(cfg.StateDir(), hook.FixModeMarker)

	switch action {
	case "start":
		exitOn(os.MkdirAll(cfg.StateDir(), 0o755))
		exitOn(os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644))
		fmt.Println("orion: fix mode ON. Test files are now read-only to the agent.")
		fmt.Println("       Write the failing test first, confirm it fails for the right reason, then fix the code.")
	case "end":
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			exitOn(err)
		}
		fmt.Println("orion: fix mode OFF.")
	default:
		fmt.Fprintln(os.Stderr, "orion: fix takes start or end")
		os.Exit(64)
	}
}

// runProvision creates the remote repository, its branch model and the
// tracker project. Everything here touches systems outside the sandbox, so
// each destructive step confirms first and each is idempotent.
func runProvision(id string, rest []string) {
	ws, err := workspace.Open(id)
	exitOn(err)
	cfg := config.Load(ws.RepoDir())

	yes := hasFlag(rest, "--yes")
	confirm := func(prompt string) bool {
		if yes {
			fmt.Println(prompt + " [--yes]")
			return true
		}
		fmt.Printf("%s [y/N] ", prompt)
		var answer string
		fmt.Scanln(&answer)
		return strings.EqualFold(strings.TrimSpace(answer), "y")
	}

	// 1. Remote repository and branch model.
	if !hasFlag(rest, "--skip-repo") {
		res, err := provision.Remote(provision.Options{
			Dir:           ws.RepoDir(),
			Name:          ws.Task.Slug,
			Description:   truncateStr(ws.Task.Idea, 200),
			DefaultBranch: cfg.VCS.DefaultBranch,
			WorkBranch:    cfg.VCS.WorkBranch,
			Private:       true,
			Org:           argFlag(rest, "--org", ""),
			Confirm:       confirm,
			Out:           os.Stdout,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "orion: repository provisioning failed: %v\n", err)
		} else {
			fmt.Print(res.Summary())
			ws.Task.Remote = res.RemoteURL
			_ = ws.SaveTask()
		}
	}

	// 2. Tracker project. Separated from the repo step so a Jira failure
	// does not strand a repository that was created successfully.
	if !hasFlag(rest, "--skip-tracker") {
		runTrackerProvision(ws, cfg, confirm)
	}

	fmt.Printf("\nnext: orion run %s --stage plan\n", ws.ID)
}

func runTrackerProvision(ws *workspace.Workspace, cfg config.Config, confirm func(string) bool) {
	j, err := tracker.NewJiraFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: skipping tracker (%v)\n", err)
		return
	}
	cap, err := j.Probe()
	if err != nil || !cap.Authenticated {
		fmt.Fprintf(os.Stderr, "orion: skipping tracker (%s)\n", cap.Detail)
		return
	}

	what := "Create a new Jira project for %q?"
	if cfg.Tracker.ProjectKey != "" {
		what = "Bind to existing Jira project " + cfg.Tracker.ProjectKey + " for %q?"
	}
	if !confirm(fmt.Sprintf(what, truncateStr(ws.Task.Idea, 60))) {
		fmt.Println("orion: tracker step cancelled")
		return
	}

	b, note, err := tracker.Provision(j, ws.Task.Slug, ws.Task.Idea,
		cfg.Tracker.ProjectKey, cap.AccountID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		return
	}
	raw, _ := json.Marshal(b)
	ws.Task.Tracker = raw
	_ = ws.SaveTask()

	fmt.Printf("tracker    %s (%s)\n", b.Key, note)
	fmt.Printf("           %s/browse/%s\n", b.BaseURL, b.Key)
	// The decomposition is the agent's job, and it stays behind a human
	// approval even under auto-merge: a sandboxed workspace can be deleted,
	// a shared tracker cannot.
	fmt.Println("\nNext, decompose the plan into issues. The whole tree is previewed")
	fmt.Println("for one approval before anything is created:")
	// `orion routes` in the prompt, not the table itself: a copy of the
	// vocabulary pasted here is a copy that drifts from the one routing
	// actually reads (OR-191).
	fmt.Printf("  claude -p \"Run 'orion routes' first and set the marker it names on every item. "+
		"Then use /pm-plan to decompose plans/*.plan.md into %s. Preview the full tree and wait for approval.\"\n", b.Key)
}

// runReport prints the digest, and optionally sends it.
//
// Exit code is 1 when something needs a human. That is what makes it usable
// from cron without a wrapper: a silent success stays silent, and only a
// real problem produces mail.
func runReport(args []string) {
	since := time.Now().Add(-parseDuration(argFlag(args, "--since", "7d"), 7*24*time.Hour))
	cfg := config.Load(rootOrCwd())

	// The same filter vocabulary as `orion logs`: a project key, an issue key
	// or a workspace id. Two commands describing the same work should not
	// disagree about how to name it.
	only := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		only = args[0]
	}
	d := report.Build(workspace.Home(), since,
		budget.Limits{WeeklyUSD: cfg.Budget.WeeklyUSD, WeeklyTokens: cfg.Budget.WeeklyTokens}, only)

	text := d.Text()
	fmt.Print(text)

	if hasFlag(args, "--notify") {
		level := notify.Info
		if !d.Healthy() {
			level = notify.Warning
		}
		title := "orion: all clear"
		if !d.Healthy() {
			title = fmt.Sprintf("orion: %d failure(s), %d needing attention",
				len(d.Failures), len(d.Attention))
		}
		// Send even when healthy if asked: a heartbeat that only appears on
		// failure is indistinguishable from a broken cron job.
		if errs := notify.Send(notify.Event{Level: level, Title: title, Body: text}); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "orion: notify: %v\n", e)
			}
		}
	}
	if !d.Healthy() {
		os.Exit(1)
	}
}

// runLogs shows the tail of a workspace's runs, which is what someone wants
// when a stage failed: not the whole transcript, the end of it.
// runLogs shows Orion's own event stream by default, and the raw agent
// transcript only when asked.
//
// Two logs, and the default matters. The transcript is tens of thousands of
// tokens per run; the events are a dozen lines saying what Orion did. Showing
// the transcript first buries the line anyone actually wants -- which ticket
// was claimed, what the architect answered, whether CI passed -- inside a
// wall of tool output.
//
// The target may be a project key (FCIA), an issue key (FCIA-6) or a
// workspace id. A key is what a person has in their hand; requiring the
// workspace id would mean looking it up first, every time.
func runLogs(args []string) {
	target := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
		args = args[1:]
	}
	if hasFlag(args, "--transcript") {
		runTranscript(target, args)
		return
	}
	runEventLog(target, args)
}

// resolveWorkspace turns a project key, an issue key or a workspace id into
// a workspace, trying the registry first because that is what a person will
// type.
func resolveWorkspace(target string) (*workspace.Workspace, string, error) {
	if target == "" {
		return nil, "", fmt.Errorf("which project? try: orion logs FCIA")
	}
	if e, err := registry.Lookup(workspace.Home(), target); err == nil {
		ws, wErr := workspace.Open(e.Workspace)
		return ws, e.Key, wErr
	}
	ws, err := workspace.Open(target)
	if err != nil {
		return nil, "", fmt.Errorf("no project or workspace matches %q.\n"+
			"  Registered projects: orion repos\n  Workspaces: orion ls", target)
	}
	return ws, "", nil
}

func runEventLog(target string, args []string) {
	w := os.Stdout
	ws, _, err := resolveWorkspace(target)
	exitOn(err)

	path := events.Path(ws.Dir)
	all, err := events.Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(w, "no events yet for %s.\n  They appear once a run starts: orion work <KEY>\n", ws.ID)
			return
		}
		exitOn(err)
	}

	// The globally configured roster (OR-132), so a renamed agent renders
	// with the name the operator chose -- the same roster on every project,
	// not one orion.json repeats per checkout. A bad roster is fatal here
	// rather than ignored: two agents sharing one name destroys exactly the
	// thing this output exists to provide.
	agents, err := config.LoadAgents(workspace.Home())
	exitOn(err)
	exitOn(actors.Configure(agents))

	// Filter to one ticket when an issue key was given, so `orion logs
	// FCIA-6` is about that ticket rather than everything the project did.
	if key := registry.NormalizeKey(target); strings.Contains(key, "-") {
		var kept []events.Event
		for _, e := range all {
			if strings.EqualFold(e.Key, key) {
				kept = append(kept, e)
			}
		}
		all = kept
	}

	// --actor selects by the STABLE identifier, never by display name.
	//
	// The rendered line says "backend developer", which is what a person
	// wants and what a script must not depend on: a team that renames the
	// developer would break every script written against the old name. The
	// identifier is persisted, unchanging, and is what this filter reads --
	// so `--actor implementer` keeps working across any rename.
	if id := strings.TrimSpace(argFlag(args, "--actor", "")); id != "" {
		var kept []events.Event
		for _, e := range all {
			if strings.EqualFold(e.Actor, id) {
				kept = append(kept, e)
			}
		}
		all = kept
	}

	n := intFlag(args, "--tail", 50)
	if len(all) > n {
		all = all[len(all)-n:]
	}
	for _, e := range all {
		printEvent(w, e)
	}

	if !hasFlag(args, "--follow") && !hasFlag(args, "-f") {
		return
	}
	fi, statErr := os.Stat(path)
	var from int64
	if statErr == nil {
		from = fi.Size()
	}
	fmt.Fprintln(w, ui.Dim(w, "  following; ctrl-c to stop"))
	stop := make(chan struct{})
	exitOn(events.Follow(path, from, time.Second, stop, func(e events.Event) {
		printEvent(w, e)
	}))
}

// printEvent renders one line of history through the SAME renderer the live
// watch uses (internal/ui). Two formatters over one event stream drift, and
// the drift shows up as the same run reading differently depending on which
// command you happened to type.
//
// The stored actor identifier is what is rendered FROM, never what is
// stored: a log written before the roster existed renders with the current
// names, and renaming an agent later migrates nothing.
func printEvent(w io.Writer, e events.Event) {
	// A stage boundary is not a status line and must not read back as one:
	// replaying it through the five-verb layout would put it back in the
	// columns it was deliberately taken out of, and dump its from/to detail
	// as loose key: value lines underneath. Same renderer, same shape as the
	// live run printed (OR-189).
	if e.Kind == events.KindStage {
		fmt.Fprintln(w, ui.RenderStage(w, ui.HandoffOf(e)))
		return
	}
	ui.Print(w, ui.Line{
		At: e.At, Key: e.Key, Actor: e.Actor, Model: e.Model,
		Verb: ui.VerbFor(e.Kind), Msg: e.Msg,
	})
	for k, v := range e.Detail {
		fmt.Fprintf(w, "           %s\n", ui.Dim(w, fmt.Sprintf("%s: %v", k, v)))
	}
}

func runTranscript(target string, args []string) {
	ws, _, err := resolveWorkspace(target)
	exitOn(err)
	logs, err := report.LogsFor(ws)
	exitOn(err)
	if len(logs) == 0 {
		fmt.Printf("no run logs yet for %s\n", ws.ID)
		return
	}
	n := intFlag(args, "--tail", 40)
	count := intFlag(args, "--runs", 1)
	if count > len(logs) {
		count = len(logs)
	}
	for i := 0; i < count; i++ {
		fmt.Printf("=== %s\n", filepath.Base(logs[i]))
		tail, tailErr := report.TailLog(logs[i], n)
		if tailErr != nil {
			fmt.Fprintf(os.Stderr, "  unreadable: %v\n", tailErr)
			continue
		}
		fmt.Println(tail)
		fmt.Println()
	}
}

// parseDuration accepts 7d, 24h, 90m. time.ParseDuration has no day unit,
// and a report window is most naturally expressed in days.
func parseDuration(s string, fallback time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
		return fallback
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return fallback
}

// runBudget reports and acknowledges the rolling weekly budget.
//
// Worth restating wherever this surfaces: the percentage is against a limit
// the user configured, never against the Anthropic plan's weekly allowance.
// Nothing reports the latter, so presenting one would be a fabrication.
func runBudget(args []string) {
	home := workspace.Home()
	cfg := config.Load(rootOrCwd())
	lim := budget.Limits{WeeklyUSD: cfg.Budget.WeeklyUSD, WeeklyTokens: cfg.Budget.WeeklyTokens}
	ledger, err := budget.Load(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
	}

	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}

	switch sub {
	case "status":
		st := ledger.Status(lim)
		if !lim.Set() {
			fmt.Println("no weekly budget configured.")
			fmt.Println("Set budget.weekly_usd or budget.weekly_tokens in orion.json to")
			fmt.Println("enable the 50/75/90/95% checkpoints. Until then Orion accounts")
			fmt.Println("spend but never stops for it.")
		}
		fmt.Printf("window     last 7 days (%d runs)\n", st.Runs)
		fmt.Printf("spend      $%.2f", st.SpentUSD)
		if lim.WeeklyUSD > 0 {
			fmt.Printf(" of $%.2f (%d%%)", lim.WeeklyUSD, st.PercentUSD)
		}
		fmt.Println()
		fmt.Printf("tokens     %d", st.Tokens)
		if lim.WeeklyTokens > 0 {
			fmt.Printf(" of %d (%d%%)", lim.WeeklyTokens, st.PercentTok)
		}
		fmt.Println()
		if st.Crossed > 0 {
			fmt.Printf("\nCHECKPOINT %d%% reached and not acknowledged.\n", st.Crossed)
			fmt.Printf("Runs are stopped until: orion budget ack %d\n", st.Crossed)
		}
		// Total consumption across EVERY Claude Code session, not just the
		// runs Orion supervised. A budget that ignored the interactive
		// session you spent the morning in would measure the wrong thing.
		if tu, scanErr := budget.ScanTranscripts(budget.TranscriptDir(), st.WindowStart); scanErr == nil && tu.Turns > 0 {
			fmt.Printf("\nall Claude Code usage in the same window\n")
			fmt.Printf("  sessions   %d (%d turns, %d from subagents)\n", tu.Sessions, tu.Turns, tu.Sidechain)
			fmt.Printf("  input      %s new, %s cache write, %s cache read\n",
				human(tu.Tokens.Input), human(tu.Tokens.CacheCreation), human(tu.Tokens.CacheRead))
			fmt.Printf("  output     %s\n", human(tu.Tokens.Output))
			fmt.Printf("  raw total  %s\n", human(tu.Tokens.Total()))
			fmt.Printf("  effective  %s  (cache reads 0.1x, writes 2x, output 5x)\n", human(tu.Tokens.Effective()))
			if tu.Skipped > 0 {
				fmt.Printf("  NOTE       %d transcript(s) unreadable; the total is partial\n", tu.Skipped)
			}
		}

		if runs := ledger.Recent(5); len(runs) > 0 {
			fmt.Println("\nrecent")
			for _, r := range runs {
				fmt.Printf("  %s  %-10s $%.4f  %d in / %d out\n",
					r.At.Local().Format("Jan 02 15:04"), r.Stage, r.CostUSD,
					r.InputTokens, r.OutputTokens)
			}
		}

	case "ack":
		st := ledger.Status(lim)
		pct := st.Crossed
		if len(args) > 0 {
			if n, convErr := strconv.Atoi(args[0]); convErr == nil {
				pct = n
			}
		}
		if pct == 0 {
			fmt.Println("nothing to acknowledge.")
			return
		}
		// Acknowledge everything at or below, so returning after a long gap
		// does not stop the next four runs in a row.
		//
		// Re-read under lock rather than saving the copy loaded above: a
		// watcher may have recorded runs while the operator was reading the
		// status, and saving this stale snapshot would discard them (OR-138).
		exitOn(budget.Update(home, func(l *budget.Ledger) { l.AckAll(pct) }))
		fmt.Printf("acknowledged the %d%% checkpoint. Runs may continue.\n", pct)
		fmt.Println("The next checkpoint will stop again; this is consent for one step, not the rest.")

	default:
		fmt.Fprintf(os.Stderr, "orion: unknown budget subcommand %q (status|ack)\n", sub)
		os.Exit(64)
	}
}

// runNJAgents inspects and refreshes the delegated toolkit.
//
// nj-agents is developed independently of Orion, so its improvements reach
// Orion only if something fetches them. This is that something.
func runNJAgents(args []string) {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	home := workspace.Home()
	cfg := config.Load(rootOrCwd())
	inst := njagents.Discover(cfg.Delegation.NJAgentsDir, home)

	switch sub {
	case "status":
		if inst == nil {
			fmt.Println("nj-agents  NOT FOUND")
			fmt.Println("fetch it:  orion doctor --fix")
			fmt.Println("or:        " + njagents.CloneCommand(home))
			os.Exit(1)
		}
		fmt.Printf("root       %s\nfound via  %s\ncommit     %s\n", inst.Root, inst.Via, inst.Commit)
		if inst.Dirty {
			fmt.Println("tree       MODIFIED (local changes present)")
		}
		fmt.Printf("owner      %s\n", ownerLabel(inst))
		if behind, known := njagents.Refreshed(inst); known && behind > 0 {
			fmt.Printf("stale      %d commit(s) behind origin as of the last fetch\n", behind)
			fmt.Println("           run: orion njagents update")
		} else if known {
			fmt.Println("stale      up to date as of the last fetch")
		}
		for _, m := range inst.Missing {
			fmt.Printf("MISSING    %s\n", m)
		}
		for _, w := range inst.Warnings {
			fmt.Printf("WARNING    %s\n", w)
		}

	case "update":
		res, err := njagents.Update(inst, cfg.Delegation.NJAgentsRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "orion: %v\n", err)
			os.Exit(1)
		}
		if res.Skipped != "" {
			fmt.Println("skipped: " + res.Skipped)
			return
		}
		if res.Changed {
			fmt.Printf("updated    %s -> %s\n", res.From, res.To)
		} else {
			fmt.Println("already up to date at " + res.From)
		}

	case "install":
		dir := argFlag(args, "--project", "")
		if dir == "" {
			fmt.Fprintln(os.Stderr, "orion njagents install --project <dir>")
			os.Exit(64)
		}
		if inst != nil && !inst.Managed {
			// A global install is already visible to every claude run, so
			// wiring it into a directory achieves nothing and leaves symlinks
			// behind that someone has to clean up later.
			fmt.Printf("nj-agents is already installed globally at %s.\n", inst.Root)
			fmt.Println("Every claude run can see those skills, so a per-project install")
			fmt.Println("is unnecessary. Nothing to do.")
			return
		}
		// Running a third-party installer is a different consent level from
		// reading files, so it is never a side effect of another command.
		fmt.Println("This runs the toolkit's own installer:")
		fmt.Println("  " + njagents.InstallCommand(inst, dir))
		out, err := njagents.InstallInto(inst, dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "orion: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(out)

	default:
		fmt.Fprintf(os.Stderr, "orion: unknown njagents subcommand %q (status|update|install)\n", sub)
		os.Exit(64)
	}
}

func human(n int) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func ownerLabel(i *njagents.Install) string {
	if i.Managed {
		return "Orion's own clone (orion njagents update maintains it)"
	}
	return "your global install (Orion reads it; you update it)"
}

func rootOrCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// runLessons manages the cross-project memory. Deliberately human-facing:
// an automatic memory nobody can inspect or retire is a liability, because
// a wrong lesson silently misdirects every future project.
func runLessons(args []string) {
	store := lessons.New(workspace.Home())
	action := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		args = args[1:]
	}

	switch action {
	case "add":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, `orion: usage: orion lessons add "<the lesson>" [--kind correction|breaker|review|incident]`)
			os.Exit(64)
		}
		project := argFlag(args, "--project", "")
		if project == "" {
			if root, err := config.FindRoot("."); err == nil {
				project = filepath.Base(root)
			} else {
				project = "unknown"
			}
		}
		l := lessons.Lesson{
			Text:    args[0],
			Kind:    lessons.Kind(argFlag(args, "--kind", string(lessons.KindCorrection))),
			Project: project,
			Stack:   detectStackHere(),
		}
		exitOn(store.Append(l))
		fmt.Printf("orion: recorded, scoped to project %q.\n", project)
		fmt.Println("       It reaches other projects only if it recurs in one.")

	case "list":
		records, err := store.Load()
		exitOn(err)
		health, herr := store.Health()
		printLessons(os.Stdout, records, health, herr)

	case "pending":
		cs, err := store.Pending()
		exitOn(err)
		if len(cs) == 0 {
			fmt.Printf("nothing is waiting for a decision (a lesson is only offered after %d sightings)\n",
				lessons.Strikes)
			return
		}
		for _, c := range cs {
			fmt.Printf("%s  seen %dx in %s\n  %s\n", c.Signature, c.Strikes,
				strings.Join(c.Projects, ", "), c.Text)
			for _, e := range c.Evidence {
				fmt.Printf("    - %s\n", e)
			}
			fmt.Printf("  orion lessons approve %s   |   orion lessons reject %s\n\n", c.Signature, c.Signature)
		}

	case "approve", "reject":
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "orion: usage: orion lessons %s <signature>   (see: orion lessons pending)\n", action)
			os.Exit(64)
		}
		d := lessons.DecisionApproved
		if action == "reject" {
			d = lessons.DecisionRejected
		}
		c, err := store.Decide(args[0], d, argFlag(args, "--by", "you"))
		exitOn(err)
		if d == lessons.DecisionRejected {
			fmt.Printf("orion: rejected. It will not be proposed again.\n  %s\n", c.Text)
			return
		}
		fmt.Printf("orion: recorded, scoped to %s.\n  %s\n", strings.Join(c.Projects, ", "), c.Text)
		fmt.Println("       It reaches other projects only if it recurs in one.")

	case "retire":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, `orion: usage: orion lessons retire "<exact lesson text>"`)
			os.Exit(64)
		}
		// Retirement is an append, not a delete. The log stays complete so
		// the provenance of a rule the agent once followed is recoverable.
		exitOn(store.Append(lessons.Lesson{Text: args[0], Retired: true, Project: "manual"}))
		fmt.Println("orion: retired. It will stop being injected into CLAUDE.md.")

	default:
		fmt.Fprintf(os.Stderr,
			"orion: unknown lessons action %q (want: add, list, pending, approve, reject, retire)\n", action)
		os.Exit(64)
	}
}

// printLessons renders `orion lessons list`.
//
// The trailing health line is the point of this function. An empty store has
// two very different causes that look identical from the outside -- nothing
// worth recording has happened, or nothing is writing to it -- and for this
// store's entire life it was the second, reporting cleanly the whole time. A
// subsystem that answers "nothing" the same way whether it is idle or broken
// is one nobody ever goes to look at.
func printLessons(w io.Writer, records []lessons.Record, health lessons.Health, herr error) {
	if len(records) > 0 {
		fmt.Fprintf(w, "%-8s %-5s %-12s %-24s %s\n", "SCOPE", "HITS", "LAST", "PROJECTS", "LESSON")
		for _, r := range records {
			mark := ""
			if r.Retired {
				mark = " (retired)"
			}
			fmt.Fprintf(w, "%-8s %-5d %-12s %-24s %s%s\n", r.Scope, r.Hits,
				r.LastSeen.Local().Format("2006-01-02"),
				truncateStr(strings.Join(r.Projects, ","), 23), truncateStr(r.Text, 70), mark)
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "no lessons recorded yet")
	}
	switch {
	case herr != nil:
		fmt.Fprintf(w, "could not read the proposal log: %v\n", herr)
	case !health.Observed():
		fmt.Fprintln(w, "nothing has ever been OBSERVED either, so no automatic path has")
		fmt.Fprintln(w, "written to this store. If runs have completed since then, lesson")
		fmt.Fprintln(w, "proposal is not reaching it -- an empty store is not the same as a")
		fmt.Fprintln(w, "quiet one.")
	default:
		fmt.Fprintf(w, "%d event(s) observed, most recently %s.\n",
			health.Sightings, health.LastObserved.Local().Format("2006-01-02"))
		if health.Pending > 0 {
			fmt.Fprintf(w, "%d proposal(s) waiting for your decision: orion lessons pending\n", health.Pending)
		}
	}
}

func detectStackHere() string {
	root, err := config.FindRoot(".")
	if err != nil {
		return ""
	}
	return detectStack(root)
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// --- tiny flag helpers. Not using the flag package because subcommands
// with positional args plus pass-through flags get awkward there, and the
// surface here is small enough that hand-parsing stays readable.

func argFlag(args []string, name, def string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return def
}

// positional returns the arguments that are NOT flags and not the value of
// a flag.
//
// A naive "anything not starting with -" filter reads `--max-jobs 1` as a
// flag and a positional `1`, so `orion watch fcia --max-jobs 1` watched two
// projects: FCIA and "1". The value belongs to the flag before it.
//
// takesValue lists the flags that consume the next argument. Boolean flags
// must NOT appear, or a positional after one is silently swallowed.
func positional(args []string, takesValue ...string) []string {
	consumes := map[string]bool{}
	for _, f := range takesValue {
		consumes[f] = true
	}
	var out []string
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		if strings.HasPrefix(a, "-") {
			// --flag=value carries its own value; --flag takes the next one.
			if consumes[a] {
				skip = true
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func intFlag(args []string, name string, def int) int {
	s := argFlag(args, name, "")
	if s == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func mustArg(args []string, idx int, usageLine string) {
	if len(args) <= idx {
		fmt.Fprintf(os.Stderr, "orion: usage: %s\n", usageLine)
		os.Exit(64)
	}
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		os.Exit(1)
	}
}
