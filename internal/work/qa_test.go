package work

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/njagents"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The same project as work_test.go's cfg, saying nothing about QA -- which is
// how a repository that has never heard of the stage is configured, and the
// case where it must run.
const qaCfg = `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
                "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"}}`

// qaFake is a Supervise stand-in that records every run by stage and replies
// to the QA stage from a script.
//
// It commits a uniquely-named file per run so that the second and third runs
// are not "nothing to commit" -- a real agent produces something each time,
// and a fake that does not makes git fail for a reason the test is not about.
type qaFake struct {
	t *testing.T
	// qaReplies are the QA agent's closing messages, in order. Running past
	// the end repeats the last one, which is what a QA agent that keeps
	// finding the same thing looks like.
	qaReplies []string
	stages    []string // "ticket" or "qa", in call order
	prompts   []string
	qaCalls   int
}

func (f *qaFake) run(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
	f.stages = append(f.stages, o.Stage)
	f.prompts = append(f.prompts, o.Prompt)

	name := filepath.Join(ws.RepoDir(), o.Stage+"-"+strconv.Itoa(len(f.stages))+".go")
	if err := os.WriteFile(name, []byte("package x\n"), 0o644); err != nil {
		return nil, err
	}
	git(f.t, ws.RepoDir(), "add", ".")
	git(f.t, ws.RepoDir(), "commit", "-q", "-m", o.Stage+" work")

	res := &supervisor.Result{ExitCode: 0, SessionID: o.Stage + "-session"}
	if o.Stage == "qa" {
		reply := ""
		if len(f.qaReplies) > 0 {
			i := f.qaCalls
			if i >= len(f.qaReplies) {
				i = len(f.qaReplies) - 1
			}
			reply = f.qaReplies[i]
		}
		f.qaCalls++
		res.Final = reply
	}
	return res, nil
}

func (f *qaFake) sequence() string { return strings.Join(f.stages, ",") }

// fakeToolkit writes an nj-agents checkout complete enough for Discover to
// accept it, with or without the testing class. Pointed at through the
// environment so the test never depends on what is installed on the machine
// running it -- a check whose answer is "whatever this laptop has" proves
// nothing on any other one.
func fakeToolkit(t *testing.T, withTesting bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CONVENTIONS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := append([]string{}, njagents.RequiredSkills...)
	if withTesting {
		skills = append(skills, njagents.TestingSkills...)
	}
	for _, s := range skills {
		dir := filepath.Join(root, "skills", s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Off means off. A documentation repository does not need an agent writing
// tests for it, and the stage costs real money on every ticket.
func TestQAIsSkippedWhenTheProjectSwitchesItOff(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
	                     "qa":{"enabled":false}}`)
	f := &qaFake{t: t}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if strings.Contains(f.sequence(), "qa") {
		t.Errorf("QA ran for a project that switched it off: %s", f.sequence())
	}
}

// The exchange: QA finds something, the developer fixes it, QA re-verifies
// and it is clean. The stage must not escalate, and the pull request opens.
func TestQAFindingsGoToTheDeveloperAndAreReVerified(t *testing.T) {
	home := project(t, qaCfg)
	j := &fakeJira{}
	f := &qaFake{t: t, qaReplies: []string{
		"The rounding case is wrong: expected 2 decimal places, got 4.",
		"QA CLEAN",
	}}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q; a cleared finding must not stop the run: %+v", res[0].Outcome, res[0])
	}
	// A cleared finding is not something to bother anyone with on the ticket.
	if c := strings.Join(j.comments, "\n"); strings.Contains(c, "still open") {
		t.Errorf("a finding that was fixed was reported as open:\n%s", c)
	}
	// implement, verify, fix, re-verify. Any other order means the findings
	// were not actually carried back to the developer between the two QA runs.
	if got := f.sequence(); got != "ticket,qa,ticket,qa" {
		t.Fatalf("run sequence = %q, want ticket,qa,ticket,qa", got)
	}
	// The findings themselves must reach the developer. A round that sends
	// "QA found something" spends a run and tells it nothing.
	if !strings.Contains(f.prompts[2], "2 decimal places") {
		t.Errorf("the findings did not reach the developer:\n%s", f.prompts[2])
	}
	// And QA must be asked to look again rather than told it passed.
	if !strings.Contains(strings.ToLower(f.prompts[3]), "re-run") {
		t.Errorf("QA was not asked to re-verify:\n%s", f.prompts[3])
	}
	if strings.Contains(out.String(), "A person needs to look") {
		t.Error("a cleared finding escalated to a person")
	}
}

// The ceiling. Two agents can disagree about a test for as long as somebody
// keeps paying them, so a fixed number of rounds ends it and a person is
// told what is still open -- and the change still goes to review, because QA
// does not block on its own authority.
func TestQAEscalatesToAPersonWhenTheRoundsRunOut(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
	                     "qa":{"max_rounds":1}}`)
	j := &fakeJira{}
	f := &qaFake{t: t, qaReplies: []string{"The authorisation case still fails: an editor can delete."}}
	var out strings.Builder
	opened := false

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push: func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) {
				opened = true
				return "https://pr/1", nil
			},
		})

	// One fix round, then stop: implement, verify, fix, re-verify.
	if got := f.sequence(); got != "ticket,qa,ticket,qa" {
		t.Fatalf("run sequence = %q; the round ceiling did not hold", got)
	}
	if !opened || res[0].Outcome != OutcomeCIWait {
		t.Errorf("QA blocked the change on its own authority: opened=%v outcome=%q", opened, res[0].Outcome)
	}
	comments := strings.Join(j.comments, "\n")
	if !strings.Contains(comments, "an editor can delete") {
		t.Errorf("the open findings were not left for a person:\n%s", comments)
	}
	if !strings.Contains(comments, "QA engineer") {
		t.Errorf("the findings do not say who reported them:\n%s", comments)
	}
	if !strings.Contains(out.String(), "A person needs to look") {
		t.Errorf("nothing told the operator the findings are still open:\n%s", out.String())
	}
}

