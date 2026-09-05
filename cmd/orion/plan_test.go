package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fakeProjects answers Project() from a map, and records what was asked, so a
// test can prove the command read the tracker rather than deriving from the
// key it was handed.
type fakeProjects struct {
	projects map[string]tracker.Project
	asked    []string
}

func (f *fakeProjects) Project(key string) (tracker.Project, error) {
	f.asked = append(f.asked, key)
	p, ok := f.projects[key]
	if !ok {
		return tracker.Project{}, fmt.Errorf("no project %s", key)
	}
	return p, nil
}

// orpay is the ticket's own worked example: a free-text NAME with a space in
// it, and a short uppercase KEY that could never hold that name.
func orpay() *fakeProjects {
	return &fakeProjects{projects: map[string]tracker.Project{
		"ORPAY": {
			ID: "10042", Key: "ORPAY", Name: "Orion Payments",
			Description: "Take card payments from the web app.",
		},
	}}
}

func planHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("ORION_HOME", h)
	return h
}

func runPlanInto(t *testing.T, pr projectReader, cfg config.Config, opts planOptions) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	opts.Out = &buf
	err := planRun(pr, cfg, opts)
	return buf.String(), err
}

// ONE NAME ACROSS ALL THREE (docs/decisions/0009). The workspace and the repo
// take a slug derived from the project's finalised NAME; the Jira key keeps
// its own derivation and is not re-derived here. Deriving the slug from the
// key instead would name the workspace "orpay", which is the mapping table
// 0009 exists to remove.
func TestPlanNamesTheWorkspaceFromTheProjectNameNotTheKey(t *testing.T) {
	home := planHome(t)
	out, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "projects", "orion-payments")); err != nil {
		t.Fatalf("no workspace named orion-payments: %v", err)
	}
	if strings.Contains(out, "orpay ") || strings.Contains(out, "/orpay") {
		t.Errorf("the slug looks derived from the key, not the name:\n%s", out)
	}
	for _, want := range []string{"ORPAY", "Orion Payments", "orion-payments"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not show %q, so a reader cannot see the three names line up:\n%s",
				want, out)
		}
	}
}

// The project is READ, not assumed. The description is what the later stages
// design from, so it has to reach the workspace's task record.
func TestPlanReadsTheProjectAndCarriesItsDescriptionIntoTheWorkspace(t *testing.T) {
	home := planHome(t)
	f := orpay()
	if _, err := runPlanInto(t, f, config.Config{}, planOptions{Key: "ORPAY", Home: home}); err != nil {
		t.Fatal(err)
	}
	if len(f.asked) != 1 || f.asked[0] != "ORPAY" {
		t.Errorf("tracker was asked %v, want exactly [ORPAY]", f.asked)
	}
	ws, err := workspace.Open("orion-payments")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Task.Idea != "Take card payments from the web app." {
		t.Errorf("Task.Idea = %q; the project description is what the stages design from",
			ws.Task.Idea)
	}
	var b tracker.Binding
	if err := json.Unmarshal(ws.Task.Tracker, &b); err != nil {
		t.Fatalf("no tracker binding recorded: %v", err)
	}
	if b.Key != "ORPAY" || b.ProjectID != "10042" {
		t.Errorf("binding = %+v, want ORPAY/10042", b)
	}
	if b.Created {
		t.Error("binding claims Orion created the project; it bound to one that existed")
	}
}

// PROVISION THE WORKSPACE FIRST, before anything else runs. Everything
// downstream writes into a workspace, so announcing a roster and a cost while
// none exists leaves that window with nowhere isolated to put its output.
func TestPlanProvisionsTheWorkspaceBeforeItAnnouncesAnything(t *testing.T) {
	home := planHome(t)
	out, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	ws := strings.Index(out, "Workspace")
	roster := strings.Index(out, "Roster")
	cost := strings.Index(out, "Cost shape")
	if ws < 0 || roster < 0 || cost < 0 {
		t.Fatalf("output is missing one of Workspace/Roster/Cost shape:\n%s", out)
	}
	if !(ws < roster && roster < cost) {
		t.Errorf("order is Workspace@%d Roster@%d Cost@%d; the workspace must come first:\n%s",
			ws, roster, cost, out)
	}
}

