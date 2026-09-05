package work

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/toolkit"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// OR-133 is about every actor's run, not only the implementer's: QA's own
// two runs (the initial verification and the re-verify after a fix) must
// carry the QA actor's configured model and effort too, or the roster's
// agents.qa.model setting is the same reports-one-thing-does-another gap the
// ticket names for the implementer.
func TestQAsOwnRunsCarryTheQAModelAndEffort(t *testing.T) {
	home := project(t, qaCfg)
	roster(t, home, map[string]config.Agent{
		"qa": {Model: "opus", Effort: "xhigh"},
	})
	f := &qaFake{t: t, qaReplies: []string{
		"The rounding case is wrong: expected 2 decimal places, got 4.",
		"QA CLEAN",
	}}

	var qaRuns []supervisor.Options
	var out strings.Builder
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if o.Stage == "qa" {
					qaRuns = append(qaRuns, o)
				}
				return f.run(ws, o)
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if len(qaRuns) != 2 {
		t.Fatalf("%d QA runs; expected the initial verification and the re-verify: %s",
			len(qaRuns), f.sequence())
	}
	for i, o := range qaRuns {
		if o.Model != "opus" || o.Effort != "xhigh" {
			t.Errorf("QA run %d ran with model=%q effort=%q, want the configured opus/xhigh",
				i+1, o.Model, o.Effort)
		}
	}
}

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
	// fixReplies are the implementer's closing messages on a FIX round, in
	// order -- what it says it changed in response to QA. Nil leaves Final
	// unset, which is what every test predating OR-200 assumes.
	fixReplies []string
	stages     []string // "ticket" or "qa", in call order
	prompts    []string
	qaCalls    int
	fixCalls   int
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
	// A fix round is the implementer resumed, which is the only "ticket" run
	// that carries a session to resume.
	if o.Stage == "ticket" && o.Resume != "" && len(f.fixReplies) > 0 {
		i := f.fixCalls
		if i >= len(f.fixReplies) {
			i = len(f.fixReplies) - 1
		}
		f.fixCalls++
		res.Final = f.fixReplies[i]
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
	var skills []string
	for _, r := range toolkit.RequiredSkills(toolkit.Toolkit{}) {
		skills = append(skills, r.Skill)
	}
	if withTesting {
		skills = append(skills, toolkit.TestingSkills...)
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
	// implement, derive the cases, verify, fix, re-verify. Any other order
	// means the findings were not actually carried back to the developer
	// between the two QA runs. The derive step runs once, before the first
	// verification only: a re-verify runs the cases that already exist
	// (OR-182).
	if got := f.sequence(); got != "ticket,qa-cases,qa,ticket,qa" {
		t.Fatalf("run sequence = %q, want ticket,qa-cases,qa,ticket,qa", got)
	}
	// The findings themselves must reach the developer. A round that sends
	// "QA found something" spends a run and tells it nothing.
	if !strings.Contains(f.prompts[3], "2 decimal places") {
		t.Errorf("the findings did not reach the developer:\n%s", f.prompts[3])
	}
	// And QA must be asked to look again rather than told it passed.
	if !strings.Contains(strings.ToLower(f.prompts[4]), "re-run") {
		t.Errorf("QA was not asked to re-verify:\n%s", f.prompts[4])
	}
	if strings.Contains(out.String(), "A person needs to look") {
		t.Error("a cleared finding escalated to a person")
	}
}

// OR-171. A ticket routed to a non-default actor must have its QA fix round
// resume THAT actor, not the implementer -- otherwise a UI ticket's fix lands
// on the backend developer's session and the run reports two different
// authors for one branch.
func TestQAFixLoopResumesTheRoutedActorNotTheImplementer(t *testing.T) {
	home := project(t, qaCfg)
	j := &fakeJira{issue: &tracker.Issue{
		Key: "FCIA-6", Summary: "restyle the button", Labels: []string{"ui"},
		URL: "https://x/browse/FCIA-6",
	}}
	f := &qaFake{t: t, qaReplies: []string{
		"The button color is wrong.",
		"QA CLEAN",
	}}

	var ticketActors []string
	var out strings.Builder
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if o.Stage == "ticket" {
					ticketActors = append(ticketActors, o.Actor)
				}
				return f.run(ws, o)
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if len(ticketActors) != 2 {
		t.Fatalf("%d ticket-stage runs; expected the implementing run and the fix round: %v",
			len(ticketActors), ticketActors)
	}
	for i, a := range ticketActors {
		if a != events.ActorFrontend {
			t.Errorf("ticket run %d ran as %q, want %q (the routed actor)", i, a, events.ActorFrontend)
		}
	}
}

// OR-167: on the common path -- QA finds something, the implementer fixes it
// in round one, QA goes clean -- the full findings text still has to reach
// somewhere durable. Before this, the only copy of the full text lived in
// memory and reached storage solely via qaEscalate, which never runs when the
// branch clears within the round ceiling.
func TestQAFullFindingsReachTheEventLogEvenWhenQAGoesClean(t *testing.T) {
	home := project(t, qaCfg)
	f := &qaFake{t: t, qaReplies: []string{
		"Verification done. Summary:\nThe rounding case is wrong: expected 2 decimal places, got 4.",
		"QA CLEAN",
	}}
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

	ws, err := workspace.Open(mustWorkspaceID(t, home))
	if err != nil {
		t.Fatal(err)
	}
	evs, err := events.Read(events.Path(ws.Dir))
	if err != nil {
		t.Fatal(err)
	}
	var qaMsgs []string
	for _, e := range evs {
		if e.Kind == events.KindQA {
			qaMsgs = append(qaMsgs, e.Msg)
		}
	}
	joined := strings.Join(qaMsgs, "\n---\n")
	if !strings.Contains(joined, "Verification done. Summary:") ||
		!strings.Contains(joined, "expected 2 decimal places, got 4") {
		t.Errorf("the event log did not carry the full findings text:\n%s", joined)
	}
}

// OR-167: the console line is the first SUBSTANTIVE line, not the literal
// first line -- a header like "Verification done. Summary:" must not be the
// one line the operator sees, and when the console does drop content it must
// say where the rest is.
func TestQAConsoleSkipsTheHeaderAndPointsAtTheEventLog(t *testing.T) {
	home := project(t, qaCfg)
	f := &qaFake{t: t, qaReplies: []string{
		"Verification done. Summary:\nThe rounding case is wrong: expected 2 decimal places, got 4.",
		"QA CLEAN",
	}}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	printed := out.String()
	if strings.Contains(printed, "findings: Verification done. Summary:") {
		t.Errorf("the console line was the header, not the content:\n%s", printed)
	}
	if !strings.Contains(printed, "The rounding case is wrong") {
		t.Errorf("the console did not show the substantive line:\n%s", printed)
	}
	if !strings.Contains(printed, "full text in the event log") {
		t.Errorf("the console did not say where the full text is:\n%s", printed)
	}
}

// OR-167: the ticket gets the findings every round, not only when the round
// ceiling escalates to a person -- otherwise the common case (fixed in round
// one) leaves nothing on the ticket for someone reading it weeks later.
func TestQAPostsFindingsToTheTicketEveryRound(t *testing.T) {
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
		t.Fatalf("outcome = %q", res[0].Outcome)
	}

	comments := strings.Join(j.comments, "\n")
	if !strings.Contains(comments, "expected 2 decimal places, got 4") {
		t.Errorf("round one's findings were never posted to the ticket, though the branch never hit "+
			"the round ceiling:\n%s", comments)
	}
}

