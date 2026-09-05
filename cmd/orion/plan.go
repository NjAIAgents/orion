package main

// `orion plan <KEY>` is the front door to the async design phase.
//
// It is the second of the two sequential phases docs/decisions/0006 describes:
// `orion new` has the interactive conversation and `orion provision` leaves a
// tracker project behind as the handoff artifact, and this reads that project
// back rather than re-deriving anything from the original idea text.
//
// This file is the FRAME, not the work. It reads the project, provisions the
// workspace, announces what it would dispatch and what that costs, and stops.
// Dispatch itself lands in later tickets of the same epic, against the roster
// printed here -- which is the point of declaring planStages in one place: the
// announcement and the dispatch read the same list, so they cannot drift.
//
// Three orderings in here are load-bearing rather than incidental.
//
// THE WORKSPACE IS PROVISIONED FIRST, before the roster is announced and long
// before anything spends. Everything downstream writes into a workspace; a run
// that starts work and provisions later has, for that window, nowhere isolated
// to put what it produces except the directory the user happened to be in.
//
// THE BUDGET CHECKPOINT IS CHECKED LAST, immediately before the handoff to
// dispatch. It gates SPENDING, and provisioning a directory spends nothing --
// so refusing before the workspace exists would withhold the free part of the
// command over the cost of the part that is not free. Checked here, an
// unacknowledged checkpoint stops the dispatch and leaves a workspace the user
// can carry straight into `orion budget ack` and a re-run.
//
// --dry-run IS READ-ONLY THROUGHOUT. It creates no workspace, not just no
// agent: "prints what it would do" is not a claim a command can make while
// leaving a directory tree behind.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/dbaplan"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// planStage is one link in the chain this command drives.
type planStage struct {
	Stage string // the --stage name supervisor.Run already understands
	Actor string // who does it, for the roster announcement
	What  string // one line, for a reader deciding whether to let it run
}

// planStages is the chain, in order.
//
// The four stages are the ones docs/decisions/0006 names as running after the
// interactive phase: "one ambiguous premise there propagates into spec, plan,
// scaffold and the tracker tree". Intent is absent because 0006 puts it in
// `orion new`, where a human is present to be asked.
//
// Declared here rather than inside the announcement so that the dispatch a
// later ticket adds iterates this same slice. A roster that is written out by
// hand next to a dispatch loop is a roster that eventually describes a run
// that no longer happens.
//
// The stage strings are supervisor's vocabulary, owned by stagePrompt's switch
// -- a name not in it is refused there by name, so a typo here surfaces as
// "unknown stage" on the first dispatch rather than as silence.
var planStages = []planStage{
	{"spec", events.ActorArchitect, "requirements and design spec"},
	{"plan", events.ActorArchitect, "implementation plan: files, order of work, tests, risks"},
	{"scaffold", events.ActorDevOps, "repository skeleton on the OpenSSF baseline"},
	{"decompose", events.ActorPM, "the Epic, Story and Task tree in the tracker"},
}

type planOptions struct {
	Key    string
	DryRun bool
	Home   string
	Out    io.Writer
}

// projectReader is the slice of tracker.Tracker this command needs.
//
// Narrow on purpose: the whole Tracker interface would drag project CREATION
// into a command that must never create one, and would make the test double
// implement three methods to exercise a path that calls one.
type projectReader interface {
	Project(key string) (tracker.Project, error)
}

func runPlan(args []string) {
	key := strings.ToUpper(strings.TrimSpace(args[0]))

	// The globally configured roster (docs/decisions/0005), so the
	// announcement names the actors by whatever the operator called them.
	home := workspace.Home()
	agents, err := config.LoadAgents(home)
	exitOn(err)
	exitOn(actors.Configure(agents))

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	exitOn(planRun(j, config.Load(rootOrCwd()), planOptions{
		Key:    key,
		DryRun: hasFlag(args[1:], "--dry-run"),
		Home:   home,
		Out:    os.Stdout,
	}))
}

