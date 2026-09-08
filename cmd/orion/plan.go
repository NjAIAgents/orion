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
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/dbaplan"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/provision"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// planStage is one link in the chain this command drives.
type planStage struct {
	Stage string // the --stage name supervisor.Run already understands
	Actor string // who does it, for the roster announcement
	What  string // one line, for a reader deciding whether to let it run

	// Frame, when set, runs this step in Orion's own process -- no claude
	// run, no budget checkpoint, no cost line. It is for the deterministic
	// work between stages (installing the toolkit, creating the remote,
	// creating the tracker tree, cloning) that used to be a separate command
	// the operator had to remember, in the right order, after the chain.
	// Held in the same slice as the stages so the roster, the cost shape
	// and the dispatch loop all read one list and none can omit a step the
	// others know about. Actor is events.ActorOrion for these, so the roster
	// says who does it without listing Orion as a participant on a model.
	Frame func(out io.Writer, ws *workspace.Workspace, ask confirmer) error
	// Fallback marks a frame step that may decline -- errNotApplicable --
	// when the artifact it works from is absent, in which case the
	// supervised stage of the same name runs instead. Counted as a stage
	// that may spend, and reachable by `orion run --stage`.
	Fallback bool
	// Done, when set, reports that this step's work is already there, so a
	// resumed chain prints it as done and moves on without asking. Derived
	// from the step's own artifact wherever there is one -- a flag can say
	// done about a file that was never committed; the file cannot.
	Done func(ws *workspace.Workspace) bool
}

// supervisedStages counts the steps that spend: one claude run each. Frame
// steps are in the chain and not in this number, which is why the cost
// shape reads it rather than len(planStages).
func supervisedStages() int {
	n := 0
	for _, s := range planStages {
		if s.Frame == nil || s.Fallback {
			n++
		}
	}
	return n
}

// planStages is the chain, in order.
//
// docs/decisions/0006 names spec, plan, scaffold and the tracker tree as the
// stages running after the interactive phase: "one ambiguous premise there
// propagates into spec, plan, scaffold and the tracker tree". It puts intent
// in `orion new`, where a human is present to be asked -- but `orion new`
// only ever wrote those answers to the tracker, never to the repository the
// stages read, so the first of them designed from a file nobody had written.
// Intent runs here instead, as the stage that turns what was said into the
// artifact the rest of the chain reads.
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
	// The toolkit comes before everything, and spends nothing: spec-kit is
	// installed into the repository once, by `specify init`, from templates
	// bundled in its CLI, so that the first stage delegating to it finds
	// its commands where Claude Code reads them (docs/decisions/0022). A
	// project whose stages name no spec-kit command has nothing to install
	// and the step reports done.
	{Stage: "toolkit", Actor: events.ActorOrion, What: "spec-kit installed into the repository, once",
		Frame: toolkitStep, Done: toolkitDone},
	// Intent comes first among the STAGES because every stage after it
	// reads what it wrote.
	//
	// It was missing from this chain, and the chain began at spec -- which
	// opens by reading docs/intent/<slug>.md, a file nothing had ever
	// created. FOUND ON A REAL PROJECT: the spec stage looked for the intent,
	// did not find it, correctly refused to invent a product from the name
	// alone, and committed 26KB explaining why. The answers it needed were
	// sitting in the tracker's project description, because `orion new` puts
	// them there and no stage brought them into the repository.
	//
	// The stage itself was already built -- supervisor's prompt for it, the
	// discovery gate that reads its open questions, `orion answer` that walks
	// them. Only its place in the chain was missing.
	{Stage: "intent", Actor: events.ActorPM, What: "what is being built and why, captured from the idea", Done: stageDone("intent")},
	// Between intent and spec: spec-kit's every later command reads the
	// constitution, and it is seeded from the intent's constraints, so it
	// can be written no earlier and is wanted no later.
	{Stage: "constitution", Actor: events.ActorArchitect, What: "project principles for spec-kit, seeded from orion.json gates and the intent", Done: stageDone("constitution")},
	{Stage: "spec", Actor: events.ActorArchitect, What: "requirements and design spec", Done: stageDone("spec")},
	{Stage: "plan", Actor: events.ActorArchitect, What: "implementation plan: files, order of work, tests, risks", Done: stageDone("plan")},
	// After plan and before anything is built from it: a read-only check
	// that spec, plan and tasks agree with each other and the constitution.
	// It owes no file, so its Done is the recorded verdict of its last run.
	{Stage: "analyze", Actor: events.ActorArchitect, What: "read-only consistency check of spec, plan and tasks; blocks on critical issues", Done: stageDone("analyze")},
	{Stage: "scaffold", Actor: events.ActorDevOps, What: "repository skeleton on the OpenSSF baseline", Done: stageDone("scaffold")},
	// The remote comes AFTER scaffold and BEFORE decompose. After scaffold,
	// because creating a repository on GitHub is outward and irreversible
	// enough to want every gate before it passed first -- a chain stopped
	// at the discovery gate has created nothing anybody has to delete.
	// Before decompose, because the tickets name branches and a repository
	// to push to, and `orion watch` needs the remote to open pull requests
	// against. It used to be `orion provision`, a separate command typed
	// after the chain; the chain runs it now (docs/decisions/0022).
	{Stage: "remote", Actor: events.ActorOrion, What: "the GitHub repository: create it, push main and develop, protect both",
		Frame: remoteStep, Done: remoteDone},
	// Native when the plan stage left a tasks.md -- Orion creates the tree
	// itself, stamping the queue label so `orion watch` can claim it -- and
	// the supervised /pm-plan stage otherwise. One entry with both, because
	// which one runs is a property of the artifact, not of the roster.
	{Stage: "decompose", Actor: events.ActorPM, What: "the Epic, Story and Task tree in the tracker",
		Frame: decomposeStep, Fallback: true, Done: decomposeDone},
	// Opt-in, and free when opted into: the version every ticket in the
	// tree is attached to, so `orion release status` never reports the
	// tree as orphans. Skipped, and done, without --release.
	{Stage: "release", Actor: events.ActorOrion, What: "the tracker version the tree is attached to (--release vX.Y.Z)",
		Frame: releaseStep, Done: releaseDone},
	// Last, and best effort: the operator's own copy, once there is
	// something committed worth copying. In the chain rather than after it
	// so a resume can retry a clone that failed.
	{Stage: "clone", Actor: events.ActorOrion, What: "your own copy of the repository, where you asked for it",
		Frame: cloneStep, Done: cloneDone},
}