// CONVENTIONS-orchestration §R: announce the roster before dispatch -- every
// agent and what it will do -- and §C: state the cost shape. A run that
// dispatches without either is one nobody can judge before it spends.
func TestPlanAnnouncesEveryStageAndItsCostShapeBeforeDispatch(t *testing.T) {
	home := planHome(t)
	out, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(planStages) == 0 {
		t.Fatal("the roster is empty")
	}
	for _, s := range planStages {
		if !strings.Contains(out, s.Stage) {
			t.Errorf("stage %q is not announced:\n%s", s.Stage, out)
		}
		if !strings.Contains(out, s.What) {
			t.Errorf("stage %q is announced without saying what it does:\n%s", s.Stage, out)
		}
	}
	// The shape, not just the list: how many runs, and that it does not loop.
	if !strings.Contains(out, fmt.Sprint(len(planStages))) || !strings.Contains(out, "no fix loop") {
		t.Errorf("cost shape does not state the fleet size and loop shape:\n%s", out)
	}
	// The number is the user's own limit, never the provider's.
	if !strings.Contains(out, "not your Anthropic plan") {
		t.Errorf("the budget line does not disclaim whose limit it is:\n%s", out)
	}
}

// A REAL --dry-run: it prints what it would do and creates nothing. A dry run
// that leaves a workspace tree behind is not a dry run.
func TestPlanDryRunCreatesNothing(t *testing.T) {
	home := planHome(t)
	out, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "projects", "orion-payments")); !os.IsNotExist(err) {
		t.Errorf("--dry-run created a workspace (stat err = %v)", err)
	}
	// It still has to SAY what it would do, or it is only a no-op.
	for _, want := range []string{"would create", "orion-payments", "Roster", "Cost shape"} {
		if !strings.Contains(out, want) {
			t.Errorf("--dry-run output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "nothing was created and nothing was spent") {
		t.Errorf("--dry-run does not say it spent nothing:\n%s", out)
	}
}

// Running it twice must not silently do the same work twice.
func TestPlanDryRunIsRepeatableAndLeavesNothingBehind(t *testing.T) {
	home := planHome(t)
	for i := 0; i < 2; i++ {
		if _, err := runPlanInto(t, orpay(), config.Config{},
			planOptions{Key: "ORPAY", Home: home, DryRun: true}); err != nil {
			t.Fatalf("dry run %d: %v", i, err)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(home, "projects")); err == nil && len(entries) > 0 {
		t.Errorf("two dry runs left %d workspace(s) behind", len(entries))
	}
}

// docs/decisions/0012: a tracker project gets ONE workspace. The second call
// refuses, names the existing one, and says how to go forward -- it does not
// reuse it and does not create a suffixed twin.
func TestPlanRefusesASecondRunOnTheSameKey(t *testing.T) {
	home := planHome(t)
	if _, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home}); err != nil {
		t.Fatal(err)
	}
	_, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err == nil {
		t.Fatal("the second run succeeded; one project must map to one workspace")
	}
	for _, want := range []string{"orion-payments", "orion rm", "--stage"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q, so it is a dead end: %v", want, err)
		}
	}
	entries, readErr := os.ReadDir(filepath.Join(home, "projects"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 {
		t.Errorf("there are %d workspaces; the second run created a twin", len(entries))
	}
}

// A different project whose name slugifies alike is a NAME CLASH, not a
// repeated command, and the refusal has to say which it is or the user is
// sent to `orion rm` a workspace that belongs to other work.
func TestPlanRefusalNamesTheProjectThatActuallyOwnsTheWorkspace(t *testing.T) {
	home := planHome(t)
	if _, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home}); err != nil {
		t.Fatal(err)
	}
	other := &fakeProjects{projects: map[string]tracker.Project{
		"OPAY2": {ID: "10099", Key: "OPAY2", Name: "Orion Payments", Description: "Something else."},
	}}
	_, err := runPlanInto(t, other, config.Config{}, planOptions{Key: "OPAY2", Home: home})
	if err == nil {
		t.Fatal("a colliding name was allowed to take over another project's workspace")
	}
	if !strings.Contains(err.Error(), "ORPAY") || !strings.Contains(err.Error(), "DIFFERENT") {
		t.Errorf("refusal does not say the workspace belongs to another project: %v", err)
	}
}