// planRun is the whole command, with the tracker and the destination injected
// so a test can drive it without Jira and without reading os.Stdout.
func planRun(pr projectReader, cfg config.Config, opts planOptions) error {
	out := opts.Out
	if opts.Key == "" {
		return fmt.Errorf("orion plan needs a tracker project key, e.g. orion plan ORPAY")
	}

	// 1. The handoff artifact.
	p, err := pr.Project(opts.Key)
	if err != nil {
		return err
	}
	if p.Name == "" {
		return fmt.Errorf("project %s has no name, so there is nothing to derive a slug from.\n"+
			"  Give it a name in the tracker and re-run", opts.Key)
	}

	// 2. ONE canonical slug, derived once from the finalised project name and
	// reused for the workspace and the git repo (docs/decisions/0009). The
	// Jira key is NOT re-derived here: it already exists, it is what was
	// looked up, and Jira's key charset could not hold this slug anyway.
	slug := workspace.Slugify(p.Name)

	fmt.Fprintln(out, ui.Heading(out, "Project"))
	fmt.Fprintf(out, "  key          %s\n", p.Key)
	fmt.Fprintf(out, "  name         %s\n", p.Name)
	fmt.Fprintf(out, "  slug         %s  %s\n", slug,
		ui.Dim(out, "(names the workspace and the git repo)"))
	fmt.Fprintf(out, "  description  %s\n", orNone(p.Description))
	fmt.Fprintln(out)

	// 3. The workspace, before anything else runs.
	ws, err := planWorkspace(out, p, slug, opts)
	if err != nil {
		return err
	}

	// 4. What would be dispatched, and what it costs, before it is.
	printPlanRoster(out, ws.ID, planIdea(p))
	st, budgetSet := printPlanCostShape(out, cfg, opts.Home)

	// 5. The checkpoint, last, immediately before the handoff.
	if budgetSet && st.Crossed > 0 {
		fmt.Fprintln(out)
		fmt.Fprint(out, st.Message())
		if opts.DryRun {
			// A dry run reports; it does not fail. Its exit code answers "did
			// the inspection work", and turning it into "would the run have
			// proceeded" would make a correctly-reported checkpoint look like
			// a broken command.
			fmt.Fprintf(out, "\n  %s\n", ui.Dim(out,
				"--dry-run: a real run would stop here, having spent nothing"))
			return nil
		}
		return fmt.Errorf("stopped at the %d%% budget checkpoint; nothing was dispatched", st.Crossed)
	}

	fmt.Fprintln(out)
	if opts.DryRun {
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, "--dry-run: nothing was created and nothing was spent"))
		fmt.Fprintf(out, "  run it: orion plan %s\n", p.Key)
		return nil
	}
	fmt.Fprintf(out, "next: orion run %s --stage %s\n", ws.ID, planStages[0].Stage)
	return nil
}