// toolkitStep installs spec-kit into the workspace repository, once.
func toolkitStep(out io.Writer, ws *workspace.Workspace, _ confirmer) error {
	did, err := provision.InitSpecKit(ws.RepoDir())
	if err != nil {
		return err
	}
	if did {
		ui.Ok(out, "installed", "spec-kit into %s", ws.RepoDir())
	} else {
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, "spec-kit is already installed"))
	}
	return nil
}

// toolkitDone: nothing to do for a project that delegates nothing to
// spec-kit; otherwise done when the installer's own directory is there.
func toolkitDone(ws *workspace.Workspace) bool {
	if !config.Load(ws.RepoDir()).Toolkit.DelegatesTo("speckit") {
		return true
	}
	st, err := os.Stat(filepath.Join(ws.RepoDir(), provision.SpecKitDir))
	return err == nil && st.IsDir()
}

// planFromIndex resolves --from to an index into planStages, or -1 when
// nothing was asked for. An unknown name is an error that lists the steps,
// because the alternative -- treating it as "from the start" -- would make
// a typo re-run the whole chain at full cost.
func planFromIndex(from string) (int, error) {
	from = strings.TrimSpace(from)
	if from == "" {
		return -1, nil
	}
	names := make([]string, 0, len(planStages))
	for i, s := range planStages {
		if strings.EqualFold(s.Stage, from) {
			return i, nil
		}
		names = append(names, s.Stage)
	}
	return -1, fmt.Errorf("--from %q is not a step of the chain.\n  Steps: %s", from, strings.Join(names, ", "))
}

// stageDone is the Done predicate for a supervised stage: the artifact gate
// and the run record, read by supervisor.StageDone. One closure per entry
// rather than a switch in the chain, so the slice stays the only list.
func stageDone(stage string) func(*workspace.Workspace) bool {
	return func(ws *workspace.Workspace) bool { return supervisor.StageDone(ws, stage) }
}

// nextPlanStage returns the stage that follows the one given, and whether
// there is one.
//
// Reads planStages rather than repeating the order, for the reason declared
// above it: an order written out twice is an order that eventually disagrees
// with itself. Unknown stage names -- the work stages, which are not part of
// the planning chain -- return false and get no suggestion, which is correct:
// this only knows about the planning chain.
func nextPlanStage(stage string) (planStage, bool) {
	for i, s := range planStages {
		if strings.EqualFold(s.Stage, stage) {
			// The next SUPERVISED step. A frame step is not something
			// `orion run --stage` can run, so suggesting it would name a
			// command that refuses; the chain itself runs frame steps.
			for _, next := range planStages[i+1:] {
				if next.Frame == nil || next.Fallback {
					return next, true
				}
			}
			return planStage{}, false
		}
	}
	return planStage{}, false
}

