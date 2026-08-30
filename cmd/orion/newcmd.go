package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/creds"
	"github.com/orion-sdlc/orion/internal/discovery"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// `orion new` is the interactive front half: elaborate the idea, finalise the
// name, create the tracker project (OR-148).
//
// It is the only place in Orion that may hold a synchronous conversation,
// because it is the only place a human is present by definition. Every stage
// after it runs through `claude -p` and cannot ask anything, which is why
// internal/discovery's other half -- the gate -- exists at all. Here the
// question gets asked instead of gated.
//
// The workspace is still provisioned here rather than deferred to the plan
// phase. That was the open question on the ticket, and it is settled in
// docs/decisions/0012: the tracker is optional everywhere else in Orion and
// may simply not be configured, so a `new` that wrote nothing locally would
// hold the one conversation in the system and then have nowhere to put the
// answer. The Jira project remains the handoff artifact; the workspace is
// what makes the exchange survive its absence.

func runNew(idea string, rest []string) {
	intake, asked := elaborate(idea, rest)

	ws, err := workspace.New(workspace.NewOptions{
		Idea:        idea,
		Name:        intake.Name,
		Description: intake.Description(),
		Template:    argFlag(rest, "--template", ""),
		FromRepo:    argFlag(rest, "--from", ""),
		Container:   hasFlag(rest, "--container"),
	})
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

	provisionTracker(ws)

	fmt.Printf("\nnext: orion run %s --stage intent\n", ws.ID)
	// Only when the questions were actually put. Listing them after
	// --skip-discovery would nag someone about a conversation they declined,
	// and a warning people learn to ignore stops working for the case it was
	// written for.
	if open := intake.OpenQuestions(); asked && len(open) > 0 {
		fmt.Printf("\n%d question(s) went unanswered and are recorded as open:\n", len(open))
		for _, q := range open {
			fmt.Printf("  - %s\n", q)
		}
		fmt.Println("\nDesigning from an unanswered question means inventing the answer,")
		fmt.Println("and every later stage inherits the invention. Answer them before spec.")
	}
}

// elaborate runs the interview, or explains why it did not.
//
// Two things can skip it and they are not the same thing. `--skip-discovery`
// is a person declining; no terminal is a person not being there, which is
// also the case under cron and in a test. Both fall back to the idea as typed
// and a name derived from it, because a command that blocked on a read that
// will never return would be worse than one that guessed the name.
func elaborate(idea string, rest []string) discovery.Intake {
	proposed := discovery.NameFromSlug(workspace.Slugify(idea))
	flat := discovery.Intake{Idea: idea, Name: proposed}

	needs, reason := discovery.NeedsDiscovery(idea)
	switch {
	case hasFlag(rest, "--skip-discovery"):
		fmt.Printf("discovery skipped: --skip-discovery\n\n")
		return flat
	case !creds.Interactive():
		fmt.Printf("discovery skipped: not a terminal, so there is nobody to ask\n\n")
		return flat
	case needs:
		fmt.Printf("Discovery first: %s\n\n", reason)
	default:
		fmt.Printf("%s, so only the name is asked for.\n\n", reason)
	}
	// needs doubles as "elaborate": an idea that already states its
	// constraints has had this conversation somewhere else, and asking again
	// is friction for no gain. The name is asked for either way -- a
	// detailed idea still arrives without a name anyone agreed to.
	return discovery.Interview(os.Stdin, os.Stdout, idea, proposed, needs)
}

// provisionTracker creates the project the plan phase will consume.
//
// Routed through adopt.RemotePlan.Describe(), which is the one
// describe-then-confirm gate in Orion and the only one that says out loud
// that a Jira project cannot be deleted without admin rights. A second
// confirmation pattern beside it would be a second place for that warning to
// go missing.
func provisionTracker(ws *workspace.Workspace) {
	j, err := tracker.NewJiraFromEnv()
	if err != nil {
		// Not configured is ordinary, and the elaborated description is on
		// the workspace either way. Say where it went so the absence is not
		// mistaken for a loss.
		fmt.Printf("tracker    not configured; the description is recorded in %s\n", ws.TaskPath())
		return
	}
	cap, err := j.Probe()
	if err != nil || !cap.Authenticated {
		fmt.Fprintf(os.Stderr, "orion: skipping tracker (%s)\n", cap.Detail)
		return
	}

	cfg := config.Load(ws.RepoDir())
	plan, err := newProjectPlan(j, j.BaseURL, ws.Task.Name, ws.Task.Slug, cfg.Tracker.ProjectKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		return
	}

	fmt.Println()
	fmt.Print(plan.Describe())
	if !plan.Nothing() && !confirm("Proceed?") {
		ui.Ok(os.Stdout, "skipped", "tracker project (create it later with orion provision %s)", ws.ID)
		return
	}

	// Jira rejects project creation without a lead, and says so only after
	// the confirmation has been given. The authenticated account is the
	// obvious one: whoever answered the questions owns the project.
	b, err := createProjectFromPlan(j, plan, ws.Task.Slug, cap.AccountID, ws.Task.Description)
	if err != nil {
		ui.Fail(os.Stdout, "Jira project %s: %v", plan.JiraKey, err)
		return
	}
	raw, _ := json.Marshal(b)
	ws.Task.Tracker = raw
	_ = ws.SaveTask()

	verb := "created"
	if !b.Created {
		verb = "bound"
	}
	ui.Ok(os.Stdout, verb, "Jira project %s  %s/browse/%s", b.Key, j.BaseURL, b.Key)
}

// newProjectPlan resolves what creating this project would actually do,
// without doing any of it.
//
// The key is resolved HERE, before the plan is described, so the key the
// human confirms is the key that gets created. Resolving it inside the
// creation step instead would let a collision suffix appear after the
// confirmation, which makes the gate a formality rather than a decision.
func newProjectPlan(t tracker.Tracker, site, name, slug, existingKey string) (adopt.RemotePlan, error) {
	plan := adopt.RemotePlan{ProjectName: name, JiraSite: site}
	if existingKey != "" {
		exists, _, err := t.ProjectExists(existingKey)
		if err != nil {
			return plan, err
		}
		if !exists {
			return plan, fmt.Errorf("configured tracker.project_key %q does not exist", existingKey)
		}
		plan.JiraKey, plan.JiraExists = existingKey, true
		return plan, nil
	}
	key, err := tracker.ResolveKey(t, tracker.DeriveKey(slug))
	if err != nil {
		return plan, err
	}
	plan.JiraKey = key
	return plan, nil
}

// createProjectFromPlan performs exactly what the confirmed plan described,
// and nothing the plan did not mention.
func createProjectFromPlan(t tracker.Tracker, plan adopt.RemotePlan, slug, lead, description string) (tracker.Binding, error) {
	if plan.JiraExists {
		b, _, err := tracker.Provision(t, slug, plan.ProjectName, description, plan.JiraKey, lead)
		return b, err
	}
	return t.CreateProject(plan.JiraKey, plan.ProjectName, lead, description)
}