// planWorkspace provisions the workspace, or -- on a dry run -- reports the
// one it would have provisioned.
//
// The collision is refused rather than reused or suffixed
// (docs/decisions/0012). Refusing needs the existing workspace named, which is
// why this looks before it creates instead of leaning on workspace.New's own
// check: New cannot say which tracker project the squatter belongs to, and
// "already exists" without that is a dead end when the two are different
// projects whose names happen to slugify alike.
func planWorkspace(out io.Writer, p tracker.Project, slug string, opts planOptions) (*workspace.Workspace, error) {
	if existing, err := workspace.Open(slug); err == nil && existing.ID == slug {
		return nil, fmt.Errorf("workspace %s already exists for %s.\n"+
			"  A tracker project gets ONE workspace, so this will not create a second.\n"+
			"  Continue in it:      orion run %s --stage %s\n"+
			"  Or start over:       orion rm %s",
			existing.ID, planExistingOwner(existing, p.Key), existing.ID, planStages[0].Stage, existing.ID)
	}

	idea := planIdea(p)

	if opts.DryRun {
		fmt.Fprintln(out, ui.Heading(out, "Workspace"))
		fmt.Fprintf(out, "  would create %s\n", slug)
		fmt.Fprintf(out, "  under        %s\n", workspace.Home())
		fmt.Fprintln(out)
		return &workspace.Workspace{ID: slug}, nil
	}

	ws, err := workspace.New(workspace.NewOptions{Idea: idea, Slug: slug})
	if err != nil {
		return nil, err
	}

	// Record what was read, so the workspace knows which project owns it and
	// a second `orion plan` can say so. Created:false -- this bound to a
	// project that already existed, and claiming otherwise would misreport who
	// is responsible for a project nobody can delete.
	raw, err := json.Marshal(tracker.Binding{
		Provider: "jira", ProjectID: p.ID, Key: p.Key, Name: p.Name,
		Created: false, BoundAt: time.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	ws.Task.Tracker = raw
	ws.Task.Stage = planStages[0].Stage

	// The project channel follows the workspace, which is now born here rather
	// than in `orion new` (docs/decisions/0013). Failure is reported and never
	// fatal: a workspace without a channel is usable, while refusing to
	// provision because Slack was unreachable is not.
	if ch := createProjectChannel(ws); ch != nil {
		ws.Task.Slack = ch
	}
	if err := ws.SaveTask(); err != nil {
		return nil, err
	}

	fmt.Fprintln(out, ui.Heading(out, "Workspace"))
	fmt.Fprintf(out, "  id           %s\n", ws.ID)
	fmt.Fprintf(out, "  path         %s\n", ws.Dir)
	fmt.Fprintf(out, "  repo         %s\n", ws.RepoDir())
	fmt.Fprintf(out, "  sandbox      %s\n", ws.SandboxMode())
	fmt.Fprintln(out)
	return ws, nil
}

// planExistingOwner names the project the existing workspace is bound to,
// which is the difference between "you already ran this" and "a different
// project's name slugified to the same thing".
func planExistingOwner(ws *workspace.Workspace, wantKey string) string {
	var b tracker.Binding
	if len(ws.Task.Tracker) > 0 && json.Unmarshal(ws.Task.Tracker, &b) == nil && b.Key != "" {
		if b.Key != wantKey {
			return b.Key + " -- a DIFFERENT project to the " + wantKey + " you asked for"
		}
		return b.Key
	}
	return wantKey + " (unverified: that workspace records no tracker binding)"
}

// planIdea is what the project says the work is, and it is the text the
// roster's own signals are read out of (OR-150).
//
// The description first; the name is the honest fallback when nobody filled
// one in. Never the key: "ORPAY" tells a later stage prompt nothing it can
// design from, and it holds no signal either.
func planIdea(p tracker.Project) string {
	if p.Description != "" {
		return p.Description
	}
	return p.Name
}

// printPlanRoster is CONVENTIONS-orchestration §R: every agent, what it will
// do, before dispatch.
//
// A subagent reports once, at the end, so the spawning command is the only
// place a multi-agent run can be made legible. This is the sequential shape
// §R distinguishes -- each stage feeds the next -- so it prints the chain,
// and a stall is attributable to a named stage rather than to "it is thinking".
//
// Then the actors the IDEA selected, each beside the word that selected it. A
// roster that varies between two runs without saying why is as bad as one that
// cannot vary at all: the reader cannot tell a considered choice from a bug,
// which is exactly how the frontend developer stayed unreachable for a release
// while every run looked correct (internal/work/route.go, OR-191).
func printPlanRoster(out io.Writer, wsID, idea string) {
	fmt.Fprintln(out, ui.Heading(out, "Roster"))
	fmt.Fprintf(out, "  %s\n", ui.Dim(out, "sequential: each stage reads what the one before it committed"))

	stageW, whoW := 0, 0
	who := make([]string, len(planStages))
	for i, s := range planStages {
		who[i] = actors.Display(s.Actor)
		if n := len([]rune(s.Stage)); n > stageW {
			stageW = n
		}
		if n := len([]rune(who[i])); n > whoW {
			whoW = n
		}
	}
	for i, s := range planStages {
		fmt.Fprintf(out, "  %d. %-*s  %-*s  %-6s  %s\n", i+1,
			stageW, s.Stage, whoW, who[i], orNone(actors.Model(s.Actor)), ui.Dim(out, s.What))
	}
	printPlanSelected(out, wsID, idea)
	fmt.Fprintln(out)
}

// planActorStages are the planning steps an actor runs only when the idea
// selects it.
//
// NOT planStages, and the difference is the whole reason there are two lists.
// planStages is the chain every project pays for; these run when the project
// has the thing they are about. The database architect is the first: choosing
// a database for a project that stores nothing is a run nobody should be
// billed for, and the roster already decides -- for free, from the idea's own
// words -- whether this project is one of those (OR-150, OR-154).
//
// Announced with the command that runs it, because a selected actor that is
// named and never invoked is indistinguishable from one that was announced by
// mistake.
var planActorStages = map[string]string{events.ActorDBA: dbaplan.Stage}

// printPlanSelected names the actors the idea itself put on this run, and the
// signal that put each one there.
//
// NEVER SILENT, the rule internal/work/route.go states for the same reason: an
// idea that selects nobody is a normal outcome, and a run that prints nothing
// in that case is indistinguishable from selection having failed to run at all.
func printPlanSelected(out io.Writer, wsID, idea string) {
	var chosen []planActor
	for _, a := range planRoster(idea) {
		if a.FromIdea {
			chosen = append(chosen, a)
		}
	}
	if len(chosen) == 0 {
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, fmt.Sprintf(
			"the idea names no other actor, so the %d stages above are the whole roster",
			len(planStages))))
		return
	}

	fmt.Fprintf(out, "  %s\n", ui.Dim(out, "also on this run, selected by the idea:"))
	whoW := 0
	who := make([]string, len(chosen))
	for i, a := range chosen {
		who[i] = actors.Display(a.ID)
		if n := len([]rune(who[i])); n > whoW {
			whoW = n
		}
	}
	for i, a := range chosen {
		fmt.Fprintf(out, "     %-*s  %-6s  %s\n",
			whoW, who[i], orNone(actors.Model(a.ID)), ui.Dim(out, a.Signal))
		if stage, ok := planActorStages[a.ID]; ok {
			fmt.Fprintf(out, "     %-*s  %-6s  %s\n", whoW, "", "",
				ui.Dim(out, "orion run "+wsID+" --stage "+stage))
		}
	}
}