type planOptions struct {
	Key    string
	DryRun bool
	Home   string
	Out    io.Writer
	// Org is the GitHub organisation for the remote step, from --org.
	// Recorded on the task when given, so a resume needs no flag.
	Org string
	// From names a step to re-run from, regardless of whether it and the
	// steps after it are done. Done derived from artifacts is right by
	// default and wrong when the operator has edited the spec and wants
	// everything downstream rebuilt from it.
	From string
	// Release is the tracker version to attach the tree to, from --release.
	// Recorded on the task when given, so a resume needs no flag.
	Release string
	// Run and Confirm drive the stage chain. Both nil means announce only --
	// which is what a non-interactive caller gets, and what every existing
	// test of this command already exercises.
	Run     stageRunner
	Confirm confirmer
	// Ask reads one free-text answer. Nil means no terminal, so nothing that
	// needs typing is offered.
	Ask func(prompt string) string
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

	o := planOptions{
		Key:     key,
		DryRun:  hasFlag(args[1:], "--dry-run"),
		Home:    home,
		Out:     os.Stdout,
		Org:     argFlag(args[1:], "--org", ""),
		From:    argFlag(args[1:], "--from", ""),
		Release: argFlag(args[1:], "--release", ""),
	}
	// A dry run spends nothing and dispatches nothing, so it must not offer
	// to. Off a terminal there is nobody to answer the pauses.
	if !o.DryRun && isTerminal(os.Stdin) {
		r := bufio.NewReader(os.Stdin)
		o.Confirm = func(prompt string) bool { return askYesNo(r, os.Stdout, prompt) }
		o.Ask = func(prompt string) string {
			answer, _ := ask(r, os.Stdout, prompt)
			return answer
		}
		o.Run = func(ws *workspace.Workspace, stage string) (*supervisor.Result, error) {
			// Narrated. A stage is minutes of silence otherwise, which reads
			// as a hang and gets a working run killed halfway.
			prog := newStageProgress(os.Stdout)
			defer prog.Close()
			return supervisor.Run(ws, supervisor.Options{
				Stage:      stage,
				OnActivity: prog.On,
			})
		}
	}
	exitOn(planRun(j, config.Load(rootOrCwd()), o))
}