// RESPECT THE BUDGET CHECKPOINT. An unacknowledged threshold stops the
// dispatch and says so; it does not proceed and report afterwards.
func TestPlanStopsAtAnUnacknowledgedBudgetCheckpoint(t *testing.T) {
	home := planHome(t)
	spend(t, home, 90)
	cfg := config.Config{Budget: config.Budget{WeeklyUSD: 100}}

	out, err := runPlanInto(t, orpay(), cfg, planOptions{Key: "ORPAY", Home: home})
	if err == nil {
		t.Fatal("the run continued past an unacknowledged checkpoint")
	}
	if !strings.Contains(err.Error(), "nothing was dispatched") {
		t.Errorf("the refusal does not say nothing was dispatched: %v", err)
	}
	if !strings.Contains(out, "BUDGET CHECKPOINT") || !strings.Contains(out, "orion budget ack") {
		t.Errorf("the checkpoint was not reported with a way past it:\n%s", out)
	}
	// The gate is on SPENDING, so the free part still happened: the workspace
	// is there to carry into `orion budget ack` and a re-run.
	if _, statErr := os.Stat(filepath.Join(home, "projects", "orion-payments")); statErr != nil {
		t.Errorf("the checkpoint also withheld the workspace, which costs nothing: %v", statErr)
	}
}

// An acknowledged checkpoint is not a checkpoint. Ack must actually clear it,
// or the command is unusable for the rest of the week.
func TestPlanContinuesOnceTheCheckpointIsAcknowledged(t *testing.T) {
	home := planHome(t)
	spend(t, home, 90)
	l, err := budget.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	l.AckAll(100)
	if err := l.Save(home); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Budget: config.Budget{WeeklyUSD: 100}}

	out, err := runPlanInto(t, orpay(), cfg, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatalf("an acknowledged checkpoint still stopped the run: %v", err)
	}
	if !strings.Contains(out, "next: orion run") {
		t.Errorf("the run did not hand off after the ack:\n%s", out)
	}
}

// A dry run reports the checkpoint; it does not fail on it. Its exit code
// answers "did the inspection work", and a report that exits non-zero for
// correctly finding something reads as a broken command.
func TestPlanDryRunReportsACheckpointWithoutFailing(t *testing.T) {
	home := planHome(t)
	spend(t, home, 90)
	cfg := config.Config{Budget: config.Budget{WeeklyUSD: 100}}

	out, err := runPlanInto(t, orpay(), cfg, planOptions{Key: "ORPAY", Home: home, DryRun: true})
	if err != nil {
		t.Fatalf("--dry-run failed on a checkpoint it only had to report: %v", err)
	}
	if !strings.Contains(out, "BUDGET CHECKPOINT") {
		t.Errorf("--dry-run did not report the checkpoint:\n%s", out)
	}
	if !strings.Contains(out, "would stop here") {
		t.Errorf("--dry-run did not say a real run would stop:\n%s", out)
	}
}

// With no history the estimate is absent rather than invented. A confident
// figure derived from nothing is the same number every time, so it reads as a
// measurement and never corrects itself.
func TestPlanSaysThereIsNoEstimateRatherThanInventingOne(t *testing.T) {
	home := planHome(t)
	out, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing to estimate from") {
		t.Errorf("an estimate appeared with no run history behind it:\n%s", out)
	}
	if strings.Contains(out, "$0.00 per run") {
		t.Errorf("zero is being presented as a measured per-run cost:\n%s", out)
	}
}

// With history it is the measured mean, multiplied by the fleet size -- the
// number §C asks for before the first dispatch.
func TestPlanEstimatesTheChainFromRecordedRuns(t *testing.T) {
	home := planHome(t)
	spend(t, home, 4) // 2 runs of $2.00 each
	out, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "$2.00 per run") {
		t.Errorf("the per-run estimate is not the measured mean:\n%s", out)
	}
	want := fmt.Sprintf("$%.2f for the chain", 2.0*float64(len(planStages)))
	if !strings.Contains(out, want) {
		t.Errorf("the chain total %q is missing; the fleet size is what multiplies the cost:\n%s",
			want, out)
	}
}

func TestPlanNeedsAKey(t *testing.T) {
	home := planHome(t)
	f := orpay()
	if _, err := runPlanInto(t, f, config.Config{}, planOptions{Key: "", Home: home}); err == nil {
		t.Fatal("an empty key was accepted")
	}
	if len(f.asked) != 0 {
		t.Errorf("the tracker was called with %v before the key was validated", f.asked)
	}
}