// OR-167: "every round", not just "the first non-clean round" -- two
// consecutive rounds each with distinct findings must both land on the
// ticket. A version that only posted once (e.g. on entry to the loop, or
// only at the end) would pass TestQAPostsFindingsToTheTicketEveryRound
// since that test only has one non-clean round.
func TestQAPostsEachRoundsDistinctFindingsToTheTicket(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
	                     "qa":{"max_rounds":2}}`)
	j := &fakeJira{}
	f := &qaFake{t: t, qaReplies: []string{
		"Round one problem: the discount is applied twice.",
		"Round two problem: the refund path still double-charges.",
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
		t.Fatalf("outcome = %q", res[0].Outcome)
	}

	comments := strings.Join(j.comments, "\n")
	if !strings.Contains(comments, "discount is applied twice") {
		t.Errorf("round one's finding never reached the ticket:\n%s", comments)
	}
	if !strings.Contains(comments, "refund path still double-charges") {
		t.Errorf("round two's finding never reached the ticket:\n%s", comments)
	}
}

// Neither comment path may reach for a nil deps.Jira -- runQA is reachable
// directly (unlike the outer Run, which already dereferences deps.Jira
// unconditionally before QA is ever claimed), and their own guards are the
// only thing standing between a nil tracker and a panic here.
func TestQACommentsDoNotPanicWithoutATracker(t *testing.T) {
	qaPostRound(Deps{Jira: nil}, qaRoundReport{Key: "FCIA-6", Round: 1,
		Findings: "the rounding case is wrong", FixActor: events.ActorImplementer})
	qaPostNothingFound(Deps{Jira: nil}, "FCIA-6")
}

// OR-200: one round leaves ONE comment, and that comment carries all three
// facts a reviewer wants -- what QA found, what changed in response, and
// whether re-verification passed. A version that posted the finding alone
// (the OR-167 behaviour) leaves the ticket saying a problem was raised and
// never saying it was resolved, which reads worse than saying nothing.
func TestQACommentsTheFindingAndItsResolutionInOneComment(t *testing.T) {
	home := project(t, qaCfg)
	j := &fakeJira{}
	f := &qaFake{t: t,
		qaReplies:  []string{"The rounding case is wrong: expected 2 decimal places, got 4.", "QA CLEAN"},
		fixReplies: []string{"Rounded the total to 2 decimal places before formatting it."},
	}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})
	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}

	round := qaRoundComments(j.comments)
	if len(round) != 1 {
		t.Fatalf("one QA round produced %d round comment(s), want exactly 1:\n%s",
			len(round), strings.Join(j.comments, "\n---\n"))
	}
	for _, want := range []string{
		"expected 2 decimal places, got 4",                        // the finding
		"Rounded the total to 2 decimal places before formatting", // the resolution
		"Re-verified: every case passes.",                         // the re-verification
	} {
		if !strings.Contains(round[0], want) {
			t.Errorf("the round comment is missing %q:\n%s", want, round[0])
		}
	}
	// The exchange itself is noise. Only the fix's closing line belongs here.
	if strings.Contains(round[0], "QA round 2") {
		t.Errorf("a second round was reported though only one ran:\n%s", round[0])
	}
	// A clean end after a fix round is already said by that round's comment.
	if strings.Contains(strings.Join(j.comments, "\n"), "no fix rounds were needed") {
		t.Errorf("the nothing-found line was posted for a run that did have findings:\n%s",
			strings.Join(j.comments, "\n---\n"))
	}
}

// OR-200: two rounds produce two comments -- one per exchange, never one
// merged summary and never a per-turn stream. The round cap (qa.go's max) is
// what bounds this, so the count is the cap and cannot become a flood.
func TestQACommentsOncePerRound(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
	                     "qa":{"max_rounds":2}}`)
	j := &fakeJira{}
	f := &qaFake{t: t,
		qaReplies: []string{
			"Round one problem: the discount is applied twice.",
			"Round two problem: the refund path still double-charges.",
			"QA CLEAN",
		},
		fixReplies: []string{
			"Applied the discount once, in the pricing step only.",
			"Made the refund path idempotent per transaction id.",
		},
	}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	round := qaRoundComments(j.comments)
	if len(round) != 2 {
		t.Fatalf("two QA rounds produced %d round comment(s), want exactly 2:\n%s",
			len(round), strings.Join(j.comments, "\n---\n"))
	}
	if !strings.Contains(round[0], "discount is applied twice") ||
		!strings.Contains(round[0], "Applied the discount once") {
		t.Errorf("round one's comment does not pair its finding with its fix:\n%s", round[0])
	}
	if !strings.Contains(round[1], "refund path still double-charges") ||
		!strings.Contains(round[1], "idempotent per transaction id") {
		t.Errorf("round two's comment does not pair its finding with its fix:\n%s", round[1])
	}
}

// OR-200: QA finding nothing has to be SAID. Silence on the ticket is
// currently ambiguous between "QA verified this and found nothing" and "QA
// never ran", and after OR-156's proved-red requirement those are very
// different claims about a change.
func TestQACommentsWhenItFoundNothing(t *testing.T) {
	home := project(t, qaCfg)
	j := &fakeJira{}
	f := &qaFake{t: t, qaReplies: []string{"QA CLEAN"}}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})
	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}

	comments := strings.Join(j.comments, "\n---\n")
	if !strings.Contains(comments, "found nothing") {
		t.Fatalf("a clean QA run left nothing on the ticket, so it cannot be told apart "+
			"from a run where QA never happened:\n%s", comments)
	}
	if got := qaRoundComments(j.comments); len(got) != 0 {
		t.Errorf("a clean run reported %d fix round(s):\n%s", len(got), strings.Join(got, "\n---\n"))
	}
}

// OR-200: a fix round that never finishes is still a round the ticket must
// carry -- the finding that opened it is the last thing anyone knows was
// wrong, and a version that only posted on a successful re-verify would
// leave exactly this, the actually-unresolved case, silent.
func TestQAPostsARoundCommentWhenTheFixRunFails(t *testing.T) {
	home := project(t, qaCfg)
	j := &fakeJira{}
	var out strings.Builder
	ticketCalls := 0

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				switch o.Stage {
				case "qa-cases":
					return &supervisor.Result{ExitCode: 0, Final: "- the rounding boundary"}, nil
				case "qa":
					if err := os.WriteFile(filepath.Join(ws.RepoDir(), "qa.go"), []byte("package x\n"), 0o644); err != nil {
						return nil, err
					}
					git(t, ws.RepoDir(), "add", ".")
					git(t, ws.RepoDir(), "commit", "-q", "-m", "qa work")
					return &supervisor.Result{ExitCode: 0, SessionID: "qa-session",
						Final: "The rounding case is wrong: expected 2 decimal places, got 4."}, nil
				case "ticket":
					ticketCalls++
					if ticketCalls == 1 {
						if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
							return nil, err
						}
						git(t, ws.RepoDir(), "add", ".")
						git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
						return &supervisor.Result{ExitCode: 0, SessionID: "impl-session"}, nil
					}
					// The fix round: crashes before it can change anything.
					return &supervisor.Result{ExitCode: 1, Reason: "the fix run crashed"}, nil
				}
				return nil, fmt.Errorf("unexpected stage %q", o.Stage)
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q; a fix run that crashed must not fail the ticket", res[0].Outcome)
	}
	round := qaRoundComments(j.comments)
	if len(round) != 1 {
		t.Fatalf("the fix run's failure produced %d round comment(s), want exactly 1:\n%s",
			len(round), strings.Join(j.comments, "\n---\n"))
	}
	if !strings.Contains(round[0], "expected 2 decimal places, got 4") {
		t.Errorf("the round comment lost the finding that was open when the fix run failed:\n%s", round[0])
	}
	if !strings.Contains(round[0], "The fix run did not finish, so this was never re-verified.") {
		t.Errorf("the round comment does not say the fix run never finished:\n%s", round[0])
	}
	if !strings.Contains(out.String(), "unverified by QA") {
		t.Errorf("nothing said the change went to review unverified:\n%s", out.String())
	}
}