// printPlanCostShape is CONVENTIONS-orchestration §C: the cost shape, stated
// before anything spends, and returned so the caller can gate on it.
//
// The per-run figure is MEASURED -- the mean of the runs actually recorded in
// the rolling window (budget.Ledger.Estimate) -- and is absent rather than
// invented when there is no history. A confident number derived from nothing
// is worse than "no history yet": it is the same number every time, so it
// reads as a measurement and never corrects itself.
func printPlanCostShape(out io.Writer, cfg config.Config, home string) (budget.Status, bool) {
	lim := budget.Limits{WeeklyUSD: cfg.Budget.WeeklyUSD, WeeklyTokens: cfg.Budget.WeeklyTokens}
	ledger, err := budget.Load(home)
	if err != nil && ledger == nil {
		ledger = &budget.Ledger{}
	}
	st := ledger.Status(lim)
	est := ledger.Estimate()

	fmt.Fprintln(out, ui.Heading(out, "Cost shape"))
	fmt.Fprintf(out, "  shape        %d sequential stages, one supervised claude run each; no fix loop\n",
		len(planStages))
	if est.CostUSD > 0 {
		fmt.Fprintf(out, "  estimate     $%.2f per run (mean of %d runs in the last 7 days) -- about $%.2f for the chain\n",
			est.CostUSD, st.Runs, est.CostUSD*float64(len(planStages)))
	} else {
		fmt.Fprintf(out, "  estimate     %s\n", ui.Dim(out,
			"no run history in the window, so there is nothing to estimate from"))
	}

	switch {
	case !lim.Set():
		fmt.Fprintf(out, "  budget       %s\n", ui.Dim(out,
			"none configured (budget.weekly_usd in orion.json); spend is accounted, never stopped for"))
	case lim.WeeklyUSD > 0:
		fmt.Fprintf(out, "  budget       $%.2f of $%.2f used (%d%%) this week\n",
			st.SpentUSD, lim.WeeklyUSD, st.PercentUSD)
	default:
		fmt.Fprintf(out, "  budget       %d of %d tokens used (%d%%) this week\n",
			st.Tokens, lim.WeeklyTokens, st.PercentTok)
	}
	fmt.Fprintf(out, "  %s\n", ui.Dim(out,
		"the budget is the one you configured, not your Anthropic plan's weekly limit"))
	return st, lim.Set()
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}
