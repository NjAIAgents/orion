package work

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// roster writes the global agent config and restores the shipped one after
// the test, since actors is process-wide state.
func roster(t *testing.T, home string, agents map[string]config.Agent) {
	t.Helper()
	if err := config.SaveAgents(home, agents); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = actors.Configure(nil) })
}

// OR-133. Configuring agents.implementer used to change the banner, the
// event log and the cost report but not which model actually ran, so the
// roster reported one thing and did another. Every implementer run -- the
// first one and the one resumed with an advisor's answer -- must carry the
// configured model and effort into the supervisor.
func TestTheImplementersModelAndEffortReachEveryImplementerRun(t *testing.T) {
	home := project(t, cfg)
	roster(t, home, map[string]config.Agent{
		"implementer": {Model: "sonnet", Effort: "high"},
	})

	run, _ := advisor("TECHNICAL",
		`{"verdict":"derived","decision":"By issuer.","grounding":"spec.md section 4"}`)

	var seen []supervisor.Options
	var out strings.Builder
	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				seen = append(seen, o)
				if len(seen) == 1 {
					return &supervisor.Result{ExitCode: 0, SessionID: "sess-1",
						Final: "Are segments keyed by MCC or by issuer?"}, nil
				}
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"),
					[]byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: segment by issuer")
				return &supervisor.Result{ExitCode: 0, SessionID: "sess-1", Final: "done"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q: %+v", res[0].Outcome, res[0])
	}
	if len(seen) != 2 {
		t.Fatalf("%d runs; expected the initial run and the resumed one", len(seen))
	}
	for i, o := range seen {
		if o.Model != "sonnet" || o.Effort != "high" {
			t.Errorf("run %d ran with model=%q effort=%q, want the configured sonnet/high",
				i+1, o.Model, o.Effort)
		}
	}
}

// Empty must keep meaning "whatever the CLI is set to". The shipped roster
// sets no effort for anybody, so a project that configures nothing must
// still send no --effort at all rather than a level Orion invented.
func TestAnUnsetEffortIsStillPassedAsNothing(t *testing.T) {
	home := project(t, cfg)
	if err := actors.Configure(nil); err != nil {
		t.Fatal(err)
	}

	var seen supervisor.Options
	var out strings.Builder
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				seen = o
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"),
					[]byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: a change")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if seen.Effort != "" {
		t.Errorf("an actor with no configured effort must pass none, got %q", seen.Effort)
	}
	if seen.Model != actors.Model("implementer") {
		t.Errorf("model = %q, want the roster's %q", seen.Model, actors.Model("implementer"))
	}
}

// The QA loop's fix run is the implementer's, so it carries the implementer's
// model and effort too -- fixing what QA found is implementation work.
func TestTheQALoopsFixRunUsesTheImplementersModelAndEffort(t *testing.T) {
	home := project(t, qaCfg)
	roster(t, home, map[string]config.Agent{
		"implementer": {Model: "sonnet", Effort: "high"},
	})
	f := &qaFake{t: t, qaReplies: []string{
		"The rounding case is wrong: expected 2 decimal places, got 4.",
		"QA CLEAN",
	}}

	var fixRuns []supervisor.Options
	var out strings.Builder
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if o.Stage == "ticket" && o.Resume != "" {
					fixRuns = append(fixRuns, o)
				}
				return f.run(ws, o)
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if len(fixRuns) == 0 {
		t.Fatalf("QA never handed a finding back to the developer: %s", f.sequence())
	}
	for i, o := range fixRuns {
		if o.Model != "sonnet" || o.Effort != "high" {
			t.Errorf("fix run %d ran with model=%q effort=%q, want the configured sonnet/high",
				i+1, o.Model, o.Effort)
		}
	}
}

// The describer's run is an agent turn like any other, so the registry's
// effort has to reach it as well as its model.
func TestTheDescribersModelAndEffortReachItsRun(t *testing.T) {
	if err := actors.Configure(map[string]config.Agent{
		"describer": {Model: "haiku", Effort: "low"},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = actors.Configure(nil) })

	var gotModel, gotEffort string
	run := func(dir, model, effort, prompt string) (string, error) {
		gotModel, gotEffort = model, effort
		return `{"title":"T","body":"B"}`, nil
	}
	if _, _, ok := describePR(run, "/repo", "FCIA-7", fbTitle, fbBody); !ok {
		t.Fatal("a valid description was rejected")
	}
	if gotModel != "haiku" || gotEffort != "low" {
		t.Errorf("describer ran with model=%q effort=%q, want haiku/low", gotModel, gotEffort)
	}
}