// OR-200: the mirror case -- the fix itself finished and said what it
// changed, but the re-verification run that was supposed to confirm it never
// came back. The ticket must still show the finding paired with the fix,
// with a verdict that says the outcome is unknown rather than either
// resolved or still open.
func TestQAPostsARoundCommentWhenReVerificationFails(t *testing.T) {
	home := project(t, qaCfg)
	j := &fakeJira{}
	var out strings.Builder
	ticketCalls, qaCalls := 0, 0

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				switch o.Stage {
				case "qa-cases":
					return &supervisor.Result{ExitCode: 0, Final: "- the rounding boundary"}, nil
				case "qa":
					qaCalls++
					if qaCalls == 1 {
						if err := os.WriteFile(filepath.Join(ws.RepoDir(), "qa.go"), []byte("package x\n"), 0o644); err != nil {
							return nil, err
						}
						git(t, ws.RepoDir(), "add", ".")
						git(t, ws.RepoDir(), "commit", "-q", "-m", "qa work")
						return &supervisor.Result{ExitCode: 0, SessionID: "qa-session",
							Final: "The rounding case is wrong: expected 2 decimal places, got 4."}, nil
					}
					// The re-verify: crashes before it can report a verdict.
					return &supervisor.Result{ExitCode: 1, Reason: "the re-verify run crashed"}, nil
				case "ticket":
					ticketCalls++
					if ticketCalls == 1 {
						if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
							return nil, err
						}
						git(t, ws.RepoDir(), "add", ".")
						git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
						return &supervisor.Result{ExitCode: 0, SessionID: "impl-session"}, nil
					}
					return &supervisor.Result{ExitCode: 0, SessionID: "fix-session",
						Final: "Rounded the total to 2 decimal places before formatting it."}, nil
				}
				return nil, fmt.Errorf("unexpected stage %q", o.Stage)
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q; a re-verify run that crashed must not fail the ticket", res[0].Outcome)
	}
	round := qaRoundComments(j.comments)
	if len(round) != 1 {
		t.Fatalf("the re-verify failure produced %d round comment(s), want exactly 1:\n%s",
			len(round), strings.Join(j.comments, "\n---\n"))
	}
	for _, want := range []string{
		"expected 2 decimal places, got 4",
		"Rounded the total to 2 decimal places before formatting",
		"The re-verification run did not finish, so whether the fix cleared this is unknown.",
	} {
		if !strings.Contains(round[0], want) {
			t.Errorf("the round comment is missing %q:\n%s", want, round[0])
		}
	}
}

// OR-200: no comment path may upload a file. A raw run log is hundreds of
// kilobytes against a 2 GB free-tier quota, cannot be grepped once uploaded,
// and carries everything the agent read and wrote -- on OR-105 that included
// a live-format access key that push protection caught and an attachment
// would have published past it. The structural guarantee is that the tracker
// interface this package holds has no way to send a file at all, so this
// asserts the interface rather than any one call site.
func TestTheTrackerInterfaceCannotUploadAFile(t *testing.T) {
	iface := reflect.TypeOf((*TrackerAPI)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		name := strings.ToLower(iface.Method(i).Name)
		for _, banned := range []string{"attach", "upload", "file", "artifact"} {
			if strings.Contains(name, banned) {
				t.Errorf("TrackerAPI.%s can put a file on a ticket; run logs must stay local "+
					"(OR-200)", iface.Method(i).Name)
			}
		}
	}
}

// qaRoundComments picks out the per-round comments, which are the ones this
// ticket's count claims are about -- the escalation comment and the
// nothing-found line are separate statements and are asserted separately.
func qaRoundComments(comments []string) []string {
	var out []string
	for _, c := range comments {
		if strings.Contains(c, "QA round ") && strings.Contains(c, " found:") {
			out = append(out, c)
		}
	}
	return out
}

// OR-167: the console note "(full text in the event log)" only makes sense
// when the console line actually dropped something. A single-line finding
// with no header shows the whole thing on the console already, so tacking
// on the pointer would claim there is more to read when there is not.
func TestQAConsoleOmitsThePointerWhenNothingWasTruncated(t *testing.T) {
	home := project(t, qaCfg)
	f := &qaFake{t: t, qaReplies: []string{
		"Case 3 fails: expected 400, got 500.",
		"QA CLEAN",
	}}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	printed := out.String()
	if !strings.Contains(printed, "Case 3 fails: expected 400, got 500.") {
		t.Errorf("the console did not show the finding:\n%s", printed)
	}
	if strings.Contains(printed, "full text in the event log") {
		t.Errorf("the console pointed at the event log though nothing was truncated:\n%s", printed)
	}
}