// A QA run that could not finish is not a failed ticket. The change is
// committed and CI-gated by this point, and throwing finished work away over
// the verification of it is the worse error by a distance -- so the run says
// the change is going to review unverified and carries on.
func TestAQARunThatFailsDoesNotFailTheTicket(t *testing.T) {
	home := project(t, qaCfg)
	var out strings.Builder
	stages := []string{}
	pushed := false

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				stages = append(stages, o.Stage)
				if o.Stage == "qa" {
					return &supervisor.Result{ExitCode: 1, Reason: "the breaker tripped"}, nil
				}
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
				return &supervisor.Result{ExitCode: 0, SessionID: "s"}, nil
			},
			Push:   func(string, string) error { pushed = true; return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait || !pushed {
		t.Fatalf("a failed QA run stopped the ticket: outcome=%q pushed=%v", res[0].Outcome, pushed)
	}
	if strings.Join(stages, ",") != "ticket,qa" {
		t.Errorf("run sequence = %v", stages)
	}
	// And it must say so: a change that went to review unverified reads
	// exactly like one QA passed unless something says which happened.
	if !strings.Contains(out.String(), "unverified by QA") {
		t.Errorf("nothing said the change was not verified:\n%s", out.String())
	}
}

// Without the nj-agents testing class the stage still runs, using whatever
// the repository has -- and says which of the two it did. A run that
// silently degraded to half its coverage reads exactly like one that did not.
func TestQADegradesToTheRepositorysOwnToolingAndSaysSo(t *testing.T) {
	t.Setenv("ORION_NJ_AGENTS_DIR", fakeToolkit(t, false))
	home := project(t, qaCfg)
	f := &qaFake{t: t, qaReplies: []string{"QA CLEAN"}}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if got := f.sequence(); got != "ticket,qa" {
		t.Fatalf("run sequence = %q; QA must still run without the skills", got)
	}
	if !strings.Contains(f.prompts[1], "nj-agents is not installed here") {
		t.Errorf("the prompt did not tell QA the skills are absent:\n%s", f.prompts[1])
	}
	if !strings.Contains(out.String(), "this repository's own test tooling") {
		t.Errorf("the run did not say which path it took:\n%s", out.String())
	}
	// No non-prod target is configured, so an e2e run must be off the table
	// rather than pointed at whatever the agent can find.
	if !strings.Contains(f.prompts[1], "No non-production target is configured") {
		t.Errorf("the prompt did not rule out an e2e run:\n%s", f.prompts[1])
	}
}

// The toolkit is detected in the clone it resolves to, not in the runner's
// skills directory: a symlink says a skill was installed once, not that the
// toolkit behind it is still there.
func TestQAUsesTheTestingSkillsWhenTheToolkitHasThem(t *testing.T) {
	root := fakeToolkit(t, true)
	cfg := config.Defaults()
	cfg.Delegation.NJAgentsDir = root
	got := qaTools(cfg, t.TempDir())
	if !got.Skills {
		t.Fatalf("the testing skills were present and were not detected: %+v", got)
	}
	if got.Path() != "nj-agents testing skills" {
		t.Errorf("path = %q", got.Path())
	}

	// One missing testing skill is enough to fall back: half the chain is
	// not the chain, and a prompt naming a skill that is not there sends the
	// agent looking for it.
	if err := os.RemoveAll(filepath.Join(root, "skills", njagents.TestingSkills[0])); err != nil {
		t.Fatal(err)
	}
	if qaTools(cfg, t.TempDir()).Skills {
		t.Error("a missing testing skill was still reported as installed")
	}
}

// The sentinel decides the verdict, and only when it starts a line. An agent
// that quotes its own instructions must not declare a clean branch by
// describing one.
func TestQAVerdictReadsTheSentinelAndNotThePraise(t *testing.T) {
	cases := []struct {
		final     string
		wantClean bool
	}{
		{"everything passed\nQA CLEAN", true},
		{"**QA CLEAN**", true},
		{"You told me to write QA CLEAN when every case passes.\nCase 3 fails.", false},
		{"Case 3 fails: expected 400, got 500.", false},
		{"   ", false},
		{"All tests pass and the change looks good to me.", false},
	}
	for _, c := range cases {
		findings, clean := qaVerdict(c.final)
		if clean != c.wantClean {
			t.Errorf("qaVerdict(%q) clean = %v, want %v", c.final, clean, c.wantClean)
		}
		if !clean && strings.TrimSpace(findings) == "" {
			t.Errorf("qaVerdict(%q) reported no findings and no pass; a reader learns nothing", c.final)
		}
	}
}
