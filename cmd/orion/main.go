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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/discovery"
	"github.com/orion-sdlc/orion/internal/doctor"
	"github.com/orion-sdlc/orion/internal/hook"
	"github.com/orion-sdlc/orion/internal/lessons"
	"github.com/orion-sdlc/orion/internal/njagents"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/provision"
	"github.com/orion-sdlc/orion/internal/report"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/state"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Version is set at build time: -ldflags "-X main.Version=$(git describe)".
var Version = "dev"

const usage = `orion - AI-native SDLC orchestrator

ADOPT AN EXISTING REPO
  orion init [--plan-gate]    config, artifact dirs and hooks, idempotent

WORKSPACES
  orion new "<idea>"          provision an isolated project workspace
                              (--skip-discovery to bypass the intent conversation)
  orion answer <id>           resolve the open questions blocking a workspace
  orion ls                    list workspaces
  orion open <id>             print a workspace path (use with cd)
  orion rm <id>               remove a workspace

RUNNING
  orion provision <id>        create the remote repo, branches, and tracker
  orion run <id> [--stage S]  supervise a sandboxed claude run in a workspace
  orion status <id>           show stage, breaker state and last run

GUARDRAILS
  orion doctor [--fix]        preflight: tools, auth, sandbox, config
                              --fix fetches nj-agents if it is missing
  orion reset --session <id>  clear a tripped breaker after human review
  orion fix start|end         mark a bug fix, protecting the failing test

DEPENDENCIES
  orion njagents status       where nj-agents is, which commit, how stale
  orion njagents update       fast-forward Orion's own clone, if it has one
  orion njagents install      wire Orion's clone into a dir (only if no global)

MONITORING
  orion report [--since 7d]   digest: failures, workspaces, budget, usage
  orion report --notify       also send it to ORION_NOTIFY_WEBHOOK (Slack)
  orion logs <id> [--tail n]  the failing tail of a workspace's runs

BUDGET (rolling 7 days, your limit, not your plan's)
  orion budget status         spend, tokens and the next checkpoint
  orion budget ack [pct]      confirm a checkpoint and continue

MEMORY (shared across every project)
  orion lessons add "<text>"  record a correction so it is not repeated
  orion lessons list          show what Orion has learned, and its scope
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

	switch os.Args[1] {
	case "hook":
		runHook(os.Args[2:])
	case "doctor":
		os.Exit(doctor.Run(os.Stdout, argFlag(os.Args[2:], "--path", "."), hasFlag(os.Args[2:], "--fix")))
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
	case "run":
		mustArg(os.Args, 2, "orion run <id>")
		runSupervised(os.Args[2], os.Args[3:])
	case "status":
		mustArg(os.Args, 2, "orion status <id>")
		exitOn(workspace.Status(os.Stdout, os.Args[2]))
	case "reset":
		runReset(os.Args[2:])
	case "fix":
		mustArg(os.Args, 2, "orion fix start|end")
		runFix(os.Args[2])
	case "njagents", "nj-agents":
		runNJAgents(os.Args[2:])
	case "report":
		runReport(os.Args[2:])
	case "logs":
		mustArg(os.Args, 2, "orion logs <id>")
		runLogs(os.Args[2], os.Args[3:])
	case "budget":
		runBudget(os.Args[2:])
	case "lessons":
		runLessons(os.Args[2:])
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

func runNew(idea string, rest []string) {
	opts := workspace.NewOptions{
		Idea:      idea,
		Template:  argFlag(rest, "--template", ""),
		FromRepo:  argFlag(rest, "--from", ""),
		Container: hasFlag(rest, "--container"),
	}
	ws, err := workspace.New(opts)
	exitOn(err)

	// The project channel, if Slack is configured. Failure here is reported
	// and never fatal: a workspace that exists without a channel is usable,
	// while refusing to provision because Slack was unreachable is not.
	if ch := createProjectChannel(ws); ch != nil {
		ws.Task.Slack = ch
		_ = ws.SaveTask()
	}

	fmt.Printf("workspace  %s\n", ws.ID)
	fmt.Printf("path       %s\n", ws.Dir)
	fmt.Printf("repo       %s\n", ws.RepoDir())
	fmt.Printf("sandbox    %s\n", ws.SandboxMode())
	needs, reason := discovery.NeedsDiscovery(idea)
	switch {
	case hasFlag(rest, "--skip-discovery"):
		fmt.Printf("\nnext: orion run %s --stage intent\n", ws.ID)
	case !needs:
		fmt.Printf("\ndiscovery skipped: %s\n", reason)
		fmt.Printf("next: orion run %s --stage intent\n", ws.ID)
	default:
		fmt.Printf("\nDiscovery first: %s\n\n", reason)
		fmt.Println("The intent stage runs non-interactively, so it cannot ask you anything.")
		fmt.Println("Have the conversation now, while changing course still means editing a")
		fmt.Println("sentence rather than nine stages of derived work:")
		fmt.Println()
		fmt.Printf("  cd %s\n", ws.RepoDir())
		fmt.Println("  claude \"/capture-intent\"")
		fmt.Println()
		fmt.Println("Then continue. Any question left open will block the spec stage until")
		fmt.Printf("it is answered (orion answer %s).\n", ws.ID)
		fmt.Println()
		fmt.Printf("Skip this next time with: orion new \"...\" --skip-discovery\n")
	}
}

// runInit adopts the current repository.
func runInit(args []string) {
	dir := argFlag(args, "--dir", ".")
	abs, err := filepath.Abs(dir)
	exitOn(err)

	bin, lookErr := os.Executable()
	if lookErr != nil {
		bin = "orion"
	} else if resolved, symErr := filepath.EvalSymlinks(bin); symErr == nil {
		bin = resolved
	}

	res, err := adopt.Run(adopt.Options{
		Dir:      abs,
		Binary:   bin,
		PlanGate: hasFlag(args, "--plan-gate"),
		Force:    hasFlag(args, "--force"),
	})
	if res != nil {
		fmt.Print(res.Summary())
	}
	exitOn(err)

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
func detectStack(root string) string {
	for file, stack := range map[string]string{
		"go.mod": "go", "package.json": "node", "Cargo.toml": "rust",
		"pyproject.toml": "python", "requirements.txt": "python",
		"pom.xml": "java", "build.gradle": "java", "Gemfile": "ruby",
	} {
		if _, err := os.Stat(filepath.Join(root, file)); err == nil {
			return stack
		}
	}
	return ""
}

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
	fmt.Printf("  claude -p \"Use /pm-plan to decompose plans/*.plan.md into %s. Preview the full tree and wait for approval.\"\n", b.Key)
}

// runReport prints the digest, and optionally sends it.
//
// Exit code is 1 when something needs a human. That is what makes it usable
// from cron without a wrapper: a silent success stays silent, and only a
// real problem produces mail.
func runReport(args []string) {
	since := time.Now().Add(-parseDuration(argFlag(args, "--since", "7d"), 7*24*time.Hour))
	cfg := config.Load(rootOrCwd())
	d := report.Build(workspace.Home(), since,
		budget.Limits{WeeklyUSD: cfg.Budget.WeeklyUSD, WeeklyTokens: cfg.Budget.WeeklyTokens})

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
func runLogs(id string, args []string) {
	ws, err := workspace.Open(id)
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
		ledger.AckAll(pct)
		exitOn(ledger.Save(home))
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
		if len(records) == 0 {
			fmt.Println("no lessons recorded yet")
			return
		}
		fmt.Printf("%-8s %-5s %-28s %s\n", "SCOPE", "HITS", "PROJECTS", "LESSON")
		for _, r := range records {
			mark := ""
			if r.Retired {
				mark = " (retired)"
			}
			fmt.Printf("%-8s %-5d %-28s %s%s\n", r.Scope, r.Hits,
				truncateStr(strings.Join(r.Projects, ","), 27), truncateStr(r.Text, 70), mark)
		}

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
		fmt.Fprintf(os.Stderr, "orion: unknown lessons action %q (want: add, list, retire)\n", action)
		os.Exit(64)
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