// planRun is the whole command, with the tracker and the destination injected
// so a test can drive it without Jira and without reading os.Stdout.
func planRun(pr projectReader, cfg config.Config, opts planOptions) error {
	out := opts.Out
	if opts.Key == "" {
		return fmt.Errorf("orion plan needs a tracker project key, e.g. orion plan ORPAY")
	}
	// A wrong --from fails here, before the tracker is read or anything is
	// provisioned: a typo is cheapest to find at the prompt.
	if _, err := planFromIndex(opts.From); err != nil {
		return err
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

	// Where the remote goes, recorded once so the remote step and every
	// resume after it agree. A dry run has no task to write to.
	if opts.Org != "" && !opts.DryRun && ws.Task.RemoteOrg != opts.Org {
		ws.Task.RemoteOrg = opts.Org
		if err := ws.SaveTask(); err != nil {
			return err
		}
	}
	if opts.Release != "" && !opts.DryRun && ws.Task.ReleaseVersion != opts.Release {
		ws.Task.ReleaseVersion = opts.Release
		if err := ws.SaveTask(); err != nil {
			return err
		}
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
	// Run the chain when there is someone to pause for. Without a terminal
	// -- a script, CI, a pipe -- there is nobody to answer the confirmations
	// this chain is built around, so it announces the first command and stops
	// exactly as it always did.
	if opts.Run != nil && opts.Confirm != nil {
		if !opts.Confirm(fmt.Sprintf("Run all %d stages, pausing after each?", len(planStages))) {
			fmt.Fprintf(out, "\nnext: orion run %s --stage %s\n", ws.ID, planStages[0].Stage)
			return nil
		}
		// Where their own copy should go, asked once, acted on at the end.
		// Only when nothing has been recorded already, so a resumed chain
		// does not ask again.
		if opts.Ask != nil && strings.TrimSpace(ws.Task.CheckoutPath) == "" {
			if p := askCheckoutPath(out, opts.Ask); p != "" {
				ws.Task.CheckoutPath = p
				if err := ws.SaveTask(); err != nil {
					ui.Warn(out, "could not record where to clone: %v", err)
				}
			}
		}

		done := runPlanChainFrom(out, ws, opts.Run, opts.Confirm, opts.From)
		if done == len(planStages) {
			fmt.Fprintf(out, "\n%s\n", ui.Dim(out,
				"all planning stages are done; the tracker holds the work tree"))
			fmt.Fprintf(out, "next: orion watch %s\n", strings.ToUpper(opts.Key))
		}
		return nil
	}

	fmt.Fprintf(out, "next: orion run %s --stage %s\n", ws.ID, planStages[0].Stage)
	return nil
}

// ideaKeyFromDescription reads the provenance marker `orion new` writes.
//
// Only the FIRST line, and only when it is the whole line: a description that
// mentions another ticket in passing is not a statement about where this
// project came from, and treating it as one would point a stage at the wrong
// idea.
func ideaKeyFromDescription(desc string) string {
	first := strings.TrimSpace(desc)
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = strings.TrimSpace(first[:i])
	}
	rest, ok := strings.CutPrefix(first, "From ")
	if !ok {
		return ""
	}
	// "From PRIOR-3 (https://...)" -- the key is the first field. A bare
	// "From " with nothing after it has no fields at all.
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	key := fields[0]
	if !looksLikeIdeaKey(key) {
		return ""
	}
	return strings.ToUpper(key)
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
		// THE SAME PROJECT RESUMES. docs/decisions/0012 refused a second run
		// because it would plan again into a half-finished workspace; with
		// each step now deriving whether it is done from its own artifact,
		// a re-run skips what is there and picks up where the last one
		// stopped. A DIFFERENT project whose name slugified alike still
		// refuses, naming the owner: that is a name clash, and 0012's
		// reason for it holds unchanged. A missing binding refuses too --
		// "probably the same project" is not a basis for writing into it.
		if planBoundTo(existing) == p.Key {
			fmt.Fprintln(out, ui.Heading(out, "Workspace"))
			fmt.Fprintf(out, "  id           %s  %s\n", existing.ID, ui.Dim(out, "(resumed)"))
			fmt.Fprintf(out, "  path         %s\n", existing.Dir)
			fmt.Fprintf(out, "  repo         %s\n", existing.RepoDir())
			printPlanProgress(out, existing, opts.From)
			fmt.Fprintln(out)
			return existing, nil
		}
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
	// The discovery idea this came from, if the description says. `orion new`
	// writes "From PRIOR-3" at the top for an idea given by key and for one
	// it filed from an interview, so a stage can be TOLD which idea to fill
	// in rather than reading orion's own help output looking for a key.
	ws.Task.IdeaKey = ideaKeyFromDescription(p.Description)

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

// planBoundTo is the tracker key the workspace records, or "" when it
// records none.
func planBoundTo(ws *workspace.Workspace) string {
	var b tracker.Binding
	if len(ws.Task.Tracker) > 0 && json.Unmarshal(ws.Task.Tracker, &b) == nil {
		return b.Key
	}
	return ""
}

// printPlanProgress says where a resumed chain is: which steps are done and
// will be skipped, and which will run -- including any --from forces. This
// is what a dry run of a resumable workspace reports, and what a real run
// shows before asking to continue, so "resumed" is never a word without a
// position behind it.
func printPlanProgress(out io.Writer, ws *workspace.Workspace, from string) {
	fromIdx, _ := planFromIndex(from)
	for i, s := range planStages {
		state := "would run"
		switch {
		case fromIdx >= 0 && i >= fromIdx:
			state = "would run (--from " + from + ")"
		case s.Done != nil && s.Done(ws):
			state = "done"
		}
		fmt.Fprintf(out, "  %d/%d %-10s %s\n", i+1, len(planStages), s.Stage, ui.Dim(out, state))
	}
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

	// Frame steps are in the chain and not in this number: they run in
	// Orion's own process and spend nothing, and a cost line that counted
	// them would estimate a run that never happens.
	supervised := supervisedStages()
	fmt.Fprintln(out, ui.Heading(out, "Cost shape"))
	fmt.Fprintf(out, "  shape        %d sequential stages, one supervised claude run each; no fix loop\n",
		supervised)
	if est.CostUSD > 0 {
		fmt.Fprintf(out, "  estimate     $%.2f per run (mean of %d runs in the last 7 days) -- about $%.2f for the chain\n",
			est.CostUSD, st.Runs, est.CostUSD*float64(supervised))
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