// A project with no NAME has nothing to derive a slug from -- rejected
// before a workspace is touched, with an instruction the operator can act on.
func TestPlanRejectsAProjectWithNoName(t *testing.T) {
	home := planHome(t)
	f := &fakeProjects{projects: map[string]tracker.Project{
		"NONAME": {ID: "1", Key: "NONAME", Name: "", Description: "something"},
	}}
	_, err := runPlanInto(t, f, config.Config{}, planOptions{Key: "NONAME", Home: home})
	if err == nil {
		t.Fatal("a project with no name was accepted")
	}
	if !strings.Contains(err.Error(), "no name") {
		t.Errorf("refusal does not say the project has no name: %v", err)
	}
	if entries, readErr := os.ReadDir(filepath.Join(home, "projects")); readErr == nil && len(entries) > 0 {
		t.Error("a workspace was provisioned for a project with no name")
	}
}

// The roster names actors by whatever the operator configured them as
// (docs/decisions/0005), not the shipped default -- a renamed agent has to
// show up renamed in the one place a run is judged before it costs anything.
func TestPlanRosterUsesConfiguredActorNames(t *testing.T) {
	home := planHome(t)
	// The shipped default, read back rather than typed literally: actors.go is
	// the one file allowed to name a default, and TestNoDefaultNameAppearsOutsideTheRegistry
	// enforces that no other file may -- including this one.
	shipped := actors.Display(events.ActorArchitect)
	custom := "Beeblebrox"
	if err := actors.Configure(map[string]config.Agent{
		events.ActorArchitect: {Name: &custom},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = actors.Configure(nil) })

	out, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, custom) {
		t.Errorf("roster does not show the configured actor name %q:\n%s", custom, out)
	}
	if strings.Contains(out, shipped) {
		t.Errorf("roster shows the shipped default name instead of the configured one:\n%s", out)
	}
}

// The database architect's planning step is not one of planStages, so the
// only place a reader finds it is this line -- and it has to appear when the
// idea itself named the database (OR-150, OR-154).
func TestPlanAnnouncesTheDatabaseArchitectStepWhenTheIdeaSelectsIt(t *testing.T) {
	home := planHome(t)
	pr := &fakeProjects{projects: map[string]tracker.Project{
		"ORPAY": {
			ID: "10042", Key: "ORPAY", Name: "Orion Payments",
			Description: "A payments ledger with a database behind it.",
		},
	}}

	out, err := runPlanInto(t, pr, config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--stage "+planDBAStage) {
		t.Errorf("the output does not tell the operator to run the database stage:\n%s", out)
	}
}

// The mirror case: an idea that never says "database" must not print the
// database architect's step at all. It is not in the fixed chain, so a line
// that appears regardless of the idea would be indistinguishable from one the
// idea actually asked for -- the same failure mode OR-191 names for a roster
// that varies without saying why.
func TestPlanDoesNotAnnounceTheDatabaseArchitectStepWhenTheIdeaDoesNotSelectIt(t *testing.T) {
	home := planHome(t)

	out, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "ORPAY", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "--stage "+planDBAStage) {
		t.Errorf("the output announces the database stage though the idea never selected it:\n%s", out)
	}
}

func TestPlanSurfacesAnUnknownProject(t *testing.T) {
	home := planHome(t)
	_, err := runPlanInto(t, orpay(), config.Config{}, planOptions{Key: "NOPE", Home: home})
	if err == nil {
		t.Fatal("an unknown project key was accepted")
	}
	if entries, readErr := os.ReadDir(filepath.Join(home, "projects")); readErr == nil && len(entries) > 0 {
		t.Error("a workspace was provisioned for a project that does not exist")
	}
}

// spend writes n dollars of run history into the ledger, as two equal runs.
func spend(t *testing.T, home string, usd float64) {
	t.Helper()
	if err := budget.Update(home, func(l *budget.Ledger) {
		for i := 0; i < 2; i++ {
			l.Record(budget.Run{At: time.Now(), CostUSD: usd / 2, InputTokens: 100, OutputTokens: 100})
		}
	}); err != nil {
		t.Fatal(err)
	}
}