// firstSubstantiveLine's fallback: if every line looks like a header (ends
// in ":"), there is no substantive line to prefer, and returning nothing
// would be worse than returning the original text.
func TestFirstSubstantiveLineFallsBackWhenEveryLineIsAHeader(t *testing.T) {
	got := firstSubstantiveLine("Verification done:\nSummary:")
	if got != "Verification done:\nSummary:" {
		t.Errorf("firstSubstantiveLine of all-header text = %q, want the original text back", got)
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

	// One fix round, then stop: implement, derive the cases, verify, fix,
	// re-verify.
	if got := f.sequence(); got != "ticket,qa-cases,qa,ticket,qa" {
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
	// OR-200: the unresolved case is the one most likely to be lost, so the
	// ticket must say in as many words that the rounds ran out with findings
	// still open -- not merely carry the last round's exchange.
	if !strings.Contains(comments, "still open after 1 fix round(s)") {
		t.Errorf("the ticket never says the rounds ran out with findings still open:\n%s", comments)
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
				if o.Stage == "qa-cases" {
					return &supervisor.Result{ExitCode: 0, Final: "- the rounding boundary"}, nil
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
	if strings.Join(stages, ",") != "ticket,qa-cases,qa" {
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

	if got := f.sequence(); got != "ticket,qa-cases,qa" {
		t.Fatalf("run sequence = %q; QA must still run without the skills", got)
	}
	if !strings.Contains(f.prompts[2], "nj-agents is not installed here") {
		t.Errorf("the prompt did not tell QA the skills are absent:\n%s", f.prompts[2])
	}
	if !strings.Contains(out.String(), "this repository's own test tooling") {
		t.Errorf("the run did not say which path it took:\n%s", out.String())
	}
	// No non-prod target is configured, so an e2e run must be off the table
	// rather than pointed at whatever the agent can find.
	if !strings.Contains(f.prompts[2], "No non-production target is configured") {
		t.Errorf("the prompt did not rule out an e2e run:\n%s", f.prompts[2])
	}
}

// The toolkit is detected in the clone it resolves to, not in the runner's
// skills directory: a symlink says a skill was installed once, not that the
// toolkit behind it is still there.
func TestQAUsesTheTestingSkillsWhenTheToolkitHasThem(t *testing.T) {
	root := fakeToolkit(t, true)
	cfg := config.Defaults()
	cfg.Toolkit.Dir = root
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
	if err := os.RemoveAll(filepath.Join(root, "skills", toolkit.TestingSkills[0])); err != nil {
		t.Fatal(err)
	}
	if qaTools(cfg, t.TempDir()).Skills {
		t.Error("a missing testing skill was still reported as installed")
	}
}

// OR-156: once QA's stage ends, whatever tests it wrote must be proven
// against the commit the branch started from. This exercises the wiring --
// runQA calling down into reportRedBeforeGreen and checkRedBeforeGreen --
// with a test that already passes before the change, which is exactly the
// case a person needs to see and not have silently dropped.
func TestRunQAReportsATestThatAlreadyPassedBeforeTheChange(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	writeExec(t, repo, "scripts/test.sh", "#!/bin/sh\nexit 0\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "seed")
	baseSHA := git(t, repo, "rev-parse", "HEAD")

	ws := &workspace.Workspace{RepoPath: repo}
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		if err := os.WriteFile(filepath.Join(w.RepoDir(), "weak_test.go"), []byte("package fake\n"), 0o644); err != nil {
			return nil, err
		}
		git(t, w.RepoDir(), "add", ".")
		git(t, w.RepoDir(), "commit", "-q", "-m", "test: qa")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	outcome := runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	if !outcome.Clean {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !strings.Contains(out.String(), "weak_test.go") || !strings.Contains(out.String(), "prove nothing") {
		t.Errorf("did not report the test that already passed before the change:\n%s", out.String())
	}
}

// OR-156 asks for the red-before-green result "in the console AND the event
// log" -- the wiring test above only ever checks the console. This checks
// the other half: the proven and unproven files must each land in the
// on-disk event log, under the QA actor, with the message a reader would
// need (which file, and why an unproven one counts for nothing).
func TestReportRedBeforeGreenWritesBothToTheEventLog(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	writeExec(t, repo, "scripts/test.sh", "#!/bin/sh\n"+
		"if [ -f sound_test.go ] && [ ! -f feature.flag ]; then\n"+
		"  exit 1\n"+
		"fi\n"+
		"exit 0\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "seed")
	baseSHA := git(t, repo, "rev-parse", "HEAD")

	writeExec(t, repo, "feature.flag", "ENABLED\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "feat: add the feature")
	preQA := git(t, repo, "rev-parse", "HEAD")

	writeExec(t, repo, "sound_test.go", "package fake\n")
	writeExec(t, repo, "weak_test.go", "package fake\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "test: qa")

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var out strings.Builder
	ws := &workspace.Workspace{RepoPath: repo}
	reportRedBeforeGreen(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA}, preQA, log, &out)
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}

	var sawProven, sawUnproven bool
	for _, e := range logged {
		if e.Actor != events.ActorQA {
			continue
		}
		if strings.Contains(e.Msg, "sound_test.go") && e.Kind == events.KindQA {
			sawProven = true
		}
		if strings.Contains(e.Msg, "weak_test.go") && strings.Contains(e.Msg, "prove nothing") {
			sawUnproven = true
			if e.Kind != events.KindQA {
				t.Errorf("the unproven entry has kind %q, want %q", e.Kind, events.KindQA)
			}
		}
	}
	if !sawProven {
		t.Errorf("the event log never named the proven test file:\n%+v", logged)
	}
	if !sawUnproven {
		t.Errorf("the event log never named the unproven test file:\n%+v", logged)
	}
}

// deriveRepo is a branch with one committed change on it: a seed commit to
// use as the base, and an implementation commit for the derive step to read
// a diff out of.
func deriveRepo(t *testing.T) (repo, baseSHA string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	writeExec(t, repo, "scripts/test.sh", "#!/bin/sh\nexit 0\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "seed")
	baseSHA = git(t, repo, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repo, "total.go"),
		[]byte("package x\n\nfunc round(v float64) float64 { return v }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "feat: round the total")
	return repo, baseSHA
}

// OR-182. The deriving is wide reading with a short answer, so it happens in
// a context that is thrown away: its own actor on its own pinned model, with
// the criteria and the diff in ITS prompt and only the case list in QA's. A
// derive run that inherited QA's model, or whose answer never reached QA,
// would be a second run that saved nothing.
func TestCaseDeriveRunsAsItsOwnCheapActorAndItsListReachesQA(t *testing.T) {
	repo, baseSHA := deriveRepo(t)
	const cases = "- a total of 1.005 rounds to 1.01\n- a negative total rounds the same way"
	const criteria = "AC: totals are rounded to 2 decimal places"

	var deriveOpts, qaOpts supervisor.Options
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		switch o.Stage {
		case "qa-cases":
			deriveOpts = o
			return &supervisor.Result{ExitCode: 0, Final: cases}, nil
		default:
			qaOpts = o
			return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
		}
	}

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var out strings.Builder
	runQA(qaJob{Key: "FCIA-6", Summary: "round the total", Description: criteria,
		WS: &workspace.Workspace{RepoPath: repo}, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, log, &out)

	if deriveOpts.Actor != events.ActorCaseDerive || deriveOpts.Key != "FCIA-6" {
		t.Errorf("derive ran as actor %q on key %q, want %q on the ticket so its spend is "+
			"its own row in that ticket's cost report",
			deriveOpts.Actor, deriveOpts.Key, events.ActorCaseDerive)
	}
	if want := actors.Model(events.ActorCaseDerive); deriveOpts.Model != want {
		t.Errorf("derive model = %q, want the roster's %q -- pinning it cheap is the cost win",
			deriveOpts.Model, want)
	}
	if deriveOpts.MaxMinutes != caseDeriveMaxMinutes || deriveOpts.MaxTurns != caseDeriveMaxTurns {
		t.Errorf("derive bounds = %d min / %d turns, want the tight %d/%d",
			deriveOpts.MaxMinutes, deriveOpts.MaxTurns, caseDeriveMaxMinutes, caseDeriveMaxTurns)
	}
	if !strings.Contains(deriveOpts.Prompt, criteria) || !strings.Contains(deriveOpts.Prompt, "func round") {
		t.Errorf("the derive prompt did not carry the criteria and the diff:\n%s", deriveOpts.Prompt)
	}

	if !strings.Contains(qaOpts.Prompt, "1.005 rounds to 1.01") {
		t.Errorf("the derived cases never reached the QA run:\n%s", qaOpts.Prompt)
	}
	if strings.Contains(qaOpts.Prompt, criteria) {
		t.Errorf("QA carried the criteria as well as the cases, which is the reading this "+
			"split exists to stop paying for on every turn:\n%s", qaOpts.Prompt)
	}

	log.Close()
	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, e := range logged {
		if e.Actor == events.ActorCaseDerive && strings.Contains(e.Msg, "1.005 rounds to 1.01") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("the case list was never written to the event log; what a subagent returns "+
			"is all the parent ever sees of it, so an unrecorded answer is lost (OR-129):\n%+v",
			logged)
	}
}

// The fallback, which matters more than the saving: a derive step that
// produced nothing must never be the reason a ticket has no tests. QA runs
// anyway, with the criteria, exactly as it did before this step existed.
func TestQAStillRunsWhenTheCaseDeriveFails(t *testing.T) {
	repo, baseSHA := deriveRepo(t)
	const criteria = "AC: totals are rounded to 2 decimal places"

	var stages []string
	var qaPrompt string
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		stages = append(stages, o.Stage)
		if o.Stage == "qa-cases" {
			return &supervisor.Result{ExitCode: 1, Reason: "the breaker tripped"}, nil
		}
		qaPrompt = o.Prompt
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	outcome := runQA(qaJob{Key: "FCIA-6", Summary: "round the total", Description: criteria,
		WS: &workspace.Workspace{RepoPath: repo}, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	if !outcome.Ran || !outcome.Clean {
		t.Fatalf("a failed derive stopped QA: %+v", outcome)
	}
	if strings.Join(stages, ",") != "qa-cases,qa" {
		t.Fatalf("run sequence = %v, want the derive attempt and then QA anyway", stages)
	}
	if !strings.Contains(qaPrompt, criteria) {
		t.Errorf("QA fell back without the criteria, so it has nothing to derive from:\n%s", qaPrompt)
	}
	// Said out loud, for the reason the tooling path is: a run that quietly
	// took the more expensive path reads exactly like one that did not.
	if !strings.Contains(out.String(), "could not derive the cases") {
		t.Errorf("nothing said the derive step had failed:\n%s", out.String())
	}
}

// A ticket with no description has no criteria to derive from, and a diff
// alone would only re-state what the implementation already does. Spending a
// run to be told that is the "small job is pure overhead" case OR-143's bar
// rules out, so the step does not run at all.
func TestCaseDeriveIsSkippedWhenThereIsNothingToDeriveFrom(t *testing.T) {
	repo, baseSHA := deriveRepo(t)

	var stages []string
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		stages = append(stages, o.Stage)
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	runQA(qaJob{Key: "FCIA-6", Summary: "round the total",
		WS: &workspace.Workspace{RepoPath: repo}, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	if strings.Join(stages, ",") != "qa" {
		t.Errorf("run sequence = %v, want QA alone: there were no criteria to derive from", stages)
	}
}

// The mirror of TestCaseDeriveIsSkippedWhenThereIsNothingToDeriveFrom: this
// time the criteria exist but there is no base commit to diff against, so
// there is still nothing for the derive step to read the change out of.
func TestCaseDeriveIsSkippedWhenThereIsNoBaseSHA(t *testing.T) {
	repo, _ := deriveRepo(t)

	var stages []string
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		stages = append(stages, o.Stage)
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	runQA(qaJob{Key: "FCIA-6", Summary: "round the total", Description: "AC: totals round",
		WS: &workspace.Workspace{RepoPath: repo}},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	if strings.Join(stages, ",") != "qa" {
		t.Errorf("run sequence = %v, want QA alone: there was no base commit to diff against", stages)
	}
}

// A base commit that IS HEAD produces an empty diff -- there is nothing the
// change touched, so the derive step has nothing to add and must not spend a
// run finding that out.
func TestCaseDeriveIsSkippedWhenTheDiffIsEmpty(t *testing.T) {
	repo, _ := deriveRepo(t)
	headSHA := git(t, repo, "rev-parse", "HEAD")

	var stages []string
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		stages = append(stages, o.Stage)
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	runQA(qaJob{Key: "FCIA-6", Summary: "round the total", Description: "AC: totals round",
		WS: &workspace.Workspace{RepoPath: repo}, BaseSHA: headSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	if strings.Join(stages, ",") != "qa" {
		t.Errorf("run sequence = %v, want QA alone: base and HEAD are the same commit, so "+
			"there is no diff to derive cases from", stages)
	}
}

// countCases is the one line the console gets, so it has to count only actual
// cases -- not blank lines the agent's formatting left in, and not a header
// line it wrote despite being asked for none.
func TestCountCasesSkipsBlankLinesAndHeaders(t *testing.T) {
	cases := "Cases:\n- a negative total rounds down\n\n- a zero total stays zero\n"
	if got := countCases(cases); got != 2 {
		t.Errorf("countCases = %d, want 2: the header line and the blank line are not cases", got)
	}
}

// projectWithSuite is project (work_test.go) plus a scripts/test.sh that is
// already on the seed commit -- the one thing project deliberately leaves
// out, and the one thing this package's own red-before-green check needs to
// run at all.
func projectWithSuite(t *testing.T, cfgJSON, suite string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	git(t, root, "clone", "-q", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "spec.md"), []byte("# spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "orion.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExec(t, seed, "scripts/test.sh", suite)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-q", "-m", "seed")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, seed, "checkout", "-q", "-b", "develop")
	git(t, seed, "push", "-q", "origin", "develop")

	ws, err := workspace.Bind(workspace.BindOptions{
		SourcePath: seed, DefaultBranch: "main", WorkBranch: "develop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Bind(home, registry.Entry{
		Key: "FCIA", Source: seed, Workspace: ws.ID,
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

// findEventLog is the one thing projectWithSuite's caller cannot know ahead
// of time: which workspace directory Run bound to. There is exactly one in
// these tests, so the first events.jsonl found is it.
func findEventLog(t *testing.T, home string) string {
	t.Helper()
	var found string
	_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Base(path) == "events.jsonl" && found == "" {
			found = path
		}
		return nil
	})
	if found == "" {
		t.Fatal("no events.jsonl under the home dir")
	}
	return found
}

// The unit tests above call checkRedBeforeGreen and reportRedBeforeGreen
// directly with a hand-set BaseSHA. This is the boundary those miss: that
// work.go itself records the base commit at the moment the ticket's branch
// is cut -- before the implementer's own run touches anything -- and that
// runQA is actually reached with it through a full Run(). scripts/test.sh
// always exits 0 here, so whatever QA writes necessarily already passes on
// that pre-change commit, which is exactly the "unproven" case a wiring bug
// (e.g. capturing BaseSHA after the implementer's commit, when the suite
// would still be green anyway) would NOT be enough to catch by itself --
// what makes this a real check is that the file the fake QA run writes is
// asserted by name, proving the two ends of the wire actually connect.
func TestRedBeforeGreenIsWiredThroughARealRun(t *testing.T) {
	home := projectWithSuite(t, qaCfg, "#!/bin/sh\nexit 0\n")
	f := &qaFake{t: t, qaReplies: []string{"QA CLEAN"}}
	var out strings.Builder

	// qaFake's own run() writes "qa-2.go" (see its counting scheme), which is
	// a test-shaped filename under isTestFile's Go convention? It is not --
	// "qa-2.go" has no _test.go suffix, so it would be silently skipped and
	// this test would prove nothing. Override Supervise to write a real
	// *_test.go file for the QA stage instead, keeping the ticket stage as
	// qaFake already does it.
	sup := func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		if o.Stage != "qa" {
			return f.run(ws, o)
		}
		if err := os.WriteFile(filepath.Join(ws.RepoDir(), "weak_test.go"), []byte("package fake\n"), 0o644); err != nil {
			return nil, err
		}
		git(t, ws.RepoDir(), "add", ".")
		git(t, ws.RepoDir(), "commit", "-q", "-m", "test: qa")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-session", Final: "QA CLEAN"}, nil
	}

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Supervise: sup,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q: %+v", res[0].Outcome, res[0])
	}
	if !strings.Contains(out.String(), "weak_test.go") || !strings.Contains(out.String(), "prove nothing") {
		t.Fatalf("the console never reported weak_test.go as unproven:\n%s", out.String())
	}

	logged, err := events.Read(findEventLog(t, home))
	if err != nil {
		t.Fatal(err)
	}
	var loggedUnproven bool
	for _, e := range logged {
		if strings.Contains(e.Msg, "weak_test.go") && strings.Contains(e.Msg, "prove nothing") {
			loggedUnproven = true
		}
	}
	if !loggedUnproven {
		t.Errorf("the event log never recorded weak_test.go as unproven:\n%+v", logged)
	}
}

// qaRepo is a branch ready for the QA stage: a seed commit to use as the
// base, this repository's one test entry point, and nothing else.
func qaRepo(t *testing.T, suite string) (repo, baseSHA string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	writeExec(t, repo, "scripts/test.sh", suite)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "seed")
	return repo, git(t, repo, "rev-parse", "HEAD")
}

// committedAtHEAD lists what the branch actually carries, which is the only
// thing the push, the pull request and the red-before-green check can see.
func committedAtHEAD(t *testing.T, repo string) string {
	t.Helper()
	return git(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
}

// OR-234. QAPrompt asks QA to commit the tests it writes and QA does not
// reliably do it -- OR-217 left them STAGED, OR-213 UNSTAGED, fifteen minutes
// apart. Both, so a fix that handled only the state that happened to be
// easier to see would still fail here.
//
// The clean worktree at the end is the other half and not a tidiness point:
// collect's rebaseOnto refuses a tree with uncommitted tracked changes, so QA
// leaving one disables the automation that keeps the branch current. OR-233
// settles that residue whatever leaves it and stays necessary as a backstop;
// this is about QA not being the thing that leaves it.
func TestQAsTestsAreCommittedWhenTheAgentLeavesThemInTheWorktree(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	ws := &workspace.Workspace{RepoPath: repo}

	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		writeExec(t, w.RepoDir(), "staged_test.go", "package fake\n")
		writeExec(t, w.RepoDir(), "unstaged_test.go", "package fake\n")
		git(t, w.RepoDir(), "add", "staged_test.go")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	if outcome := runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out); !outcome.Clean {
		t.Fatalf("outcome = %+v", outcome)
	}

	tracked := committedAtHEAD(t, repo)
	for _, f := range []string{"staged_test.go", "unstaged_test.go"} {
		if !strings.Contains(tracked, f) {
			t.Errorf("%s is not committed on the branch, so the pull request would not carry it; "+
				"HEAD holds:\n%s", f, tracked)
		}
	}
	if dirty := git(t, repo, "status", "--porcelain"); dirty != "" {
		t.Errorf("the QA stage ended with a dirty worktree, which is what refuses the next rebase:\n%s", dirty)
	}
	if !strings.Contains(out.String(), "committed 2 file(s) QA left uncommitted") {
		t.Errorf("nothing said the commit was Orion's rather than QA's:\n%s", out.String())
	}
}

// The false negative OR-234 is really about. On OR-217 the check reported "QA
// did not add or change a test file" while the test that caught the defect sat
// in the worktree -- it inspects COMMITS, and QA had made none. So the control
// that proves a test would have caught the change failed silently in exactly
// the case where a test WAS written.
//
// The suite here fails when the test file is present without the implementer's
// change, so a correctly-wired check must report it PROVEN red -- not merely
// stop saying nothing was written.
func TestRedBeforeGreenSeesTheTestsQADidNotCommit(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\n"+
		"if [ -f sound_test.go ] && [ ! -f feature.flag ]; then\n"+
		"  exit 1\n"+
		"fi\n"+
		"exit 0\n")

	// The implementer's slice, committed before QA starts, as it always is.
	writeExec(t, repo, "feature.flag", "ENABLED\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "feat: add the feature")

	ws := &workspace.Workspace{RepoPath: repo}
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		writeExec(t, w.RepoDir(), "sound_test.go", "package fake\n")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	// The false negative was a LOG line -- "red-before-green not checked: QA
	// did not add or change a test file" -- so the log is where it has to be
	// shown absent, not only the console.
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var out strings.Builder
	runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, log, &out)
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var falseNegative, proven bool
	for _, e := range logged {
		if strings.Contains(e.Msg, "did not add or change a test file") {
			falseNegative = true
		}
		if strings.Contains(e.Msg, "proved red before green") && strings.Contains(e.Msg, "sound_test.go") {
			proven = true
		}
	}
	if falseNegative {
		t.Fatalf("red-before-green reported the OR-217 false negative with the test right there:\n%+v", logged)
	}
	if !proven {
		t.Errorf("the test QA wrote was never proven red:\n%+v\n%s", logged, out.String())
	}
}

// A red pull request is the CORRECT outcome for a change QA found a defect in,
// and it must not be avoided by omission. On OR-217 the branch was pushed and
// CI went green because the commit did not contain the failing test -- a green
// mergeable pull request carrying the bug, one approval from develop.
//
// So the commit happens on every exit from the stage, including the round
// ceiling with findings still open.
func TestQACommitsATestThatIsStillFailingAtTheRoundCeiling(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	ws := &workspace.Workspace{RepoPath: repo}

	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		if o.Stage != "qa" {
			// The fix round: the implementer changes nothing that clears it.
			return &supervisor.Result{ExitCode: 0, SessionID: "impl-1"}, nil
		}
		writeExec(t, w.RepoDir(), "boundary_test.go", "package fake\n")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1",
			Final: "The boundary case fails: a path of exactly 255 bytes is rejected."}, nil
	}

	var out strings.Builder
	outcome := runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{QA: config.QA{MaxRounds: 1}}, Options{}, Deps{Supervise: sup}, nil, &out)

	if outcome.Clean || outcome.Findings == "" {
		t.Fatalf("outcome = %+v; expected findings still open", outcome)
	}
	if tracked := committedAtHEAD(t, repo); !strings.Contains(tracked, "boundary_test.go") {
		t.Errorf("the failing test was left off the branch, so the pull request goes green without it; "+
			"HEAD holds:\n%s", tracked)
	}
	if dirty := git(t, repo, "status", "--porcelain"); dirty != "" {
		t.Errorf("the stage ended dirty even though it escalated:\n%s", dirty)
	}
}

// A QA run that wrote nothing has nothing for the pull request to carry, and
// commitQAWork must not manufacture an empty commit to mark that it ran.
func TestQAWithNoTestFilesMakesNoCommit(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	ws := &workspace.Workspace{RepoPath: repo}
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	outcome := runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)
	if !outcome.Clean {
		t.Fatalf("outcome = %+v", outcome)
	}
	if head := git(t, repo, "rev-parse", "HEAD"); head != baseSHA {
		t.Errorf("HEAD moved with nothing for QA to commit: %s -> %s", baseSHA, head)
	}
	if strings.Contains(out.String(), "committed") {
		t.Errorf("claimed a commit when nothing was written:\n%s", out.String())
	}
}

// Distinct from a brand-new untracked file: an edit to a file the branch
// already tracks (QA correcting its own earlier test, say) must also reach
// the commit, since `git add -A` covers modified tracked files as much as
// new ones.
func TestQAsEditToAnAlreadyTrackedTestFileIsCommitted(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	writeExec(t, repo, "existing_test.go", "package fake\n// v1\n")
	git(t, repo, "add", "existing_test.go")
	git(t, repo, "commit", "-q", "-m", "seed an existing test")
	baseSHA = git(t, repo, "rev-parse", "HEAD")

	ws := &workspace.Workspace{RepoPath: repo}
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		writeExec(t, w.RepoDir(), "existing_test.go", "package fake\n// v2\n")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	content := git(t, repo, "show", "HEAD:existing_test.go")
	if !strings.Contains(content, "v2") {
		t.Errorf("the edit to an already-tracked test file was not committed, HEAD holds:\n%s", content)
	}
	if dirty := git(t, repo, "status", "--porcelain"); dirty != "" {
		t.Errorf("the worktree is dirty after committing a tracked edit:\n%s", dirty)
	}
}

// commitQAWork's own comment is explicit that a failure here is a warning,
// never a failed run -- the change QA verified is still good even when the
// record of QA's own work could not be written (e.g. permission denied).
func TestCommitFailureIsLoggedAsAWarningNotAFailedRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The failure is provoked by chmod-ing a git directory read-only,
		// and chmod cannot take write access away from a directory on
		// Windows -- the commit succeeds and the premise cannot be built
		// (OR-342).
		t.Skip("a directory cannot be made unwritable with chmod on Windows")
	}
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	ws := &workspace.Workspace{RepoPath: repo}
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		writeExec(t, w.RepoDir(), "blocked_test.go", "package fake\n")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	gitDir := filepath.Join(repo, ".git")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(gitDir, 0o755)

	var out strings.Builder
	outcome := runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	if !outcome.Clean {
		t.Fatalf("a commit failure must not turn a clean QA run into a failed one: %+v", outcome)
	}
	if !strings.Contains(out.String(), "could not commit") {
		t.Errorf("the commit failure was not reported on the console:\n%s", out.String())
	}
}

// Everything after the QA stage reads commits, so QA's commit has to sit
// directly on the HEAD that existed before the stage started -- not behind
// other history the stage happened to pick up, and not squashed away.
func TestQACommitIsADirectChildOfThePreQAHead(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	ws := &workspace.Workspace{RepoPath: repo}
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		writeExec(t, w.RepoDir(), "child_test.go", "package fake\n")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}
	var out strings.Builder
	runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	if parent := git(t, repo, "rev-parse", "HEAD^"); parent != baseSHA {
		t.Errorf("QA's commit parent = %s, want the pre-QA HEAD %s", parent, baseSHA)
	}
	if n := git(t, repo, "rev-list", "--count", baseSHA+"..HEAD"); n != "1" {
		t.Errorf("expected exactly one commit ahead of the pre-QA HEAD, got %s", n)
	}
}

// The message is what a reviewer reading `git log` sees, and it has to
// answer the question that commit raises on sight: who wrote this, and is a
// red test here a broken branch or the correct outcome.
func TestQACommitMessageNamesTheTicketAndOrionAndWarnsOfFailure(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	ws := &workspace.Workspace{RepoPath: repo}
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		writeExec(t, w.RepoDir(), "msg_test.go", "package fake\n")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}
	var out strings.Builder
	runQA(qaJob{Key: "OR-234", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	msg := git(t, repo, "log", "-1", "--format=%B")
	if !strings.Contains(msg, "OR-234") {
		t.Errorf("the commit message does not name the ticket:\n%s", msg)
	}
	if !strings.Contains(msg, "Orion") {
		t.Errorf("the commit message does not say Orion made the commit, not QA:\n%s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "fail") {
		t.Errorf("the commit message does not warn the tests inside may be failing:\n%s", msg)
	}
}

// QA's files often land in more than one package. The pull request and
// red-before-green both read commits, not directories, so files from
// different directories in the same round must still land in the one commit.
func TestQAsTestsAcrossMultipleDirectoriesAreCommittedTogether(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	ws := &workspace.Workspace{RepoPath: repo}
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		writeExec(t, w.RepoDir(), "pkg/a/a_test.go", "package a\n")
		writeExec(t, w.RepoDir(), "pkg/b/b_test.go", "package b\n")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}
	var out strings.Builder
	runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{}, Options{}, Deps{Supervise: sup}, nil, &out)

	tracked := committedAtHEAD(t, repo)
	for _, f := range []string{"pkg/a/a_test.go", "pkg/b/b_test.go"} {
		if !strings.Contains(tracked, f) {
			t.Errorf("%s is not committed; HEAD holds:\n%s", f, tracked)
		}
	}
	if n := git(t, repo, "rev-list", "--count", baseSHA+"..HEAD"); n != "1" {
		t.Errorf("expected the two directories' files in one commit, got %s commits", n)
	}
}

// A test QA wrote in round 1 must not be lost by the time round 2 ends
// clean -- the commit happens once, after every round, over everything the
// worktree still holds.
func TestFilesFromAnEarlierRoundSurviveALaterRound(t *testing.T) {
	repo, baseSHA := qaRepo(t, "#!/bin/sh\nexit 0\n")
	ws := &workspace.Workspace{RepoPath: repo}
	round := 0
	sup := func(w *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		if o.Stage != "qa" {
			return &supervisor.Result{ExitCode: 0, SessionID: "impl-1"}, nil
		}
		round++
		if round == 1 {
			writeExec(t, w.RepoDir(), "round1_test.go", "package fake\n")
			return &supervisor.Result{ExitCode: 0, SessionID: "qa-1",
				Final: "case X fails."}, nil
		}
		writeExec(t, w.RepoDir(), "round2_test.go", "package fake\n")
		return &supervisor.Result{ExitCode: 0, SessionID: "qa-1", Final: supervisor.QAClean}, nil
	}

	var out strings.Builder
	outcome := runQA(qaJob{Key: "FCIA-6", WS: ws, BaseSHA: baseSHA},
		config.Config{QA: config.QA{MaxRounds: 2}}, Options{}, Deps{Supervise: sup}, nil, &out)
	if !outcome.Clean {
		t.Fatalf("outcome = %+v", outcome)
	}
	tracked := committedAtHEAD(t, repo)
	for _, f := range []string{"round1_test.go", "round2_test.go"} {
		if !strings.Contains(tracked, f) {
			t.Errorf("%s from an earlier round is missing once QA went clean; HEAD holds:\n%s", f, tracked)
		}
	}
}

// The sentinel decides the verdict, and only when it starts a line. An agent
// that quotes its own instructions must not declare a clean branch by
// describing one.
//
// OR-204 added the third state: a message that names neither the sentinel nor
// a failure is NOT findings. Prose that concludes everything passes was
// indistinguishable from a report of real defects, and the difference decided
// whether an opus fix round ran.
func TestQAVerdictReadsTheSentinelAndNotThePraise(t *testing.T) {
	cases := []struct {
		final string
		want  qaVerdictKind
	}{
		{"everything passed\nQA CLEAN", qaVerdictClean},
		{"**QA CLEAN**", qaVerdictClean},
		{"You told me to write QA CLEAN when every case passes.\nCase 3 fails.", qaVerdictFindings},
		{"Case 3 fails: expected 400, got 500.", qaVerdictFindings},
		{"The rounding is wrong: expected 2 decimal places, got 4.", qaVerdictFindings},
		// The two that used to be read as findings, and are not.
		{"   ", qaVerdictNone},
		{"All tests pass and the change looks good to me.", qaVerdictNone},
		// The OR-194 message, near enough: a verified pass, written as prose.
		{"**Coverage already present** for every case I derived. All pass.", qaVerdictNone},
	}
	for _, c := range cases {
		said, kind := qaVerdict(c.final)
		if kind != c.want {
			t.Errorf("qaVerdict(%q) kind = %v, want %v", c.final, kind, c.want)
		}
		if kind != qaVerdictClean && strings.TrimSpace(said) == "" {
			t.Errorf("qaVerdict(%q) reported nothing and no pass; a reader learns nothing", c.final)
		}
	}
}

// Every stem namesAFailure is supposed to catch, each in a natural sentence
// that is not the sentinel -- so a QA report using any of these words for its
// defect is read as findings and not silently treated as no verdict, which
// would cost a real defect a cheap re-ask instead of a fix round.
func TestQAVerdictRecognizesEveryFailureStem(t *testing.T) {
	sentences := map[string]string{
		"fail":       "Case 3 fails on the boundary value.",
		"defect":     "Found a defect in the rounding path.",
		"broke":      "The refactor broke the discount calculation.",
		"regress":    "This regressed the totals for negative amounts.",
		"incorrect":  "The tax total is incorrect for zero-quantity orders.",
		"wrong":      "The rounding is wrong: expected 2 decimal places, got 4.",
		"missing":    "Authorisation check is missing on the delete endpoint.",
		"mismatch":   "There is a type mismatch between the two totals.",
		"crash":      "The handler crashes on an empty payload.",
		"panic":      "The parser panics on malformed input.",
		"unable":     "The client is unable to reach the retry path.",
		"problem":    "There is a problem with the pagination cursor.",
		"unexpected": "Got an unexpected status code for the same request.",
	}
	for stem, s := range sentences {
		if _, kind := qaVerdict(s); kind != qaVerdictFindings {
			t.Errorf("qaVerdict(%q) [stem %q] kind = %v, want qaVerdictFindings", s, stem, kind)
		}
	}
}

// Words that look like a failure stem but are not one, and are not preceded
// by a negator either -- the two ways this list could misfire in opposite
// directions. A prompt or report using ordinary engineering vocabulary must
// not be read as a defect report.
func TestQAVerdictAvoidsFalsePositiveFailureWords(t *testing.T) {
	sentences := []string{
		"Ran with debug logging enabled for the whole suite.",
		"The passfail summary printed at the end of the run.",
		"Coverage looks complete for every case in the list.",
	}
	for _, s := range sentences {
		if _, kind := qaVerdict(s); kind != qaVerdictNone {
			t.Errorf("qaVerdict(%q) kind = %v, want qaVerdictNone (no failure word present)", s, kind)
		}
	}
}

// A report of a pass stated in the negative is a pass, not a defect: "no
// failures" and "nothing broke" name a failure word but deny it, and reading
// past the negation is exactly the misread namesAFailure exists to avoid.
// Neither is the sentinel, so both fall out as no verdict -- a re-ask, never
// a fix round.
func TestQAVerdictReadsNegatedFailureWordsAsNoVerdict(t *testing.T) {
	sentences := []string{
		"Ran every case; no failures.",
		"Nothing broke after the change.",
	}
	for _, s := range sentences {
		if _, kind := qaVerdict(s); kind != qaVerdictNone {
			t.Errorf("qaVerdict(%q) kind = %v, want qaVerdictNone (negated failure word)", s, kind)
		}
	}
}

// OR-204. A QA run that verified everything but wrote its conclusion as prose
// must not be routed into a fix round: the implementer would be told to fix a
// defect nobody described, and the cheapest way to obey that is to weaken the
// nearest assertion -- which makes CI greener, not redder.
//
// One re-ask on QA's own model, and the sentinel this time. No ticket-stage
// run may be dispatched in between.
func TestQAWithoutAVerdictIsReAskedRatherThanFixed(t *testing.T) {
	home := project(t, qaCfg)
	j := &fakeJira{}
	f := &qaFake{t: t, qaReplies: []string{
		"**Coverage already present** for every case I derived. All pass.",
		"QA CLEAN",
	}}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	// implement, derive the cases, verify, re-ask. No second "ticket" run:
	// that would be the opus fix round this ticket exists to prevent.
	if got := f.sequence(); got != "ticket,qa-cases,qa,qa" {
		t.Fatalf("run sequence = %q, want ticket,qa-cases,qa,qa (a fix round was dispatched "+
			"on a verdict Orion did not have)", got)
	}
	if p := f.prompts[3]; !strings.Contains(p, supervisor.QAClean) ||
		!strings.Contains(strings.ToLower(p), "without findings") {
		t.Errorf("the re-ask did not ask for a verdict line:\n%s", p)
	}
	if strings.Contains(f.prompts[3], "Fix the behaviour they describe") {
		t.Error("the re-ask carried the findings message; QA was told to fix its own report")
	}
	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q; the re-asked sentinel must clear the stage: %+v",
			res[0].Outcome, res[0])
	}
	if c := strings.Join(j.comments, "\n"); !strings.Contains(c, "found nothing") {
		t.Errorf("a re-asked clean verdict was not reported as clean on the ticket:\n%s", c)
	}
	if strings.Contains(out.String(), "A person needs to look") {
		t.Error("a clean branch escalated to a person")
	}
}

// The re-ask is a question, not an acquittal: findings on the second ask run
// the ordinary fix loop, with those findings.
func TestQAReAskedIntoFindingsRunsTheNormalFixLoop(t *testing.T) {
	home := project(t, qaCfg)
	f := &qaFake{t: t, qaReplies: []string{
		"I looked at the rounding, the boundaries and the error branch.",
		"The rounding case is wrong: expected 2 decimal places, got 4.",
		"QA CLEAN",
	}}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if got := f.sequence(); got != "ticket,qa-cases,qa,qa,ticket,qa" {
		t.Fatalf("run sequence = %q, want ticket,qa-cases,qa,qa,ticket,qa", got)
	}
	if !strings.Contains(f.prompts[4], "2 decimal places") {
		t.Errorf("the re-asked findings did not reach the developer:\n%s", f.prompts[4])
	}
	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q: %+v", res[0].Outcome, res[0])
	}
}

// Asked twice and still nothing: a person is told, and what they are told is
// that the verdict was never obtained -- not that there were findings. No fix
// round runs on the way there.
func TestQAWithNoVerdictTwiceGoesToAPersonAndNotToAFixRound(t *testing.T) {
	home := project(t, qaCfg)
	j := &fakeJira{}
	f := &qaFake{t: t, qaReplies: []string{"All pass, looks good."}}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if got := f.sequence(); got != "ticket,qa-cases,qa,qa" {
		t.Fatalf("run sequence = %q, want ticket,qa-cases,qa,qa (one re-ask, no fix round)", got)
	}
	// The run CONTINUES: QA does not block, and "no verdict" is weaker
	// ground to stop on than findings would be.
	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q; an unread verdict must not fail the ticket: %+v",
			res[0].Outcome, res[0])
	}

	logged, err := events.Read(findEventLog(t, home))
	if err != nil {
		t.Fatal(err)
	}
	var saidNoVerdict bool
	for _, e := range logged {
		if e.Kind == events.KindEscalate && strings.Contains(e.Msg, "never reported a verdict") {
			saidNoVerdict = true
		}
		if strings.Contains(e.Msg, "findings are still open") {
			t.Errorf("the run log reported findings for a verdict it never got: %q", e.Msg)
		}
	}
	if !saidNoVerdict {
		t.Errorf("the run log never said the verdict was not obtained:\n%+v", logged)
	}
	if c := strings.Join(j.comments, "\n"); !strings.Contains(c, "never reported a verdict") {
		t.Errorf("the ticket was not told the verdict was never obtained:\n%s", c)
	}
	if !strings.Contains(out.String(), "gave no verdict") {
		t.Errorf("the console never said the verdict was missing:\n%s", out.String())
	}
}

// An empty closing message is the same unknown -- and its stand-in text is
// Orion's own words about QA, which must never be handed to the implementer
// as a defect to fix.
func TestQASayingNothingIsNotADefectToFix(t *testing.T) {
	home := project(t, qaCfg)
	f := &qaFake{t: t, qaReplies: []string{"", "QA CLEAN"}}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if got := f.sequence(); got != "ticket,qa-cases,qa,qa" {
		t.Fatalf("run sequence = %q, want ticket,qa-cases,qa,qa", got)
	}
	for i, p := range f.prompts {
		if strings.Contains(p, "QA finished without reporting anything") {
			t.Errorf("prompt %d handed Orion's own no-report message to an agent to fix:\n%s", i, p)
		}
	}
}

// The re-ask is meant to be one short run on QA's model, not a second full
// verification: that is what makes it cheaper than the fix round it replaces.
func TestTheVerdictReAskIsShortAndRunsAsQA(t *testing.T) {
	home := project(t, qaCfg)
	f := &qaFake{t: t, qaReplies: []string{"All pass.", "QA CLEAN"}}
	var opts []supervisor.Options
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{},
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				opts = append(opts, o)
				return f.run(ws, o)
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if len(opts) != 4 {
		t.Fatalf("%d runs, want 4: %v", len(opts), f.sequence())
	}
	ask := opts[3]
	if ask.Actor != events.ActorQA || ask.Model != actors.Model(events.ActorQA) {
		t.Errorf("the re-ask ran as %q on %q, want QA on QA's model", ask.Actor, ask.Model)
	}
	// Resumed, not a fresh run: the context it is being asked about is the
	// verification it just did.
	if ask.Resume != "qa-session" {
		t.Errorf("the re-ask did not resume QA's session: resume = %q", ask.Resume)
	}
	if ask.MaxMinutes != qaVerdictMaxMinutes || ask.MaxTurns != qaVerdictMaxTurns {
		t.Errorf("the re-ask ran with %d minutes / %d turns, want the short verdict bounds "+
			"(%d/%d)", ask.MaxMinutes, ask.MaxTurns, qaVerdictMaxMinutes, qaVerdictMaxTurns)
	}
}
