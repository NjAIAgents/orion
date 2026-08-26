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
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/doctor"
	"github.com/orion-sdlc/orion/internal/hook"
	"github.com/orion-sdlc/orion/internal/lessons"
	"github.com/orion-sdlc/orion/internal/njagents"
	"github.com/orion-sdlc/orion/internal/provision"
	"github.com/orion-sdlc/orion/internal/state"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Version is set at build time: -ldflags "-X main.Version=$(git describe)".
var Version = "dev"

const usage = `orion - AI-native SDLC orchestrator

WORKSPACES
  orion new "<idea>"          provision an isolated project workspace
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
	fmt.Printf("workspace  %s\n", ws.ID)
	fmt.Printf("path       %s\n", ws.Dir)
	fmt.Printf("repo       %s\n", ws.RepoDir())
	fmt.Printf("sandbox    %s\n", ws.SandboxMode())
	fmt.Printf("\nnext: orion run %s --stage intent\n", ws.ID)
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
